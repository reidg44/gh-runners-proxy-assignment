package scaler

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/actions/scaleset"
	"github.com/reidg44/gh-runners-proxy-assignment/internal/classifier"
	"github.com/reidg44/gh-runners-proxy-assignment/internal/config"
	"github.com/reidg44/gh-runners-proxy-assignment/internal/dockerutil"
	"github.com/reidg44/gh-runners-proxy-assignment/internal/metrics"
	"github.com/reidg44/gh-runners-proxy-assignment/internal/state"
	"github.com/reidg44/gh-runners-proxy-assignment/internal/units"
)

// RunnerProvisioner creates and destroys runner containers.
type RunnerProvisioner interface {
	StartRunner(ctx context.Context, name string, profile *config.Profile, jitConfig string, proxyURL string) (containerID string, containerIP string, err error)
	StopRunner(ctx context.Context, containerID string) error
}

// SessionClient abstracts the scaleset message session for testing.
type SessionClient interface {
	GetMessage(ctx context.Context, lastMessageID int, maxCapacity int) (*scaleset.RunnerScaleSetMessage, error)
	DeleteMessage(ctx context.Context, messageID int) error
}

// JITConfigGenerator generates JIT runner configurations.
type JITConfigGenerator interface {
	GenerateJitRunnerConfig(ctx context.Context, setting *scaleset.RunnerScaleSetJitRunnerSetting, scaleSetID int) (*scaleset.RunnerScaleSetJitRunnerConfig, error)
}

// pendingJob tracks a job that was assigned but hasn't been started yet.
type pendingJob struct {
	jobID       string
	displayName string
	profile     string
}

const (
	getMessageBaseBackoff = time.Second
	getMessageMaxBackoff  = 30 * time.Second
)

// Options holds the dependencies for a Scaler.
type Options struct {
	SessionClient    SessionClient
	JITGenerator     JITConfigGenerator
	Provisioner      RunnerProvisioner
	Classifier       *classifier.Classifier
	Store            *state.Store
	Config           *config.Config
	ScaleSetID       int
	ProxyURL         string
	MetricsCollector metrics.Collector // optional; nil disables collection
	MetricsStore     *metrics.Store    // optional; nil disables history
	Adjuster         *metrics.Adjuster // optional; nil disables adjustment
	Logger           *slog.Logger
}

// Scaler implements a custom message loop that inspects per-job details
// to provision runners with appropriate resource profiles.
type Scaler struct {
	sessionClient    SessionClient
	jitGenerator     JITConfigGenerator
	provisioner      RunnerProvisioner
	classifier       *classifier.Classifier
	store            *state.Store
	cfg              *config.Config
	scaleSetID       int
	proxyURL         string
	metricsCollector metrics.Collector
	metricsStore     *metrics.Store
	adjuster         *metrics.Adjuster
	logger           *slog.Logger

	// completions tracks in-flight job-completion cleanup goroutines so Run
	// doesn't return while containers are still being collected and stopped.
	completions sync.WaitGroup

	// mu protects pendingJobs and reconcileSeq.
	mu          sync.Mutex
	pendingJobs map[string]*pendingJob // jobID -> pendingJob
	// reconcileSeq gives synthetic reconcile runners unique names across
	// reconcile passes.
	reconcileSeq int
}

// New creates a new Scaler.
func New(opts Options) *Scaler {
	return &Scaler{
		sessionClient:    opts.SessionClient,
		jitGenerator:     opts.JITGenerator,
		provisioner:      opts.Provisioner,
		classifier:       opts.Classifier,
		store:            opts.Store,
		cfg:              opts.Config,
		scaleSetID:       opts.ScaleSetID,
		proxyURL:         opts.ProxyURL,
		metricsCollector: opts.MetricsCollector,
		metricsStore:     opts.MetricsStore,
		adjuster:         opts.Adjuster,
		logger:           opts.Logger,
		pendingJobs:      make(map[string]*pendingJob),
	}
}

