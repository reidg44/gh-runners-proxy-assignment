package scaler

import (
	"context"
	"fmt"
	"log/slog"
	"testing"

	"github.com/actions/scaleset"
	"github.com/reidg44/gh-runners-proxy-assignment/internal/classifier"
	"github.com/reidg44/gh-runners-proxy-assignment/internal/config"
	"github.com/reidg44/gh-runners-proxy-assignment/internal/state"
)

// mockSessionClient simulates the scaleset message session. When its messages
// are drained it cancels the run context, ending the scaler loop.
type mockSessionClient struct {
	messages []*scaleset.RunnerScaleSetMessage
	index    int
	deleted  []int
	cancel   context.CancelFunc
}

func (m *mockSessionClient) GetMessage(ctx context.Context, lastMessageID int, maxCapacity int) (*scaleset.RunnerScaleSetMessage, error) {
	if m.index >= len(m.messages) {
		if m.cancel != nil {
			m.cancel()
		}
		return nil, ctx.Err()
	}
	msg := m.messages[m.index]
	m.index++
	return msg, nil
}

func (m *mockSessionClient) DeleteMessage(ctx context.Context, messageID int) error {
	m.deleted = append(m.deleted, messageID)
	return nil
}

// mockJITGenerator returns fake JIT configs.
type mockJITGenerator struct {
	calls []string // runner names requested
}

func (m *mockJITGenerator) GenerateJitRunnerConfig(ctx context.Context, setting *scaleset.RunnerScaleSetJitRunnerSetting, scaleSetID int) (*scaleset.RunnerScaleSetJitRunnerConfig, error) {
	m.calls = append(m.calls, setting.Name)
	return &scaleset.RunnerScaleSetJitRunnerConfig{
		Runner: &scaleset.RunnerReference{
			ID:   1,
			Name: setting.Name,
		},
		EncodedJITConfig: "fake-jit-config-" + setting.Name,
	}, nil
}

// mockProvisioner tracks container lifecycle.
type mockProvisioner struct {
	started []startCall
	stopped []string
}

type startCall struct {
	name     string
	profile  string
	jitCfg   string
	proxyURL string
}

func (m *mockProvisioner) StartRunner(ctx context.Context, name string, profile *config.Profile, jitConfig string, proxyURL string) (string, string, error) {
	m.started = append(m.started, startCall{
		name:     name,
		profile:  profile.CPUs,
		jitCfg:   jitConfig,
		proxyURL: proxyURL,
	})
	return fmt.Sprintf("container-%s", name), fmt.Sprintf("172.18.0.%d", len(m.started)+1), nil
}

func (m *mockProvisioner) StopRunner(ctx context.Context, containerID string) error {
	m.stopped = append(m.stopped, containerID)
	return nil
}

func testConfig() *config.Config {
	cfg := &config.Config{
		Runner: config.RunnerConfig{
			Image:      "test:latest",
			MaxRunners: 10,
			WorkFolder: "_work",
		},
		Profiles: map[string]*config.Profile{
			"high-cpu": {CPUs: "4", Memory: "8g", MatchPatterns: []string{"high-cpu*"}},
			"low-cpu":  {CPUs: "1", Memory: "2g", MatchPatterns: []string{"low-cpu*"}},
		},
		DefaultProfile: "low-cpu",
		Proxy:          config.ProxyConfig{ListenAddr: ":8080"},
	}
	cfg.OrderedProfiles = []config.NamedProfile{
		{Name: "high-cpu", Profile: cfg.Profiles["high-cpu"]},
		{Name: "low-cpu", Profile: cfg.Profiles["low-cpu"]},
	}
	return cfg
}

func zeroStats() *scaleset.RunnerScaleSetStatistic {
	return &scaleset.RunnerScaleSetStatistic{}
}

