package media

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
)

// ErrNoFFmpeg means video analysis is enabled but ffmpeg is missing: the
// image needs the analysis-media target.
var ErrNoFFmpeg = errors.New("media: ffmpeg not found (build the image with --target analysis-media)")

// Frames are a video's key frames and audio, extracted to a temp dir.
type Frames struct {
	Dir    string
	Images [][]byte
	Audio  []byte // mono 16 kHz MP3, nil when the video has no audio
}

// Close removes the temporary files.
func (f *Frames) Close() error {
	if f.Dir == "" {
		return nil
	}
	return os.RemoveAll(f.Dir)
}

// ExtractFrames runs ffmpeg on a video file: up to maxFrames evenly spaced
// frames scaled to side pixels on the longer side, and the first
// maxSeconds of audio.
func ExtractFrames(ctx context.Context, ffmpeg, video, tempDir string, durationMS int64, maxFrames, side, maxSeconds int) (*Frames, error) {
	if _, err := exec.LookPath(ffmpeg); err != nil {
		return nil, ErrNoFFmpeg
	}
	dir, err := os.MkdirTemp(tempDir, "sb-video-")
	if err != nil {
		return nil, err
	}
	f := &Frames{Dir: dir}
	dur := float64(durationMS) / 1000
	if dur <= 0 {
		dur = 30
	}
	if maxSeconds > 0 && dur > float64(maxSeconds) {
		dur = float64(maxSeconds)
	}
	interval := dur / float64(max(maxFrames, 1))
	if interval < 1 {
		interval = 1
	}
	scale := fmt.Sprintf("scale='if(gt(iw,ih),%d,-2)':'if(gt(iw,ih),-2,%d)'", side, side)
	frameArgs := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-t", strconv.Itoa(int(dur) + 1), "-i", video,
		"-vf", fmt.Sprintf("fps=1/%.3f,%s", interval, scale), "-frames:v", strconv.Itoa(maxFrames), "-q:v", "4",
		filepath.Join(dir, "f%03d.jpg")}
	// #nosec G204 -- ffmpeg path from config, arguments built here
	if out, err := exec.CommandContext(ctx, ffmpeg, frameArgs...).CombinedOutput(); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("ffmpeg frames: %v: %s", err, lastLine(out))
	}
	files, _ := filepath.Glob(filepath.Join(dir, "f*.jpg"))
	sort.Strings(files)
	for _, p := range files {
		b, err := os.ReadFile(p) // #nosec G304 -- our temp dir
		if err != nil {
			_ = f.Close()
			return nil, err
		}
		f.Images = append(f.Images, b)
	}
	audio := filepath.Join(dir, "audio.mp3")
	audioArgs := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-i", video, "-vn", "-ac", "1", "-ar", "16000",
		"-c:a", "libmp3lame", "-b:a", "32k", "-t", strconv.Itoa(max(maxSeconds, 1)), audio}
	// #nosec G204 -- as above
	if _, err := exec.CommandContext(ctx, ffmpeg, audioArgs...).CombinedOutput(); err == nil {
		if b, err := os.ReadFile(audio); err == nil && len(b) > 1024 { // #nosec G304
			f.Audio = b
		}
	}
	if len(f.Images) == 0 {
		_ = f.Close()
		return nil, fmt.Errorf("%w: no frames extracted", ErrUndecodable)
	}
	return f, nil
}

func lastLine(b []byte) string {
	s := string(b)
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	if i := lastIndex(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

func lastIndex(s string, c byte) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == c {
			return i
		}
	}
	return -1
}
