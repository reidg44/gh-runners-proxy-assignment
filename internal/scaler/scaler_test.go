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

// mockSessionClient simulates the scaleset message session.
type mockSessionClient struct {
	messages []*scaleset.RunnerScaleSetMessage
	index    int
	deleted  []int
}

func (m *mockSessionClient) GetMessage(ctx context.Context, lastMessageID int, maxCapacity int) (*scaleset.RunnerScaleSetMessage, error) {
	if m.index >= len(m.messages) {
		return nil, ctx.Err() // return nil when no more messages
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

func TestHandleJobAssignment(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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
	cfg := testConfig()
	cls := classifier.New(cfg.OrderedProfiles, cfg.DefaultProfile)
	logger := slog.Default()

	s := New(session, jitGen, prov, cls, store, cfg, 42, "http://proxy:8080", logger)

	// Run scaler - it will process one message then get nil (ctx cancelled)
	go func() {
		// Wait for the message to be processed
		for session.index < len(session.messages) {
			// spin
		}
		cancel()
	}()
	_ = s.Run(ctx)

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
	if store.Count() != 2 {
		t.Errorf("store count=%d, want 2", store.Count())
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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
	cfg := testConfig()
	cls := classifier.New(cfg.OrderedProfiles, cfg.DefaultProfile)
	logger := slog.Default()

	s := New(session, &mockJITGenerator{}, prov, cls, store, cfg, 42, "http://proxy:8080", logger)

	go func() {
		for session.index < len(session.messages) {
		}
		cancel()
	}()
	_ = s.Run(ctx)

	// Verify container was stopped
	if len(prov.stopped) != 1 {
		t.Fatalf("expected 1 container stopped, got %d", len(prov.stopped))
	}
	if prov.stopped[0] != "container-abc" {
		t.Errorf("stopped container=%q, want container-abc", prov.stopped[0])
	}

	// Verify runner removed from store
	if store.Count() != 0 {
		t.Errorf("store count=%d, want 0", store.Count())
	}
}

func TestReconcileRunnerCount(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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
	cfg := testConfig()
	cls := classifier.New(cfg.OrderedProfiles, cfg.DefaultProfile)
	logger := slog.Default()

	s := New(session, jitGen, prov, cls, store, cfg, 42, "http://proxy:8080", logger)

	go func() {
		for session.index < len(session.messages) {
		}
		cancel()
	}()
	_ = s.Run(ctx)

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
}

func TestClassificationRouting(t *testing.T) {
	cfg := testConfig()
	cls := classifier.New(cfg.OrderedProfiles, cfg.DefaultProfile)

	tests := []struct {
		jobName string
		profile string
	}{
		{"high-cpu", "high-cpu"},
		{"low-cpu-1", "low-cpu"},
		{"low-cpu-7", "low-cpu"},
		{"unknown", "low-cpu"}, // default
	}

	for _, tt := range tests {
		got := cls.Classify(tt.jobName)
		if got != tt.profile {
			t.Errorf("Classify(%q) = %q, want %q", tt.jobName, got, tt.profile)
		}
	}
}