// Run starts the message processing loop. It blocks until the context is cancelled.
func (s *Scaler) Run(ctx context.Context) error {
	defer s.completions.Wait()

	lastMessageID := 0
	maxCapacity := s.cfg.Runner.MaxRunners
	backoff := getMessageBaseBackoff

	s.logger.Info("scaler started", "max_capacity", maxCapacity)

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		activeRunners := s.store.ActiveCount()
		availableCapacity := max(maxCapacity-activeRunners, 0)

		s.logger.Debug("polling for messages",
			"last_message_id", lastMessageID,
			"available_capacity", availableCapacity,
			"active_runners", activeRunners,
			"pending_jobs", len(s.pendingJobs),
		)

		msg, err := s.sessionClient.GetMessage(ctx, lastMessageID, availableCapacity)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			s.logger.Error("failed to get message", "error", err, "retry_in", backoff)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			if backoff *= 2; backoff > getMessageMaxBackoff {
				backoff = getMessageMaxBackoff
			}
			continue
		}
		backoff = getMessageBaseBackoff

		// No messages (202 response from long poll)
		if msg == nil {
			continue
		}

		s.logger.Info("received message",
			"message_id", msg.MessageID,
			"assigned", len(msg.JobAssignedMessages),
			"started", len(msg.JobStartedMessages),
			"completed", len(msg.JobCompletedMessages),
		)

		// Log statistics for debugging
		if msg.Statistics != nil {
			s.logger.Info("statistics",
				"total_available_jobs", msg.Statistics.TotalAvailableJobs,
				"total_acquired_jobs", msg.Statistics.TotalAcquiredJobs,
				"total_assigned_jobs", msg.Statistics.TotalAssignedJobs,
				"total_running_jobs", msg.Statistics.TotalRunningJobs,
				"total_registered_runners", msg.Statistics.TotalRegisteredRunners,
				"total_busy_runners", msg.Statistics.TotalBusyRunners,
				"total_idle_runners", msg.Statistics.TotalIdleRunners,
			)
		}

		// Acknowledge the message first (like the official listener does).
		// This prevents re-delivery of messages we're about to process.
		if err := s.sessionClient.DeleteMessage(ctx, msg.MessageID); err != nil {
			s.logger.Error("failed to ack message", "message_id", msg.MessageID, "error", err)
		}
		lastMessageID = msg.MessageID

		// Process job assignments - provision runners with appropriate resources
		for _, job := range msg.JobAssignedMessages {
			if err := s.handleJobAssigned(ctx, job); err != nil {
				s.logger.Error("failed to handle job assignment",
					"job_display_name", job.JobDisplayName,
					"job_id", job.JobID,
					"error", err,
				)
			}
		}

		// Process job started - update state
		for _, job := range msg.JobStartedMessages {
			s.handleJobStarted(job)
		}

		// Process job completed - cleanup
		for _, job := range msg.JobCompletedMessages {
			s.handleJobCompleted(ctx, job)
		}

		// CRITICAL: Check Statistics to detect orphaned jobs.
		// If Statistics says there are more assigned jobs than we have active runners,
		// provision additional runners to fill the gap.
		if msg.Statistics != nil {
			s.reconcileRunnerCount(ctx, msg.Statistics)
		}
	}
}

func (s *Scaler) handleJobAssigned(ctx context.Context, job *scaleset.JobAssigned) error {
	profileName := s.classifier.Classify(job.JobDisplayName)
	profile, ok := s.cfg.Profiles[profileName]
	if !ok {
		return fmt.Errorf("profile %q not found", profileName)
	}

	s.logger.Info("job assigned",
		"job_display_name", job.JobDisplayName,
		"job_id", job.JobID,
		"profile", profileName,
	)

	// Track as pending
	s.mu.Lock()
	s.pendingJobs[job.JobID] = &pendingJob{
		jobID:       job.JobID,
		displayName: job.JobDisplayName,
		profile:     profileName,
	}
	s.mu.Unlock()

	return s.provisionRunner(ctx, job.JobDisplayName, job.JobID, profileName, profile)
}

