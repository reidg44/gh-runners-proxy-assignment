package state

import (
	"sync"
	"testing"
)

func TestAddAndGetByName(t *testing.T) {
	s := NewStore()
	s.AddRunner(&RunnerInfo{
		RunnerName:  "runner-1",
		ContainerID: "abc123",
		ContainerIP: "172.18.0.2",
		Profile:     "high-cpu",
		JobID:       "job-1",
		JobName:     "high-cpu",
	})

	r, ok := s.GetByName("runner-1")
	if !ok {
		t.Fatal("runner not found")
	}
	if r.Profile != "high-cpu" {
		t.Errorf("got profile=%q, want high-cpu", r.Profile)
	}
	if r.Status != StatusIdle {
		t.Errorf("got status=%q, want idle", r.Status)
	}
}

func TestGetByContainerIP(t *testing.T) {
	s := NewStore()
	s.AddRunner(&RunnerInfo{
		RunnerName:  "runner-1",
		ContainerIP: "172.18.0.2",
		Profile:     "low-cpu",
	})

	r, ok := s.GetByContainerIP("172.18.0.2")
	if !ok {
		t.Fatal("runner not found by IP")
	}
	if r.RunnerName != "runner-1" {
		t.Errorf("got name=%q, want runner-1", r.RunnerName)
	}
}

func TestGetByContainerIPAfterRemove(t *testing.T) {
	s := NewStore()
	s.AddRunner(&RunnerInfo{RunnerName: "runner-1", ContainerIP: "172.18.0.2"})
	s.Remove("runner-1")

	if _, ok := s.GetByContainerIP("172.18.0.2"); ok {
		t.Error("IP lookup should fail after runner removed")
	}
}

func TestAddRunnerReplacesIPIndex(t *testing.T) {
	s := NewStore()
	s.AddRunner(&RunnerInfo{RunnerName: "runner-1", ContainerIP: "172.18.0.2"})
	// Re-register the same runner name with a new container IP.
	s.AddRunner(&RunnerInfo{RunnerName: "runner-1", ContainerIP: "172.18.0.3"})

	if _, ok := s.GetByContainerIP("172.18.0.2"); ok {
		t.Error("stale IP should no longer resolve")
	}
	if r, ok := s.GetByContainerIP("172.18.0.3"); !ok || r.RunnerName != "runner-1" {
		t.Errorf("new IP should resolve to runner-1, got %v ok=%v", r, ok)
	}
}

func TestMarkBusy(t *testing.T) {
	s := NewStore()
	s.AddRunner(&RunnerInfo{RunnerName: "runner-1"})

	r, _ := s.GetByName("runner-1")
	if r.Status != StatusIdle {
		t.Fatalf("initial status=%q, want idle", r.Status)
	}

	s.MarkBusy("runner-1")
	r, _ = s.GetByName("runner-1")
	if r.Status != StatusBusy {
		t.Fatalf("after MarkBusy status=%q, want busy", r.Status)
	}
	if r.StartedAt.IsZero() {
		t.Error("StartedAt should be set after MarkBusy")
	}
}

func TestRemove(t *testing.T) {
	s := NewStore()
	s.AddRunner(&RunnerInfo{RunnerName: "runner-1"})
	s.Remove("runner-1")

	_, ok := s.GetByName("runner-1")
	if ok {
		t.Error("runner should be removed")
	}
}

func TestActiveCount(t *testing.T) {
	s := NewStore()
	s.AddRunner(&RunnerInfo{RunnerName: "runner-1"})
	s.AddRunner(&RunnerInfo{RunnerName: "runner-2"})
	s.AddRunner(&RunnerInfo{RunnerName: "runner-3"})

	if s.ActiveCount() != 3 {
		t.Errorf("ActiveCount()=%d, want 3", s.ActiveCount())
	}

	s.Remove("runner-3")
	if s.ActiveCount() != 2 {
		t.Errorf("ActiveCount()=%d, want 2 after removal", s.ActiveCount())
	}
}

func TestNotFound(t *testing.T) {
	s := NewStore()
	if _, ok := s.GetByName("nope"); ok {
		t.Error("should not find nonexistent runner")
	}
	if _, ok := s.GetByContainerIP("0.0.0.0"); ok {
		t.Error("should not find by nonexistent IP")
	}
}

func TestMarkNonexistent(t *testing.T) {
	s := NewStore()
	// Should not panic
	s.MarkBusy("nope")
}

func TestConcurrentAccess(t *testing.T) {
	s := NewStore()
	var wg sync.WaitGroup

	for i := range 100 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := "runner-" + string(rune('a'+i%26))
			s.AddRunner(&RunnerInfo{RunnerName: name, ContainerIP: "172.18.0." + string(rune('0'+i%10))})
			s.GetByName(name)
			s.MarkBusy(name)
			s.GetByContainerIP("172.18.0.2")
			s.All()
			s.ActiveCount()
		}(i)
	}
	wg.Wait()
}

func TestAllReturnsSnapshot(t *testing.T) {
	s := NewStore()
	s.AddRunner(&RunnerInfo{RunnerName: "runner-1", Profile: "low-cpu"})
	s.AddRunner(&RunnerInfo{RunnerName: "runner-2", Profile: "high-cpu"})

	all := s.All()
	if len(all) != 2 {
		t.Fatalf("All()=%d, want 2", len(all))
	}

	// Mutating the snapshot should not affect the store
	all[0].Profile = "mutated"
	r, _ := s.GetByName(all[0].RunnerName)
	if r.Profile == "mutated" {
		t.Error("mutating snapshot should not affect store")
	}
}
