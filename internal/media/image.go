// Package media analyses downloaded images and videos and acknowledges
// them, so the collector deletes the files: images are resized and sent to
// a vision model; videos give up to N evenly spaced key frames (ffmpeg) and
// a speech-to-text transcript. Only the analysis is kept.
package media

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"net/http"
	"strings"
	// Decoders for the formats Facebook and other platforms serve.
	_ "image/gif"
	_ "image/png"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// DefaultMaxPixels is the pixel limit for tools without a config
// (analysis.media.image_max_pixels has the same default).
const DefaultMaxPixels = 40_000_000

// ErrUndecodable means the bytes are not an image we can read: a permanent
// failure.
var ErrUndecodable = errors.New("media: undecodable image")

// ErrNotReadYet means the bytes are an image in a known format that this
// service can't read yet (HEIC/HEIF, AVIF, JPEG XL, BMP, TIFF): the file
// waits rather than fail, until it can be read or its deadline nears. It
// wraps ErrUndecodable.
var ErrNotReadYet = errors.New("an image format not read yet")

// Sniff names a file's format from its first bytes, for an error when it
// can't be read: an image format ("heif-heic", "avif", "bmp"...), a video sent
// as an image ("video ftyp isom"), or else its content type
// ("text/html; charset=utf-8"). It tells whether the format is a known
// still image (one a converter could read).
func Sniff(b []byte) (name string, image bool) {
	switch {
	case len(b) >= 12 && string(b[4:8]) == "ftyp":
		brand := strings.TrimRight(string(b[8:12]), " \x00")
		switch brand {
		case "heic", "heix", "heim", "heis", "hevc", "hevx", "mif1", "msf1":
			return "heif-" + brand, true
		case "avif", "avis":
			return "avif", true
		case "jxl":
			return "jxl", true
		}
		return "video ftyp " + brand, false
	case bytes.HasPrefix(b, []byte{0xFF, 0x0A}), bytes.HasPrefix(b, []byte("\x00\x00\x00\x0cJXL ")):
		return "jxl", true
	case bytes.HasPrefix(b, []byte("BM")):
		return "bmp", true
	case bytes.HasPrefix(b, []byte("II*\x00")), bytes.HasPrefix(b, []byte("MM\x00*")):
		return "tiff", true
	}
	return http.DetectContentType(b), false
}

// undecodable wraps a decoding error; an unknown format names what the
// bytes look like, and a known image format not read yet is ErrNotReadYet.
func undecodable(data []byte, err error) error {
	if !errors.Is(err, image.ErrFormat) {
		return fmt.Errorf("%w: %w", ErrUndecodable, err)
	}
	name, img := Sniff(data)
	if img {
		return fmt.Errorf("%w: %w (%s): %w", ErrUndecodable, err, name, ErrNotReadYet)
	}
	return fmt.Errorf("%w: %w (%s)", ErrUndecodable, err, name)
}

// Resize decodes an image and re-encodes it as JPEG with its longer side at
// most maxSide pixels (smaller images keep their size). An image of more
// than maxPixels (0 = no limit) is ErrTooLarge, judged from its header
// before anything is decoded: a small file can claim a huge picture.
func Resize(data []byte, maxSide, maxPixels int) ([]byte, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, undecodable(data, err)
	}
	if maxPixels > 0 && int64(cfg.Width)*int64(cfg.Height) > int64(maxPixels) {
		return nil, fmt.Errorf("%w: %dx%d pixels", ErrTooLarge, cfg.Width, cfg.Height)
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, undecodable(data, err)
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return nil, ErrUndecodable
	}
	if maxSide > 0 && (w > maxSide || h > maxSide) {
		if w >= h {
			h = max(1, h*maxSide/w)
			w = maxSide
		} else {
			w = max(1, w*maxSide/h)
			h = maxSide
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: 85}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
