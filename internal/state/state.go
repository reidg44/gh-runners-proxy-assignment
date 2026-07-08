package state

import (
	"sync"
	"time"
)

// RunnerStatus represents the lifecycle state of a runner.
type RunnerStatus string

const (
	StatusIdle      RunnerStatus = "idle"
	StatusBusy      RunnerStatus = "busy"
	StatusCompleted RunnerStatus = "completed"
)

// RunnerInfo tracks a runner's container, profile, and job assignment.
type RunnerInfo struct {
	RunnerName  string
	ContainerID string
	ContainerIP string
	Profile     string
	JobID           string
	JobName         string
	AllocatedCPUs   string
	AllocatedMemory string
	Status          RunnerStatus
	CreatedAt   time.Time
	StartedAt   time.Time
	CompletedAt time.Time
}

// Store is a thread-safe store for runner state.
type Store struct {
	mu      sync.RWMutex
	runners map[string]*RunnerInfo // keyed by runner name
}

// NewStore creates an empty state store.
func NewStore() *Store {
	return &Store{
		runners: make(map[string]*RunnerInfo),
	}
}

// AddRunner registers a new runner in the store.
func (s *Store) AddRunner(info *RunnerInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	info.CreatedAt = time.Now()
	info.Status = StatusIdle
	s.runners[info.RunnerName] = info
}

// MarkBusy transitions a runner to the busy state.
func (s *Store) MarkBusy(runnerName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.runners[runnerName]; ok {
		r.Status = StatusBusy
		r.StartedAt = time.Now()
	}
}

// MarkCompleted transitions a runner to the completed state.
func (s *Store) MarkCompleted(runnerName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.runners[runnerName]; ok {
		r.Status = StatusCompleted
		r.CompletedAt = time.Now()
	}
}

// GetByName returns a copy of the runner info for the given name.
func (s *Store) GetByName(runnerName string) (*RunnerInfo, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.runners[runnerName]
	if !ok {
		return nil, false
	}
	cp := *r
	return &cp, true
}

// GetByContainerIP returns a copy of the runner info for the given container IP.
func (s *Store) GetByContainerIP(ip string) (*RunnerInfo, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.runners {
		if r.ContainerIP == ip {
			cp := *r
			return &cp, true
		}
	}
	return nil, false
}

// GetByJobID returns a copy of the runner info for the given job ID.
func (s *Store) GetByJobID(jobID string) (*RunnerInfo, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.runners {
		if r.JobID == jobID {
			cp := *r
			return &cp, true
		}
	}
	return nil, false
}

// Remove deletes a runner from the store.
func (s *Store) Remove(runnerName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.runners, runnerName)
}

// All returns a snapshot of all runners.
func (s *Store) All() []*RunnerInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*RunnerInfo, 0, len(s.runners))
	for _, r := range s.runners {
		cp := *r
		result = append(result, &cp)
	}
	return result
}

// Count returns the total number of tracked runners.
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.runners)
}

// ActiveCount returns the number of runners that are idle or busy.
func (s *Store) ActiveCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	count := 0
	for _, r := range s.runners {
		if r.Status == StatusIdle || r.Status == StatusBusy {
			count++
		}
	}
	return count
}
