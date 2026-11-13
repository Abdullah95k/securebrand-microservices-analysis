package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/config"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/core"
)

// zeros reads as an endless run of zero bytes, allocating nothing.
type zeros struct{}

func (zeros) Read(p []byte) (int, error) { clear(p); return len(p), nil }

// sized serves every object as size bytes, generated on the fly.
type sized struct{ size int64 }

func (o sized) Fetch(_ context.Context, _, _ string, max int64, dst io.Writer) (int64, error) {
	return CopyLimited(dst, io.LimitReader(zeros{}, o.size), max)
}

func allocated(f func()) uint64 {
	var a, b runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&a)
	f()
	runtime.ReadMemStats(&b)
	return b.TotalAlloc - a.TotalAlloc
}

func defaults(t *testing.T) config.AnalysisMedia {
	t.Helper()
	c, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	return c.Analysis.Media
}

// The largest video allowed (analysis.media.max_bytes, 500 MiB) goes to disk
// without passing through memory: the whole fetch allocates a few buffers,
// whatever the file's size. One byte more is refused, and leaves no file.
func TestLargestVideoStreamsToDisk(t *testing.T) {
	cfg := defaults(t)
	cfg.Video.TempDir = t.TempDir()
	job := core.MediaJob{MediaID: "m1", Kind: "video", Bucket: "b", Key: "k"}

	w := &Worker{Objects: sized{cfg.MaxBytes}, Cfg: cfg}
	var path string
	var err error
	got := allocated(func() { path, err = w.fetchVideo(context.Background(), job) })
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil || st.Size() != cfg.MaxBytes {
		t.Fatalf("file %v, %v; want %d bytes", st, err, cfg.MaxBytes)
	}
	_ = os.Remove(path)
	if got > 4<<20 {
		t.Fatalf("fetching a %d MiB video allocated %d MiB; it must stream", cfg.MaxBytes>>20, got>>20)
	}
	t.Logf("%d MiB video fetched with %d KiB allocated", cfg.MaxBytes>>20, got>>10)

	w.Objects = sized{cfg.MaxBytes + 1}
	if _, err := w.fetchVideo(context.Background(), job); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("a video over max_bytes: %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(cfg.Video.TempDir, "*")); len(left) != 0 {
		t.Fatalf("files left behind: %v", left)
	}
}

// gifClaiming is a GIF header announcing a w x h picture: a few bytes that
// would decode into gigabytes.
func gifClaiming(w, h uint16) []byte {
	b := []byte("GIF89a")
	b = binary.LittleEndian.AppendUint16(b, w)
	b = binary.LittleEndian.AppendUint16(b, h)
	return append(b, 0, 0, 0, ',', 0, 0, 0, 0, 1, 0, 1, 0, 0, 2, 2, 0x44, 1, 0, ';')
}

// An image is held in memory, so it has its own limits: bytes
// (image_max_bytes) and pixels (image_max_pixels), the latter judged from
// the header before decoding.
func TestImageLimits(t *testing.T) {
	cfg := defaults(t)
	var buf bytes.Buffer
	if _, err := (sized{cfg.ImageMaxBytes + 1}).Fetch(context.Background(), "b", "k", cfg.ImageMaxBytes, &buf); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("an image over image_max_bytes: %v", err)
	}
	bomb := gifClaiming(65535, 65535)
	var err error
	got := allocated(func() { _, err = Resize(bomb, cfg.ImageMaxSide, cfg.ImageMaxPixels) })
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("a 65535x65535 picture: %v", err)
	}
	if got > 1<<20 {
		t.Fatalf("refusing a picture bomb allocated %d KiB; it must not decode", got>>10)
	}
}

func TestFitWorkers(t *testing.T) {
	const mib = 1 << 20
	for _, tc := range []struct {
		configured int
		limit, per int64
		want       int
	}{
		{4, 2048 * mib, 384 * mib, 4}, // the chart's media workers: 4 fit in 2 GiB
		{8, 2048 * mib, 384 * mib, 4},
		{4, 1024 * mib, 384 * mib, 2},
		{4, 300 * mib, 384 * mib, 1}, // always at least one
		{4, 0, 384 * mib, 4},         // no limit known
		{0, 0, 0, 1},
	} {
		if got := FitWorkers(tc.configured, tc.limit, tc.per); got != tc.want {
			t.Errorf("FitWorkers(%d, %d MiB, %d MiB) = %d, want %d", tc.configured, tc.limit/mib, tc.per/mib, got, tc.want)
		}
	}
}