// provisionRunner creates a JIT config and starts a runner container.
// jobDisplayName may be empty for synthetic reconcile runners that have no
// job identity; those always use the baseline profile and never record
// metrics history.
func (s *Scaler) provisionRunner(ctx context.Context, jobDisplayName, jobID, profileName string, profile *config.Profile) error {
	// Generate JIT runner config
	runnerName := fmt.Sprintf("runner-%s-%s", profileName, jobID)
	jitCfg, err := s.jitGenerator.GenerateJitRunnerConfig(ctx, &scaleset.RunnerScaleSetJitRunnerSetting{
		Name:       runnerName,
		WorkFolder: s.cfg.Runner.WorkFolder,
	}, s.scaleSetID)
	if err != nil {
		return fmt.Errorf("generating JIT config: %w", err)
	}

	// Determine effective profile — apply adaptive adjustment if available.
	effectiveProfile := profile
	if s.adjuster != nil && s.metricsStore != nil && jobDisplayName != "" {
		history, err := s.metricsStore.GetHistory(jobDisplayName, s.adjuster.HistoryWindow)
		if err != nil {
			s.logger.Warn("failed to get metrics history, using baseline", "job_display_name", jobDisplayName, "error", err)
		} else {
			adjusted := s.adjuster.Adjust(profile, history)
			s.logger.Info("adaptive adjustment",
				"job_display_name", jobDisplayName,
				"baseline_cpus", profile.CPUs, "baseline_memory", profile.Memory,
				"adjusted_cpus", adjusted.CPUs, "adjusted_memory", adjusted.Memory,
				"reason", adjusted.Reason,
			)
			effectiveProfile = &config.Profile{CPUs: adjusted.CPUs, Memory: adjusted.Memory}
		}
	}

	// Start the runner container with the effective (possibly adjusted) profile.
	containerID, containerIP, err := s.provisioner.StartRunner(ctx, runnerName, effectiveProfile, jitCfg.EncodedJITConfig, s.proxyURL)
	if err != nil {
		return fmt.Errorf("starting runner container: %w", err)
	}

	s.store.AddRunner(&state.RunnerInfo{
		RunnerName:      runnerName,
		ContainerID:     containerID,
		ContainerIP:     containerIP,
		Profile:         profileName,
		JobID:           jobID,
		JobName:         jobDisplayName,
		AllocatedCPUs:   effectiveProfile.CPUs,
		AllocatedMemory: effectiveProfile.Memory,
	})

	s.logger.Info("runner provisioned",
		"runner_name", runnerName,
		"container_id", dockerutil.ShortID(containerID),
		"profile", profileName,
		"job_display_name", jobDisplayName,
	)

	return nil
}

func (s *Scaler) handleJobStarted(job *scaleset.JobStarted) {
	s.logger.Info("job started",
		"runner_name", job.RunnerName,
		"job_display_name", job.JobDisplayName,
		"job_id", job.JobID,
	)
	s.store.MarkBusy(job.RunnerName)

	// Remove from pending - this job now has a runner
	s.mu.Lock()
	delete(s.pendingJobs, job.JobID)
	s.mu.Unlock()
}

func (s *Scaler) handleJobCompleted(ctx context.Context, job *scaleset.JobCompleted) {
	s.logger.Info("job completed",
		"runner_name", job.RunnerName,
		"job_display_name", job.JobDisplayName,
		"result", job.Result,
	)

	// Remove from pending in case it was canceled before starting
	s.mu.Lock()
	delete(s.pendingJobs, job.JobID)
	s.mu.Unlock()

	if job.RunnerName == "" {
		s.logger.Warn("completed job with empty runner name",
			"job_display_name", job.JobDisplayName,
			"result", job.Result,
		)
		return
	}

	runner, ok := s.store.GetByName(job.RunnerName)
	if !ok {
		s.logger.Warn("completed job for unknown runner", "runner_name", job.RunnerName)
		return
	}

	// Collect metrics and stop the container off the message loop: metrics
	// collection is several docker execs and ContainerStop can wait out the
	// full SIGTERM timeout, which would otherwise block processing of new
	// job assignments. WithoutCancel so cleanup finishes during shutdown;
	// Run waits for these goroutines before returning.
	cleanupCtx := context.WithoutCancel(ctx)
	s.completions.Go(func() {
		s.collectAndRecord(cleanupCtx, runner)

		if err := s.provisioner.StopRunner(cleanupCtx, runner.ContainerID); err != nil {
			s.logger.Error("failed to stop runner container",
				"runner_name", runner.RunnerName,
				"container_id", runner.ContainerID,
				"error", err,
			)
		}

		// The capacity slot is released only after the container is gone.
		// Releasing it earlier (before StopRunner) deadlocks under load:
		// JIT runners are pre-registered at config-generation time, so
		// advertising free capacity while dying runners are still
		// registered makes GitHub assign jobs to registrations that are
		// about to vanish — those assignments orphan, replacement runners
		// idle out and exit, and no further messages arrive (observed
		// live: 5 orphaned assignments, 37-minute stall). Stop latency is
		// kept low by the SIGTERM trap in the runner command instead.
		s.store.Remove(runner.RunnerName)
	})
}

