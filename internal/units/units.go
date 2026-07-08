// Package units converts between the human-readable CPU/memory strings used
// in config.yaml (e.g. "4", "1.5", "8g", "512m") and the numeric values Docker
// and the metrics system operate on. It is the single owner of these formats;
// no other package should parse or format resource strings.
package units

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ParseCPU converts a CPU string like "4" or "1.5" to NanoCPUs.
func ParseCPU(cpus string) (int64, error) {
	f, err := strconv.ParseFloat(cpus, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid CPU value %q: %w", cpus, err)
	}
	return int64(f * 1e9), nil
}

// ParseMemory converts a memory string like "8g", "512m", "1024k", or a plain
// byte count like "1073741824" to bytes.
func ParseMemory(mem string) (int64, error) {
	mem = strings.TrimSpace(mem)
	if len(mem) == 0 {
		return 0, fmt.Errorf("empty memory string")
	}

	suffix := strings.ToLower(mem[len(mem)-1:])
	numStr := mem[:len(mem)-1]
	num, err := strconv.ParseFloat(numStr, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid memory value %q: %w", mem, err)
	}

	switch suffix {
	case "g":
		return int64(num * 1024 * 1024 * 1024), nil
	case "m":
		return int64(num * 1024 * 1024), nil
	case "k":
		return int64(num * 1024), nil
	default:
		b, err := strconv.ParseInt(mem, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("unknown memory suffix %q", suffix)
		}
		return b, nil
	}
}

// FormatCPU converts NanoCPUs back to a CPU string (e.g. "2", "1.5", "2.25").
func FormatCPU(nanoCPUs float64) string {
	cpus := nanoCPUs / 1e9
	if cpus == math.Trunc(cpus) {
		return strconv.Itoa(int(cpus))
	}
	s := strconv.FormatFloat(cpus, 'f', 2, 64)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	return s
}

// FormatMemory converts bytes back to a memory string (e.g. "8g", "512m").
func FormatMemory(bytes float64) string {
	gb := bytes / (1024 * 1024 * 1024)
	if gb == math.Trunc(gb) && gb >= 1 {
		return fmt.Sprintf("%dg", int(gb))
	}
	mb := bytes / (1024 * 1024)
	if mb == math.Trunc(mb) && mb >= 1 {
		return fmt.Sprintf("%dm", int(mb))
	}
	return strconv.FormatInt(int64(bytes), 10)
}
