package scaler

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/actions/scaleset"
	"github.com/reidg44/gh-runners-proxy-assignment/internal/classifier"
	"github.com/reidg44/gh-runners-proxy-assignment/internal/config"
	"github.com/reidg44/gh-runners-proxy-assignment/internal/state"
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

// Scaler implements a custom message loop that inspects per-job details
// to provision runners with appropriate resource profiles.
type Scaler struct {
	sessionClient SessionClient
	jitGenerator  JITConfigGenerator
	provisioner   RunnerProvisioner
	classifier    *classifier.Classifier
	store         *state.Store
	cfg           *config.Config
	scaleSetID    int
	proxyURL      string
	logger        *slog.Logger

	// pendingJobs tracks assigned jobs that haven't been picked up by a runner yet.
	// Protected by mu.
	mu          sync.Mutex
	pendingJobs map[string]*pendingJob // jobID -> pendingJob
}

// New creates a new Scaler.
func New(
	sessionClient SessionClient,
	jitGenerator JITConfigGenerator,
	provisioner RunnerProvisioner,
	classifier *classifier.Classifier,
	store *state.Store,
	cfg *config.Config,
	scaleSetID int,
	proxyURL string,
	logger *slog.Logger,
) *Scaler {
	return &Scaler{
		sessionClient: sessionClient,
		jitGenerator:  jitGenerator,
		provisioner:   provisioner,
		classifier:    classifier,
		store:         store,
		cfg:           cfg,
		scaleSetID:    scaleSetID,
		proxyURL:      proxyURL,
		logger:        logger,
		pendingJobs:   make(map[string]*pendingJob),
	}
}

// Run starts the message processing loop. It blocks until the context is cancelled.
func (s *Scaler) Run(ctx context.Context) error {
	lastMessageID := 0
	maxCapacity := s.cfg.Runner.MaxRunners

	s.logger.Info("scaler started", "max_capacity", maxCapacity)

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		availableCapacity := maxCapacity - s.store.ActiveCount()
		if availableCapacity < 0 {
			availableCapacity = 0
		}

		s.logger.Debug("polling for messages",
			"last_message_id", lastMessageID,
			"available_capacity", availableCapacity,
			"active_runners", s.store.ActiveCount(),
			"pending_jobs", len(s.pendingJobs),
		)

		msg, err := s.sessionClient.GetMessage(ctx, lastMessageID, availableCapacity)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			s.logger.Error("failed to get message", "error", err)
			continue
		}

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

	// Start the runner container
	containerID, containerIP, err := s.provisioner.StartRunner(ctx, runnerName, profile, jitCfg.EncodedJITConfig, s.proxyURL)
	if err != nil {
		return fmt.Errorf("starting runner container: %w", err)
	}

	s.store.AddRunner(&state.RunnerInfo{
		RunnerName:  runnerName,
		ContainerID: containerID,
		ContainerIP: containerIP,
		Profile:     profileName,
		JobID:       jobID,
		JobName:     jobDisplayName,
	})

	s.logger.Info("runner provisioned",
		"runner_name", runnerName,
		"container_id", truncateID(containerID),
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

func truncateID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
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

	s.store.MarkCompleted(job.RunnerName)

	runner, ok := s.store.GetByName(job.RunnerName)
	if !ok {
		s.logger.Warn("completed job for unknown runner", "runner_name", job.RunnerName)
		return
	}

	if err := s.provisioner.StopRunner(ctx, runner.ContainerID); err != nil {
		s.logger.Error("failed to stop runner container",
			"runner_name", job.RunnerName,
			"container_id", runner.ContainerID,
			"error", err,
		)
	}

	s.store.Remove(job.RunnerName)
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
	// If we don't have enough pending jobs, use default profile.
	s.mu.Lock()
	pendingList := make([]*pendingJob, 0, len(s.pendingJobs))
	for _, pj := range s.pendingJobs {
		pendingList = append(pendingList, pj)
	}
	s.mu.Unlock()

	for i := 0; i < deficit; i++ {
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
			jobDisplayName = "unknown-reconcile"
			jobID = fmt.Sprintf("reconcile-%d", i)
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