// collectAndRecord reads the completed container's cgroup metrics and records
// them in the history store. Synthetic reconcile runners (empty JobName) are
// skipped — they have no job identity to record history under.
func (s *Scaler) collectAndRecord(ctx context.Context, runner *state.RunnerInfo) {
	if s.metricsCollector == nil || s.metricsStore == nil || runner.JobName == "" {
		return
	}

	duration := time.Since(runner.StartedAt)
	if duration <= 0 {
		duration = time.Second
	}

	jobMetrics, err := s.metricsCollector.Collect(ctx, runner.ContainerID, duration)
	if err != nil {
		s.logger.Warn("failed to collect metrics", "runner_name", runner.RunnerName, "error", err)
		return
	}

	// Allocated values were produced by units.Format* (or validated config),
	// so parse failures are not expected; a zero is recorded if they occur.
	allocCPU, _ := units.ParseCPU(runner.AllocatedCPUs)
	allocMem, _ := units.ParseMemory(runner.AllocatedMemory)
	if err := s.metricsStore.Record(&metrics.MetricsRecord{
		JobName:              runner.JobName,
		Profile:              runner.Profile,
		CPUAllocatedNanoCPUs: allocCPU,
		MemAllocatedBytes:    allocMem,
		CPUUsedNanoCPUs:      jobMetrics.CPUUsedNanoCPUs,
		MemPeakBytes:         jobMetrics.MemPeakBytes,
		DurationSec:          duration.Seconds(),
	}); err != nil {
		s.logger.Warn("failed to record metrics", "runner_name", runner.RunnerName, "error", err)
		return
	}

	s.logger.Info("metrics recorded",
		"runner_name", runner.RunnerName,
		"cpu_used", jobMetrics.CPUUsedNanoCPUs,
		"mem_peak", jobMetrics.MemPeakBytes,
		"duration", duration.Seconds(),
	)
}

// reconcileRunnerCount uses Statistics to detect when we need more runners
// than what JobAssigned messages have told us about. This handles the case
// where a runner runs a different job than the one it was provisioned for,
// leaving the original job orphaned.
func (s *Scaler) reconcileRunnerCount(ctx context.Context, stats *scaleset.RunnerScaleSetStatistic) {
	desiredRunners := stats.TotalAssignedJobs
	activeRunners := s.store.ActiveCount()

	if desiredRunners <= activeRunners {
		return
	}

	deficit := desiredRunners - activeRunners

	s.logger.Info("runner deficit detected — provisioning additional runners",
		"desired", desiredRunners,
		"active", activeRunners,
		"deficit", deficit,
	)

	// Try to use pending job profiles for classification.
	// If we don't have enough pending jobs, provision synthetic runners on
	// the default profile with no job identity.
	s.mu.Lock()
	pendingList := make([]*pendingJob, 0, len(s.pendingJobs))
	for _, pj := range s.pendingJobs {
		pendingList = append(pendingList, pj)
	}
	s.mu.Unlock()

	for i := range deficit {
		var profileName string
		var jobDisplayName string
		var jobID string

		if i < len(pendingList) {
			pj := pendingList[i]
			profileName = pj.profile
			jobDisplayName = pj.displayName
			jobID = pj.jobID
		} else {
			profileName = s.cfg.DefaultProfile
			s.mu.Lock()
			jobID = fmt.Sprintf("reconcile-%d", s.reconcileSeq)
			s.reconcileSeq++
			s.mu.Unlock()
		}

		profile, ok := s.cfg.Profiles[profileName]
		if !ok {
			s.logger.Error("profile not found for reconciliation", "profile", profileName)
			continue
		}

		s.logger.Info("reconciling runner",
			"profile", profileName,
			"job_display_name", jobDisplayName,
			"deficit_index", i,
		)

		if err := s.provisionRunner(ctx, jobDisplayName, jobID, profileName, profile); err != nil {
			s.logger.Error("failed to provision reconciliation runner",
				"profile", profileName,
				"error", err,
			)
		}
	}
}
