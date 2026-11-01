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
	// Decoders for the formats Facebook and other platforms serve.
	_ "image/gif"
	_ "image/png"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// ErrUndecodable means the bytes are not an image we can read: a permanent
// failure.
var ErrUndecodable = errors.New("media: undecodable image")

// Resize decodes an image and re-encodes it as JPEG with its longer side at
// most maxSide pixels (smaller images keep their size).
func Resize(data []byte, maxSide int) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUndecodable, err)
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
