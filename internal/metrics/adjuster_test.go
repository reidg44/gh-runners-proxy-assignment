package metrics

import (
	"testing"

	"github.com/reidg44/gh-runners-proxy-assignment/internal/config"
)

func TestAdjustNotEnoughHistory(t *testing.T) {
	adj := &Adjuster{ScaleUpThreshold: 0.80, ScaleDownThreshold: 0.30, ScaleFactor: 1.5, HistoryWindow: 5, MaxCPUs: "16", MaxMemory: "32g"}
	baseline := &config.Profile{CPUs: "1", Memory: "2g"}
	history := make([]MetricsRecord, 3)
	for i := range history {
		history[i] = MetricsRecord{CPUAllocatedNanoCPUs: 1_000_000_000, MemAllocatedBytes: 2_147_483_648, CPUUsedNanoCPUs: 900_000_000, MemPeakBytes: 1_800_000_000}
	}
	result := adj.Adjust(baseline, history)
	if result.CPUs != "1" { t.Errorf("expected CPUs = 1, got %s", result.CPUs) }
	if result.Memory != "2g" { t.Errorf("expected Memory = 2g, got %s", result.Memory) }
	if result.Reason != "insufficient history" { t.Errorf("expected reason 'insufficient history', got %q", result.Reason) }
}

func TestAdjustScaleUp(t *testing.T) {
	adj := &Adjuster{ScaleUpThreshold: 0.80, ScaleDownThreshold: 0.30, ScaleFactor: 1.5, HistoryWindow: 3, MaxCPUs: "16", MaxMemory: "32g"}
	baseline := &config.Profile{CPUs: "1", Memory: "2g"}
	history := make([]MetricsRecord, 3)
	for i := range history {
		history[i] = MetricsRecord{CPUAllocatedNanoCPUs: 1_000_000_000, MemAllocatedBytes: 2_147_483_648, CPUUsedNanoCPUs: 900_000_000, MemPeakBytes: 966_367_641}
	}
	result := adj.Adjust(baseline, history)
	if result.CPUs != "1.5" { t.Errorf("expected CPUs = 1.5, got %s", result.CPUs) }
	if result.Memory != "2g" { t.Errorf("expected Memory = 2g, got %s", result.Memory) }
}

func TestAdjustScaleDown(t *testing.T) {
	adj := &Adjuster{ScaleUpThreshold: 0.80, ScaleDownThreshold: 0.30, ScaleFactor: 1.5, HistoryWindow: 3, MaxCPUs: "16", MaxMemory: "32g"}
	baseline := &config.Profile{CPUs: "2", Memory: "4g"}
	history := make([]MetricsRecord, 3)
	for i := range history {
		history[i] = MetricsRecord{CPUAllocatedNanoCPUs: 3_000_000_000, MemAllocatedBytes: 6_442_450_944, CPUUsedNanoCPUs: 600_000_000, MemPeakBytes: 1_288_490_188}
	}
	result := adj.Adjust(baseline, history)
	if result.CPUs != "2" { t.Errorf("expected CPUs = 2, got %s", result.CPUs) }
	if result.Memory != "4g" { t.Errorf("expected Memory = 4g, got %s", result.Memory) }
}

func TestAdjustFloor(t *testing.T) {
	adj := &Adjuster{ScaleUpThreshold: 0.80, ScaleDownThreshold: 0.30, ScaleFactor: 1.5, HistoryWindow: 3, MaxCPUs: "16", MaxMemory: "32g"}
	baseline := &config.Profile{CPUs: "2", Memory: "4g"}
	history := make([]MetricsRecord, 3)
	for i := range history {
		history[i] = MetricsRecord{CPUAllocatedNanoCPUs: 2_000_000_000, MemAllocatedBytes: 4_294_967_296, CPUUsedNanoCPUs: 200_000_000, MemPeakBytes: 429_496_729}
	}
	result := adj.Adjust(baseline, history)
	if result.CPUs != "2" { t.Errorf("expected CPUs = 2 (floor), got %s", result.CPUs) }
	if result.Memory != "4g" { t.Errorf("expected Memory = 4g (floor), got %s", result.Memory) }
}

