// Package bootstrap wires up everything the listener entry points share:
// the scaleset client and message session (including stale-session recovery),
// the runner provisioner, the optional adaptive-metrics components, and the
// scaler itself. cmd/all and cmd/listener differ only in what they run around
// these components, so all construction lives here.
package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/actions/scaleset"
	"github.com/reidg44/gh-runners-proxy-assignment/internal/classifier"
	"github.com/reidg44/gh-runners-proxy-assignment/internal/config"
	"github.com/reidg44/gh-runners-proxy-assignment/internal/metrics"
	"github.com/reidg44/gh-runners-proxy-assignment/internal/runner"
	"github.com/reidg44/gh-runners-proxy-assignment/internal/scaler"
	"github.com/reidg44/gh-runners-proxy-assignment/internal/state"
)

const (
	systemName    = "gh-proxy"
	systemVersion = "0.1.0"

	// metricsGracePeriod keeps runner containers alive after the job exits so
	// the metrics collector can read cgroup files before the container stops
	// (the JobCompleted message arrives over the network after the runner
	// process exits). Only applied when adaptive scaling is enabled.
	metricsGracePeriod = 15 * time.Second
)

// Components holds the fully wired scaler and its dependencies.
type Components struct {
	Scaler      *scaler.Scaler
	Provisioner *runner.Provisioner

	sessionClient *scaleset.MessageSessionClient
	metricsStore  *metrics.Store
	logger        *slog.Logger
}

// Setup builds the scaleset session, runner provisioner, adaptive-metrics
// components, and scaler from config. The returned Components must be closed
// with Close after the scaler stops.
func Setup(ctx context.Context, cfg *config.Config, store *state.Store, logger *slog.Logger) (*Components, error) {
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("GITHUB_TOKEN environment variable is required")
	}

	client, err := scaleset.NewClientWithPersonalAccessToken(scaleset.NewClientWithPersonalAccessTokenConfig{
		GitHubConfigURL:     cfg.GitHub.RepositoryURL,
		PersonalAccessToken: token,
		SystemInfo:          systemInfo(0),
	})
	if err != nil {
		return nil, fmt.Errorf("creating scaleset client: %w", err)
	}

	// Resolve runner group
	runnerGroupName := cfg.GitHub.RunnerGroup
	if runnerGroupName == "" {
		runnerGroupName = scaleset.DefaultRunnerGroup
	}
	runnerGroup, err := client.GetRunnerGroupByName(ctx, runnerGroupName)
	if err != nil {
		return nil, fmt.Errorf("resolving runner group %q: %w", runnerGroupName, err)
	}
	logger.Info("resolved runner group", "name", runnerGroup.Name, "id", runnerGroup.ID)

	// Create or reuse scale set
	scaleSet, err := getOrCreateScaleSet(ctx, client, cfg, runnerGroup.ID, logger)
	if err != nil {
		return nil, fmt.Errorf("setting up scale set: %w", err)
	}
	logger.Info("using scale set", "name", scaleSet.Name, "id", scaleSet.ID)
	client.SetSystemInfo(systemInfo(scaleSet.ID))

	// Create message session (handle stale session conflict by recreating the scale set)
	hostname, _ := os.Hostname()
	sessionClient, err := client.MessageSessionClient(ctx, scaleSet.ID, hostname)
	if err != nil {
		if !isSessionConflict(err) {
			return nil, fmt.Errorf("creating message session: %w", err)
		}
		logger.Warn("stale session detected, deleting and recreating scale set")
		_ = client.DeleteRunnerScaleSet(ctx, scaleSet.ID)
		scaleSet, err = createScaleSet(ctx, client, cfg, runnerGroup.ID)
		if err != nil {
			return nil, fmt.Errorf("recreating scale set: %w", err)
		}
		client.SetSystemInfo(systemInfo(scaleSet.ID))
		logger.Info("recreated scale set", "name", scaleSet.Name, "id", scaleSet.ID)
		sessionClient, err = client.MessageSessionClient(ctx, scaleSet.ID, hostname)
		if err != nil {
			return nil, fmt.Errorf("creating message session after recreation: %w", err)
		}
	}

	c := &Components{sessionClient: sessionClient, logger: logger}

	// Initialize runner provisioner. Containers only need to outlive the job
	// for metrics collection, so the grace period applies only when adaptive
	// scaling is on.
	grace := time.Duration(0)
	if cfg.Adaptive.Enabled {
		grace = metricsGracePeriod
	}
	prov, err := runner.New(ctx, cfg.Runner.Image, grace, logger)
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("creating runner provisioner: %w", err)
	}
	c.Provisioner = prov

	proxyURL := fmt.Sprintf("http://%s%s", prov.GatewayIP(), cfg.Proxy.ListenAddr)
	logger.Info("proxy URL for runners", "url", proxyURL)

	// Initialize adaptive scaling components (if enabled)
	var metricsCollector metrics.Collector
	var adjuster *metrics.Adjuster
	if cfg.Adaptive.Enabled {
		logger.Info("adaptive scaling enabled", "db_path", cfg.Adaptive.DBPath)
		c.metricsStore, err = metrics.NewStore(cfg.Adaptive.DBPath)
		if err != nil {
			c.Close()
			return nil, fmt.Errorf("opening metrics store: %w", err)
		}
		metricsCollector = metrics.NewDockerCollector(prov.DockerClient())
		adjuster = metrics.NewAdjuster(cfg.Adaptive)
	}

	c.Scaler = scaler.New(scaler.Options{
		SessionClient:    sessionClient,
		JITGenerator:     client,
		Provisioner:      prov,
		Classifier:       classifier.New(cfg.OrderedProfiles, cfg.DefaultProfile),
		Store:            store,
		Config:           cfg,
		ScaleSetID:       scaleSet.ID,
		ProxyURL:         proxyURL,
		MetricsCollector: metricsCollector,
		MetricsStore:     c.metricsStore,
		Adjuster:         adjuster,
		Logger:           logger,
	})

	return c, nil
}

