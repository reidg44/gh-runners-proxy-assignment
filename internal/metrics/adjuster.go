package metrics

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/reidg44/gh-runners-proxy-assignment/internal/config"
)

// Adjuster computes adjusted CPU and memory resources for a profile based on
// historical utilization data and configurable thresholds.
type Adjuster struct {
	ScaleUpThreshold   float64
	ScaleDownThreshold float64
	ScaleFactor        float64
	HistoryWindow      int
	MaxCPUs            string
	MaxMemory          string
}

// AdjustedResources holds the result of an Adjust call.
type AdjustedResources struct {
	CPUs   string
	Memory string
	Reason string
}

// Adjust computes new resource allocations given a baseline profile and historical
// metrics records. Returns baseline values unchanged when there is insufficient
// history. CPU and memory scale independently based on average utilization vs
// configured thresholds. The baseline is the floor; MaxCPUs/MaxMemory are the
// ceiling (per-profile ceiling takes precedence if lower than the global ceiling).
func (a *Adjuster) Adjust(baseline *config.Profile, history []MetricsRecord) *AdjustedResources {
	if len(history) < a.HistoryWindow {
		return &AdjustedResources{CPUs: baseline.CPUs, Memory: baseline.Memory, Reason: "insufficient history"}
	}

	var cpuUtilSum, memUtilSum float64
	for _, r := range history {
		if r.CPUAllocatedNanoCPUs > 0 {
			cpuUtilSum += float64(r.CPUUsedNanoCPUs) / float64(r.CPUAllocatedNanoCPUs)
		}
		if r.MemAllocatedBytes > 0 {
			memUtilSum += float64(r.MemPeakBytes) / float64(r.MemAllocatedBytes)
		}
	}
	cpuUtil := cpuUtilSum / float64(len(history))
	memUtil := memUtilSum / float64(len(history))

	lastCPU := float64(history[0].CPUAllocatedNanoCPUs)
	lastMem := float64(history[0].MemAllocatedBytes)

	baselineCPUNano := ParseCPUToNano(baseline.CPUs)
	baselineMemBytes := ParseMemToBytes(baseline.Memory)

	if lastCPU == 0 {
		lastCPU = baselineCPUNano
	}
	if lastMem == 0 {
		lastMem = baselineMemBytes
	}

	maxCPUNano := ParseCPUToNano(a.MaxCPUs)
	maxMemBytes := ParseMemToBytes(a.MaxMemory)

	if baseline.MaxCPUs != "" {
		if profileMax := ParseCPUToNano(baseline.MaxCPUs); profileMax < maxCPUNano {
			maxCPUNano = profileMax
		}
	}
	if baseline.MaxMemory != "" {
		if profileMax := ParseMemToBytes(baseline.MaxMemory); profileMax < maxMemBytes {
			maxMemBytes = profileMax
		}
	}

	newCPU, newMem := lastCPU, lastMem
	var reasons []string

	if cpuUtil > a.ScaleUpThreshold {
		newCPU = lastCPU * a.ScaleFactor
		reasons = append(reasons, fmt.Sprintf("CPU scaled up: avg util %.0f%%", cpuUtil*100))
	} else if cpuUtil < a.ScaleDownThreshold {
		newCPU = lastCPU / a.ScaleFactor
		reasons = append(reasons, fmt.Sprintf("CPU scaled down: avg util %.0f%%", cpuUtil*100))
	}

	if memUtil > a.ScaleUpThreshold {
		newMem = lastMem * a.ScaleFactor
		reasons = append(reasons, fmt.Sprintf("memory scaled up: avg util %.0f%%", memUtil*100))
	} else if memUtil < a.ScaleDownThreshold {
		newMem = lastMem / a.ScaleFactor
		reasons = append(reasons, fmt.Sprintf("memory scaled down: avg util %.0f%%", memUtil*100))
	}

	if newCPU < baselineCPUNano {
		newCPU = baselineCPUNano
	}
	if newMem < baselineMemBytes {
		newMem = baselineMemBytes
	}
	if newCPU > maxCPUNano {
		newCPU = maxCPUNano
	}
	if newMem > maxMemBytes {
		newMem = maxMemBytes
	}

	reason := "within thresholds"
	if len(reasons) > 0 {
		reason = strings.Join(reasons, "; ")
	}

	return &AdjustedResources{CPUs: formatCPU(newCPU), Memory: formatMemory(newMem), Reason: reason}
}

// ParseCPUToNano converts a CPU string like "4" or "1.5" to nanocpus. Exported for use by scaler.
func ParseCPUToNano(cpus string) float64 {
	f, err := strconv.ParseFloat(cpus, 64)
	if err != nil {
		return 0
	}
	return f * 1e9
}

// ParseMemToBytes converts a memory string like "8g" or "512m" to bytes. Exported for use by scaler.
func ParseMemToBytes(mem string) float64 {
	mem = strings.TrimSpace(mem)
	if len(mem) == 0 {
		return 0
	}
	suffix := strings.ToLower(mem[len(mem)-1:])
	numStr := mem[:len(mem)-1]
	num, err := strconv.ParseFloat(numStr, 64)
	if err != nil {
		b, err2 := strconv.ParseFloat(mem, 64)
		if err2 != nil {
			return 0
		}
		return b
	}
	switch suffix {
	case "g":
		return num * 1024 * 1024 * 1024
	case "m":
		return num * 1024 * 1024
	case "k":
		return num * 1024
	default:
		b, _ := strconv.ParseFloat(mem, 64)
		return b
	}
}

// formatCPU converts nanocpus back to a human-readable CPU string (e.g., "2", "1.5", "2.25").
func formatCPU(nanoCPUs float64) string {
	cpus := nanoCPUs / 1e9
	if cpus == math.Trunc(cpus) {
		return strconv.Itoa(int(cpus))
	}
	s := strconv.FormatFloat(cpus, 'f', 2, 64)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	return s
}

// formatMemory converts bytes back to a human-readable memory string (e.g., "8g", "512m").
func formatMemory(bytes float64) string {
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