func TestContainerMemoryLimit(t *testing.T) {
	write := func(dir, name, v string) {
		p := filepath.Join(dir, name)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte(v+"\n"), 0o644)
	}
	v2 := t.TempDir()
	write(v2, "memory.max", "2147483648")
	unlimited := t.TempDir()
	write(unlimited, "memory.max", "max")
	v1 := t.TempDir()
	write(v1, "memory/memory.limit_in_bytes", "1073741824")
	v1none := t.TempDir()
	write(v1none, "memory/memory.limit_in_bytes", "9223372036854771712")
	for root, want := range map[string]int64{v2: 2 << 30, unlimited: 0, v1: 1 << 30, v1none: 0, t.TempDir(): 0} {
		if got := ContainerMemoryLimit(root); got != want {
			t.Errorf("%s: %d, want %d", root, got, want)
		}
	}
}

// chartMedia returns analysis-media's memory limit and /tmp size, in bytes,
// from the chart's values files merged in order (a later file's value wins,
// as with helm -f).
func chartMedia(t *testing.T, files ...string) (limit, tmp int64) {
	t.Helper()
	var lim, tmpSize string
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join("..", "..", "deploy", "helm", "sb-analysis", f))
		if err != nil {
			t.Fatal(err)
		}
		var v struct {
			Components map[string]struct {
				Resources struct {
					Limits struct {
						Memory string `yaml:"memory"`
					} `yaml:"limits"`
				} `yaml:"resources"`
				TmpSizeLimit string `yaml:"tmpSizeLimit"`
			} `yaml:"components"`
		}
		if err := yaml.Unmarshal(b, &v); err != nil {
			t.Fatal(err)
		}
		c := v.Components["analysis-media"]
		if c.Resources.Limits.Memory != "" {
			lim = c.Resources.Limits.Memory
		}
		if c.TmpSizeLimit != "" {
			tmpSize = c.TmpSizeLimit
		}
	}
	return quantity(t, lim), quantity(t, tmpSize)
}

// quantity reads a Kubernetes size in Mi or Gi ("3584Mi", "2Gi").
func quantity(t *testing.T, q string) int64 {
	t.Helper()
	for u, f := range map[string]int64{"Mi": 1 << 20, "Gi": 1 << 30} {
		if n, ok := strings.CutSuffix(q, u); ok {
			if x, err := strconv.ParseInt(n, 10, 64); err == nil && x > 0 {
				return x * f
			}
		}
	}
	t.Fatalf("size %q not understood", q)
	return 0
}

// chartConfig writes the "config" section of a chart values file to a file
// of its own, as the chart's ConfigMap renders it.
func chartConfig(t *testing.T, values string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "deploy", "helm", "sb-analysis", values))
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Config map[string]any `yaml:"config"`
	}
	if err := yaml.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	out, err := yaml.Marshal(v.Config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), values)
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The chart gives the media workers a memory limit the default number of
// workers fits.
func TestChartMediaLimitFitsWorkers(t *testing.T) {
	lim, _ := chartMedia(t, "values.yaml")
	cfg := defaults(t)
	if got := FitWorkers(cfg.Workers, lim, int64(cfg.WorkerMemoryMiB)<<20); got != cfg.Workers {
		t.Fatalf("the chart's %d MiB fits %d of the %d default media workers", lim>>20, got, cfg.Workers)
	}
}

// whisperServers is production's number of speech-to-text servers
// (whisper.replicas, set by the platform; D-052): each holds one media
// worker for the whole of a transcription.
const whisperServers = 2

// Production's media workers (the master, 13 November 2026: 8) all start,
// none lowered to fit the memory limit; at least 4 are left for images
// while every speech-to-text slot is busy; and /tmp holds a video of the
// largest size for each.
func TestProductionMediaLimitFitsWorkers(t *testing.T) {
	c, err := config.Load(chartConfig(t, "values.yaml") + "," + chartConfig(t, "values-production.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	m := c.Analysis.Media
	lim, tmp := chartMedia(t, "values.yaml", "values-production.yaml")
	if got := FitWorkers(m.Workers, lim, int64(m.WorkerMemoryMiB)<<20); got != m.Workers {
		t.Errorf("production's %d MiB fits %d of its %d media workers", lim>>20, got, m.Workers)
	}
	if images := m.Workers - max(m.Video.STTConcurrency, whisperServers); images < 4 {
		t.Errorf("%d media workers leave %d for images while speech-to-text is busy, want at least 4", m.Workers, images)
	}
	if need := int64(m.Workers) * m.MaxBytes; tmp < need {
		t.Errorf("/tmp of %d MiB for %d workers' videos of up to %d MiB, want at least %d MiB",
			tmp>>20, m.Workers, m.MaxBytes>>20, need>>20)
	}
}