// Close releases the message session and metrics store.
func (c *Components) Close() {
	if c.sessionClient != nil {
		c.logger.Info("closing message session")
		_ = c.sessionClient.Close(context.Background())
	}
	if c.metricsStore != nil {
		_ = c.metricsStore.Close()
	}
}

func systemInfo(scaleSetID int) scaleset.SystemInfo {
	return scaleset.SystemInfo{
		System:     systemName,
		Version:    systemVersion,
		ScaleSetID: scaleSetID,
	}
}

// isSessionConflict reports whether err is the 409 returned when another
// listener holds the scale set's message session. The SDK exposes no typed
// conflict error, so this string match is the only detection available.
func isSessionConflict(err error) bool {
	return strings.Contains(err.Error(), "409") || strings.Contains(err.Error(), "Conflict")
}

func getOrCreateScaleSet(ctx context.Context, client *scaleset.Client, cfg *config.Config, runnerGroupID int, logger *slog.Logger) (*scaleset.RunnerScaleSet, error) {
	existing, err := client.GetRunnerScaleSet(ctx, runnerGroupID, cfg.GitHub.ScaleSetName)
	if err == nil && existing != nil {
		logger.Info("found existing scale set", "name", existing.Name, "id", existing.ID)
		return existing, nil
	}
	return createScaleSet(ctx, client, cfg, runnerGroupID)
}

func createScaleSet(ctx context.Context, client *scaleset.Client, cfg *config.Config, runnerGroupID int) (*scaleset.RunnerScaleSet, error) {
	created, err := client.CreateRunnerScaleSet(ctx, &scaleset.RunnerScaleSet{
		Name:          cfg.GitHub.ScaleSetName,
		RunnerGroupID: runnerGroupID,
		Labels: []scaleset.Label{
			{Name: cfg.GitHub.RunnerLabel, Type: "User"},
		},
		RunnerSetting: scaleset.RunnerSetting{
			DisableUpdate: true,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("creating scale set: %w", err)
	}
	return created, nil
}
