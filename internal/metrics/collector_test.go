package metrics

import (
	"testing"
	"time"
)

func TestParseCgroupV2CPU(t *testing.T) {
	content := "usage_usec 15000000\nuser_usec 10000000\nsystem_usec 5000000\nnr_periods 0\nnr_throttled 0\nthrottled_usec 0"
	usageUsec, err := parseCPUStatUsageUsec(content)
	if err != nil { t.Fatalf("parseCPUStatUsageUsec error: %v", err) }
	if usageUsec != 15_000_000 { t.Errorf("expected 15000000, got %d", usageUsec) }
	duration := 30 * time.Second
	nanoCPUs := usageUsecToNanoCPUs(usageUsec, duration)
	if nanoCPUs != 500_000_000 { t.Errorf("expected 500000000, got %d", nanoCPUs) }
}

func TestParseCgroupV2Memory(t *testing.T) {
	content := "1073741824\n"
	bytes, err := parseMemoryPeak(content)
	if err != nil { t.Fatalf("parseMemoryPeak error: %v", err) }
	if bytes != 1_073_741_824 { t.Errorf("expected 1073741824, got %d", bytes) }
}

func TestParseCgroupV1CPU(t *testing.T) {
	content := "15000000000\n"
	nanos, err := parseCPUAcctUsage(content)
	if err != nil { t.Fatalf("parseCPUAcctUsage error: %v", err) }
	duration := 30 * time.Second
	nanoCPUs := cpuAcctNanosToNanoCPUs(nanos, duration)
	if nanoCPUs != 500_000_000 { t.Errorf("expected 500000000, got %d", nanoCPUs) }
}

func TestParseCgroupV1Memory(t *testing.T) {
	content := "2147483648\n"
	bytes, err := parseMemoryPeak(content)
	if err != nil { t.Fatalf("parseMemoryPeak error: %v", err) }
	if bytes != 2_147_483_648 { t.Errorf("expected 2147483648, got %d", bytes) }
}

func TestParseCPUStatMissingUsageLine(t *testing.T) {
	content := "user_usec 10000000\nsystem_usec 5000000"
	_, err := parseCPUStatUsageUsec(content)
	if err == nil { t.Error("expected error for missing usage_usec line") }
}
