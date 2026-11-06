package media

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// cgroupRoot is where the container's cgroup files are mounted.
var cgroupRoot = "/sys/fs/cgroup"

// reservedMemory is kept for the rest of the process (the Go runtime, the
// database pool, the Kafka client) when sizing the media workers.
const reservedMemory = 256 << 20

// ContainerMemoryLimit returns the container's memory limit in bytes from
// its cgroup (v2 memory.max, else v1 memory.limit_in_bytes), or 0 when
// there is none or it cannot be read.
func ContainerMemoryLimit(root string) int64 {
	for _, f := range []string{"memory.max", filepath.Join("memory", "memory.limit_in_bytes")} {
		b, err := os.ReadFile(filepath.Join(root, f)) // #nosec G304 -- fixed cgroup paths
		if err != nil {
			continue
		}
		v := strings.TrimSpace(string(b))
		if v == "max" {
			return 0
		}
		n, err := strconv.ParseInt(v, 10, 64)
		// cgroup v1 reports "no limit" as a huge number.
		if err != nil || n <= 0 || n >= 1<<50 {
			return 0
		}
		return n
	}
	return 0
}

// FitWorkers returns how many media workers to run: the configured number,
// lowered so that each worker's worst case (perWorker bytes: a decoded image
// at image_max_pixels, or an ffmpeg run) fits the container's limit after
// reservedMemory; at least one. With no limit or no estimate, the
// configured number.
func FitWorkers(configured int, limit, perWorker int64) int {
	configured = max(configured, 1)
	if limit <= 0 || perWorker <= 0 {
		return configured
	}
	fit := int((limit - reservedMemory) / perWorker)
	return max(min(configured, fit), 1)
}