func TestAdjustCeiling(t *testing.T) {
	adj := &Adjuster{ScaleUpThreshold: 0.80, ScaleDownThreshold: 0.30, ScaleFactor: 2.0, HistoryWindow: 3, MaxCPUs: "8", MaxMemory: "16g"}
	baseline := &config.Profile{CPUs: "4", Memory: "8g"}
	history := make([]MetricsRecord, 3)
	for i := range history {
		history[i] = MetricsRecord{CPUAllocatedNanoCPUs: 6_000_000_000, MemAllocatedBytes: 12_884_901_888, CPUUsedNanoCPUs: 5_400_000_000, MemPeakBytes: 11_596_411_699}
	}
	result := adj.Adjust(baseline, history)
	if result.CPUs != "8" { t.Errorf("expected CPUs = 8 (ceiling), got %s", result.CPUs) }
	if result.Memory != "16g" { t.Errorf("expected Memory = 16g (ceiling), got %s", result.Memory) }
}

func TestAdjustPerProfileCeiling(t *testing.T) {
	adj := &Adjuster{ScaleUpThreshold: 0.80, ScaleDownThreshold: 0.30, ScaleFactor: 2.0, HistoryWindow: 3, MaxCPUs: "16", MaxMemory: "32g"}
	baseline := &config.Profile{CPUs: "4", Memory: "8g", MaxCPUs: "6", MaxMemory: "12g"}
	history := make([]MetricsRecord, 3)
	for i := range history {
		history[i] = MetricsRecord{CPUAllocatedNanoCPUs: 4_000_000_000, MemAllocatedBytes: 8_589_934_592, CPUUsedNanoCPUs: 3_600_000_000, MemPeakBytes: 7_730_941_132}
	}
	result := adj.Adjust(baseline, history)
	if result.CPUs != "6" { t.Errorf("expected CPUs = 6 (per-profile ceiling), got %s", result.CPUs) }
	if result.Memory != "12g" { t.Errorf("expected Memory = 12g (per-profile ceiling), got %s", result.Memory) }
}

func TestAdjustWithinThresholds(t *testing.T) {
	adj := &Adjuster{ScaleUpThreshold: 0.80, ScaleDownThreshold: 0.30, ScaleFactor: 1.5, HistoryWindow: 3, MaxCPUs: "16", MaxMemory: "32g"}
	baseline := &config.Profile{CPUs: "2", Memory: "4g"}
	history := make([]MetricsRecord, 3)
	for i := range history {
		history[i] = MetricsRecord{CPUAllocatedNanoCPUs: 2_000_000_000, MemAllocatedBytes: 4_294_967_296, CPUUsedNanoCPUs: 1_000_000_000, MemPeakBytes: 2_147_483_648}
	}
	result := adj.Adjust(baseline, history)
	if result.CPUs != "2" { t.Errorf("expected CPUs = 2, got %s", result.CPUs) }
	if result.Memory != "4g" { t.Errorf("expected Memory = 4g, got %s", result.Memory) }
	if result.Reason != "within thresholds" { t.Errorf("expected reason 'within thresholds', got %q", result.Reason) }
}

func TestAdjustCompoundScaleUp(t *testing.T) {
	adj := &Adjuster{ScaleUpThreshold: 0.80, ScaleDownThreshold: 0.30, ScaleFactor: 1.5, HistoryWindow: 3, MaxCPUs: "16", MaxMemory: "32g"}
	baseline := &config.Profile{CPUs: "1", Memory: "2g"}
	history := make([]MetricsRecord, 3)
	for i := range history {
		history[i] = MetricsRecord{CPUAllocatedNanoCPUs: 1_500_000_000, MemAllocatedBytes: 2_147_483_648, CPUUsedNanoCPUs: 1_350_000_000, MemPeakBytes: 1_073_741_824}
	}
	result := adj.Adjust(baseline, history)
	if result.CPUs != "2.25" { t.Errorf("expected CPUs = 2.25, got %s", result.CPUs) }
	if result.Memory != "2g" { t.Errorf("expected Memory = 2g, got %s", result.Memory) }
}

func TestAdjustEmptyHistory(t *testing.T) {
	adj := &Adjuster{ScaleUpThreshold: 0.80, ScaleDownThreshold: 0.30, ScaleFactor: 1.5, HistoryWindow: 5, MaxCPUs: "16", MaxMemory: "32g"}
	baseline := &config.Profile{CPUs: "1", Memory: "2g"}
	result := adj.Adjust(baseline, nil)
	if result.CPUs != "1" { t.Errorf("expected CPUs = 1, got %s", result.CPUs) }
	if result.Memory != "2g" { t.Errorf("expected Memory = 2g, got %s", result.Memory) }
	if result.Reason != "insufficient history" { t.Errorf("expected reason 'insufficient history', got %q", result.Reason) }
}
