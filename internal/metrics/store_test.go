package metrics

import (
	"testing"
	"time"
)

func TestStoreRecordAndGetHistory(t *testing.T) {
	store, err := NewStore(":memory:")
	if err != nil {
		t.Fatalf("NewStore error: %v", err)
	}
	defer store.Close()

	for i := 0; i < 3; i++ {
		err := store.Record(&MetricsRecord{
			JobName:              "low-cpu-1",
			Profile:              "low-cpu",
			CPUAllocatedNanoCPUs: 1_000_000_000,
			MemAllocatedBytes:    2_147_483_648,
			CPUUsedNanoCPUs:      int64(800_000_000 + i*50_000_000),
			MemPeakBytes:         int64(1_500_000_000 + i*100_000_000),
			DurationSec:          30.0,
		})
		if err != nil {
			t.Fatalf("Record error: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	history, err := store.GetHistory("low-cpu-1", 5)
	if err != nil {
		t.Fatalf("GetHistory error: %v", err)
	}
	if len(history) != 3 {
		t.Fatalf("expected 3 records, got %d", len(history))
	}
	if history[0].CPUUsedNanoCPUs != 900_000_000 {
		t.Errorf("expected most recent CPU = 900000000, got %d", history[0].CPUUsedNanoCPUs)
	}
}

func TestStoreHistoryWindow(t *testing.T) {
	store, err := NewStore(":memory:")
	if err != nil {
		t.Fatalf("NewStore error: %v", err)
	}
	defer store.Close()

	for i := 0; i < 5; i++ {
		err := store.Record(&MetricsRecord{
			JobName:              "low-cpu-1",
			Profile:              "low-cpu",
			CPUAllocatedNanoCPUs: 1_000_000_000,
			MemAllocatedBytes:    2_147_483_648,
			CPUUsedNanoCPUs:      int64(500_000_000 + i*100_000_000),
			MemPeakBytes:         1_000_000_000,
			DurationSec:          30.0,
		})
		if err != nil {
			t.Fatalf("Record error: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	history, err := store.GetHistory("low-cpu-1", 3)
	if err != nil {
		t.Fatalf("GetHistory error: %v", err)
	}
	if len(history) != 3 {
		t.Fatalf("expected 3 records, got %d", len(history))
	}
	if history[0].CPUUsedNanoCPUs != 900_000_000 {
		t.Errorf("expected first CPU = 900000000, got %d", history[0].CPUUsedNanoCPUs)
	}
	if history[2].CPUUsedNanoCPUs != 700_000_000 {
		t.Errorf("expected last CPU = 700000000, got %d", history[2].CPUUsedNanoCPUs)
	}
}

func TestStoreIsolation(t *testing.T) {
	store, err := NewStore(":memory:")
	if err != nil {
		t.Fatalf("NewStore error: %v", err)
	}
	defer store.Close()

	store.Record(&MetricsRecord{
		JobName: "low-cpu-1", Profile: "low-cpu",
		CPUAllocatedNanoCPUs: 1_000_000_000, MemAllocatedBytes: 2_147_483_648,
		CPUUsedNanoCPUs: 800_000_000, MemPeakBytes: 1_500_000_000, DurationSec: 30.0,
	})
	store.Record(&MetricsRecord{
		JobName: "high-cpu", Profile: "high-cpu",
		CPUAllocatedNanoCPUs: 4_000_000_000, MemAllocatedBytes: 8_589_934_592,
		CPUUsedNanoCPUs: 3_500_000_000, MemPeakBytes: 7_000_000_000, DurationSec: 60.0,
	})

	history, err := store.GetHistory("low-cpu-1", 10)
	if err != nil {
		t.Fatalf("GetHistory error: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("expected 1 record for low-cpu-1, got %d", len(history))
	}
	if history[0].Profile != "low-cpu" {
		t.Errorf("expected profile low-cpu, got %s", history[0].Profile)
	}
}
