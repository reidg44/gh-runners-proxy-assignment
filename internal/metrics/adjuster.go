package metrics

import (
	"fmt"
	"strings"

	"github.com/reidg44/gh-runners-proxy-assignment/internal/config"
	"github.com/reidg44/gh-runners-proxy-assignment/internal/units"
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

// NewAdjuster builds an Adjuster from the adaptive section of the config.
func NewAdjuster(cfg config.AdaptiveConfig) *Adjuster {
	return &Adjuster{
		ScaleUpThreshold:   cfg.ScaleUpThreshold,
		ScaleDownThreshold: cfg.ScaleDownThreshold,
		ScaleFactor:        cfg.ScaleFactor,
		HistoryWindow:      cfg.HistoryWindow,
		MaxCPUs:            cfg.MaxCPUs,
		MaxMemory:          cfg.MaxMemory,
	}
}

// AdjustedResources holds the result of an Adjust call.
type AdjustedResources struct {
	CPUs   string
	Memory string
	Reason string
}

// cpuNano and memBytes parse resource strings tolerantly: config values are
// validated elsewhere, so a parse failure here (e.g. an unset ceiling) is
// treated as 0 rather than an error.
func cpuNano(s string) float64 {
	v, err := units.ParseCPU(s)
	if err != nil {
		return 0
	}
	return float64(v)
}

func memBytes(s string) float64 {
	v, err := units.ParseMemory(s)
	if err != nil {
		return 0
	}
	return float64(v)
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

	baselineCPUNano := cpuNano(baseline.CPUs)
	baselineMemBytes := memBytes(baseline.Memory)

	if lastCPU == 0 {
		lastCPU = baselineCPUNano
	}
	if lastMem == 0 {
		lastMem = baselineMemBytes
	}

	maxCPUNano := cpuNano(a.MaxCPUs)
	maxMemBytes := memBytes(a.MaxMemory)

	if baseline.MaxCPUs != "" {
		if profileMax := cpuNano(baseline.MaxCPUs); profileMax < maxCPUNano {
			maxCPUNano = profileMax
		}
	}
	if baseline.MaxMemory != "" {
		if profileMax := memBytes(baseline.MaxMemory); profileMax < maxMemBytes {
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

	return &AdjustedResources{CPUs: units.FormatCPU(newCPU), Memory: units.FormatMemory(newMem), Reason: reason}
}
