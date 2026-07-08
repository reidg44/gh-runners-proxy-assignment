package state

import (
	"sync"
	"time"
)

// RunnerStatus represents the lifecycle state of a runner. Completed runners
// are removed from the store, so there are only two states.
type RunnerStatus string

const (
	StatusIdle RunnerStatus = "idle"
	StatusBusy RunnerStatus = "busy"
)

// RunnerInfo tracks a runner's container, profile, and job assignment.
type RunnerInfo struct {
	RunnerName      string
	ContainerID     string
	ContainerIP     string
	Profile         string
	JobID           string
	JobName         string
	AllocatedCPUs   string
	AllocatedMemory string
	Status          RunnerStatus
	StartedAt       time.Time
}

// Store is a thread-safe store for runner state.
type Store struct {
	mu      sync.RWMutex
	runners map[string]*RunnerInfo // keyed by runner name
	byIP    map[string]string      // container IP -> runner name, for the proxy's per-connection lookups
}

// NewStore creates an empty state store.
func NewStore() *Store {
	return &Store{
		runners: make(map[string]*RunnerInfo),
		byIP:    make(map[string]string),
	}
}

// AddRunner registers a new runner in the store.
func (s *Store) AddRunner(info *RunnerInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.runners[info.RunnerName]; ok && old.ContainerIP != "" {
		delete(s.byIP, old.ContainerIP)
	}
	info.Status = StatusIdle
	s.runners[info.RunnerName] = info
	if info.ContainerIP != "" {
		s.byIP[info.ContainerIP] = info.RunnerName
	}
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
	name, ok := s.byIP[ip]
	if !ok {
		return nil, false
	}
	r, ok := s.runners[name]
	if !ok {
		return nil, false
	}
	cp := *r
	return &cp, true
}

// Remove deletes a runner from the store.
func (s *Store) Remove(runnerName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.runners[runnerName]; ok && r.ContainerIP != "" {
		delete(s.byIP, r.ContainerIP)
	}
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

// ActiveCount returns the number of tracked runners. Runners are removed on
// completion, so every tracked runner is active (idle or busy).
func (s *Store) ActiveCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.runners)
}