// runScaler builds a Scaler around the mocks and runs it until the session's
// messages are drained.
func runScaler(t *testing.T, session *mockSessionClient, jitGen *mockJITGenerator, prov *mockProvisioner, store *state.Store) {
	t.Helper()
	cfg := testConfig()
	s := New(Options{
		SessionClient: session,
		JITGenerator:  jitGen,
		Provisioner:   prov,
		Classifier:    classifier.New(cfg.OrderedProfiles, cfg.DefaultProfile),
		Store:         store,
		Config:        cfg,
		ScaleSetID:    42,
		ProxyURL:      "http://proxy:8080",
		Logger:        slog.Default(),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session.cancel = cancel
	_ = s.Run(ctx)
}

func TestHandleJobAssignment(t *testing.T) {
	session := &mockSessionClient{
		messages: []*scaleset.RunnerScaleSetMessage{
			{
				MessageID:  1,
				Statistics: zeroStats(),
				JobAssignedMessages: []*scaleset.JobAssigned{
					{JobMessageBase: scaleset.JobMessageBase{JobDisplayName: "high-cpu", JobID: "job-1"}},
					{JobMessageBase: scaleset.JobMessageBase{JobDisplayName: "low-cpu-1", JobID: "job-2"}},
				},
			},
		},
	}
	jitGen := &mockJITGenerator{}
	prov := &mockProvisioner{}
	store := state.NewStore()

	runScaler(t, session, jitGen, prov, store)

	// Verify 2 runners were provisioned
	if len(prov.started) != 2 {
		t.Fatalf("expected 2 runners started, got %d", len(prov.started))
	}

	// Verify high-cpu job got high-cpu profile (4 CPUs)
	if prov.started[0].profile != "4" {
		t.Errorf("first runner CPUs=%s, want 4", prov.started[0].profile)
	}
	// Verify low-cpu job got low-cpu profile (1 CPU)
	if prov.started[1].profile != "1" {
		t.Errorf("second runner CPUs=%s, want 1", prov.started[1].profile)
	}

	// Verify runners are in the store
	if store.ActiveCount() != 2 {
		t.Errorf("store count=%d, want 2", store.ActiveCount())
	}

	// Verify JIT configs were generated
	if len(jitGen.calls) != 2 {
		t.Errorf("JIT configs generated=%d, want 2", len(jitGen.calls))
	}

	// Verify message was acknowledged
	if len(session.deleted) != 1 || session.deleted[0] != 1 {
		t.Errorf("message ack: got %v, want [1]", session.deleted)
	}
}

func TestHandleJobCompleted(t *testing.T) {
	store := state.NewStore()
	store.AddRunner(&state.RunnerInfo{
		RunnerName:  "runner-low-cpu-job-2",
		ContainerID: "container-abc",
		Profile:     "low-cpu",
		JobID:       "job-2",
		JobName:     "low-cpu-1",
	})

	session := &mockSessionClient{
		messages: []*scaleset.RunnerScaleSetMessage{
			{
				MessageID:  2,
				Statistics: zeroStats(),
				JobCompletedMessages: []*scaleset.JobCompleted{
					{
						Result:     "success",
						RunnerName: "runner-low-cpu-job-2",
						JobMessageBase: scaleset.JobMessageBase{
							JobDisplayName: "low-cpu-1",
							JobID:          "job-2",
						},
					},
				},
			},
		},
	}
	prov := &mockProvisioner{}

	runScaler(t, session, &mockJITGenerator{}, prov, store)

	// Verify container was stopped
	if len(prov.stopped) != 1 {
		t.Fatalf("expected 1 container stopped, got %d", len(prov.stopped))
	}
	if prov.stopped[0] != "container-abc" {
		t.Errorf("stopped container=%q, want container-abc", prov.stopped[0])
	}

	// Verify runner removed from store
	if store.ActiveCount() != 0 {
		t.Errorf("store count=%d, want 0", store.ActiveCount())
	}
}

// blockingProvisioner parks StopRunner until released, so tests can observe
// store state mid-stop.
type blockingProvisioner struct {
	mockProvisioner
	entered chan struct{}
	release chan struct{}
}

func (b *blockingProvisioner) StopRunner(ctx context.Context, containerID string) error {
	close(b.entered)
	<-b.release
	return b.mockProvisioner.StopRunner(ctx, containerID)
}

// TestCapacityHeldUntilContainerStopped pins the cleanup ordering: the
// runner's capacity slot must NOT be freed until the container stop
// completes. Freeing it earlier deadlocks under load — JIT runners are
// pre-registered at config-generation time, so advertising capacity while
// dying runners are still registered makes GitHub assign jobs to
// registrations that are about to vanish (observed live: orphaned
// assignments and a 37-minute stall).
func TestCapacityHeldUntilContainerStopped(t *testing.T) {
	store := state.NewStore()
	store.AddRunner(&state.RunnerInfo{
		RunnerName:  "runner-low-cpu-job-2",
		ContainerID: "container-abc",
		Profile:     "low-cpu",
		JobID:       "job-2",
		JobName:     "low-cpu-1",
	})

	session := &mockSessionClient{
		messages: []*scaleset.RunnerScaleSetMessage{
			{
				MessageID:  1,
				Statistics: zeroStats(),
				JobCompletedMessages: []*scaleset.JobCompleted{
					{
						Result:     "success",
						RunnerName: "runner-low-cpu-job-2",
						JobMessageBase: scaleset.JobMessageBase{
							JobDisplayName: "low-cpu-1",
							JobID:          "job-2",
						},
					},
				},
			},
		},
	}
	prov := &blockingProvisioner{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}

	cfg := testConfig()
	s := New(Options{
		SessionClient: session,
		JITGenerator:  &mockJITGenerator{},
		Provisioner:   prov,
		Classifier:    classifier.New(cfg.OrderedProfiles, cfg.DefaultProfile),
		Store:         store,
		Config:        cfg,
		ScaleSetID:    42,
		ProxyURL:      "http://proxy:8080",
		Logger:        slog.Default(),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session.cancel = cancel
	done := make(chan struct{})
	go func() {
		_ = s.Run(ctx)
		close(done)
	}()

	<-prov.entered
	if got := store.ActiveCount(); got != 1 {
		t.Errorf("capacity slot released during container stop: ActiveCount=%d, want 1", got)
	}
	close(prov.release)
	<-done

	if len(prov.stopped) != 1 || prov.stopped[0] != "container-abc" {
		t.Errorf("stopped containers=%v, want [container-abc]", prov.stopped)
	}
	if got := store.ActiveCount(); got != 0 {
		t.Errorf("capacity slot not released after container stop: ActiveCount=%d, want 0", got)
	}
}

func TestReconcileRunnerCount(t *testing.T) {
	// Simulate scenario: Statistics says 2 assigned jobs but we have 0 active runners.
	// This happens when provisioned runners ran different jobs than intended.
	session := &mockSessionClient{
		messages: []*scaleset.RunnerScaleSetMessage{
			{
				MessageID: 1,
				Statistics: &scaleset.RunnerScaleSetStatistic{
					TotalAssignedJobs: 2,
				},
				// No JobAssigned messages — GitHub already assigned them previously
				// but our runners ran different jobs
				JobCompletedMessages: []*scaleset.JobCompleted{
					{
						Result:     "success",
						RunnerName: "", // empty runner name (canceled/orphaned)
						JobMessageBase: scaleset.JobMessageBase{
							JobDisplayName: "high-cpu",
							JobID:          "orphan-1",
						},
					},
				},
			},
		},
	}
	jitGen := &mockJITGenerator{}
	prov := &mockProvisioner{}
	store := state.NewStore()

	runScaler(t, session, jitGen, prov, store)

	// reconcileRunnerCount should have provisioned 2 runners (deficit = 2 assigned, 0 active)
	if len(prov.started) != 2 {
		t.Fatalf("expected 2 reconciliation runners started, got %d", len(prov.started))
	}

	// Both should use default profile since we don't have pending job info
	for i, sc := range prov.started {
		if sc.profile != "1" { // default profile is low-cpu with 1 CPU
			t.Errorf("reconciliation runner %d CPUs=%s, want 1 (default)", i, sc.profile)
		}
	}

	// Synthetic runners must get unique names so a later reconcile pass
	// can't collide on Docker container names.
	if prov.started[0].name == prov.started[1].name {
		t.Errorf("synthetic runner names collide: %q", prov.started[0].name)
	}
}
