package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/actions/scaleset"
	"github.com/reidg44/gh-runners-proxy-assignment/internal/classifier"
	"github.com/reidg44/gh-runners-proxy-assignment/internal/config"
	"github.com/reidg44/gh-runners-proxy-assignment/internal/runner"
	scalerpkg "github.com/reidg44/gh-runners-proxy-assignment/internal/scaler"
	"github.com/reidg44/gh-runners-proxy-assignment/internal/state"
	"github.com/spf13/cobra"
)

func main() {
	var configPath string

	cmd := &cobra.Command{
		Use:   "listener",
		Short: "GitHub Actions runner listener with job-aware provisioning",
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(configPath)
		},
	}
	cmd.Flags().StringVarP(&configPath, "config", "c", "config.yaml", "path to config file")

	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func run(configPath string) error {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	token := os.Getenv("GITHUB_TOKEN")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		logger.Info("received signal, shutting down", "signal", sig)
		cancel()
	}()

	// Create scaleset client
	client, err := scaleset.NewClientWithPersonalAccessToken(scaleset.NewClientWithPersonalAccessTokenConfig{
		GitHubConfigURL:     cfg.GitHub.RepositoryURL,
		PersonalAccessToken: token,
		SystemInfo: scaleset.SystemInfo{
			System:  "gh-proxy",
			Version: "0.1.0",
		},
	})
	if err != nil {
		return fmt.Errorf("creating scaleset client: %w", err)
	}

	// Resolve runner group
	runnerGroup, err := client.GetRunnerGroupByName(ctx, cfg.GitHub.RunnerGroup)
	if err != nil {
		return fmt.Errorf("resolving runner group %q: %w", cfg.GitHub.RunnerGroup, err)
	}
	logger.Info("resolved runner group", "name", runnerGroup.Name, "id", runnerGroup.ID)

	// Create or reuse scale set
	scaleSet, err := getOrCreateScaleSet(ctx, client, cfg, runnerGroup.ID, logger)
	if err != nil {
		return fmt.Errorf("setting up scale set: %w", err)
	}
	logger.Info("using scale set", "name", scaleSet.Name, "id", scaleSet.ID)

	client.SetSystemInfo(scaleset.SystemInfo{
		System:     "gh-proxy",
		Version:    "0.1.0",
		ScaleSetID: scaleSet.ID,
	})

	// Create message session (handle stale session conflict)
	hostname, _ := os.Hostname()
	sessionClient, err := client.MessageSessionClient(ctx, scaleSet.ID, hostname)
	if err != nil {
		if strings.Contains(err.Error(), "409") || strings.Contains(err.Error(), "Conflict") {
			logger.Warn("stale session detected, deleting and recreating scale set")
			_ = client.DeleteRunnerScaleSet(ctx, scaleSet.ID)
			scaleSet, err = createScaleSet(ctx, client, cfg, runnerGroup.ID)
			if err != nil {
				return fmt.Errorf("recreating scale set: %w", err)
			}
			client.SetSystemInfo(scaleset.SystemInfo{
				System:     "gh-proxy",
				Version:    "0.1.0",
				ScaleSetID: scaleSet.ID,
			})
			logger.Info("recreated scale set", "name", scaleSet.Name, "id", scaleSet.ID)
			sessionClient, err = client.MessageSessionClient(ctx, scaleSet.ID, hostname)
			if err != nil {
				return fmt.Errorf("creating message session after recreation: %w", err)
			}
		} else {
			return fmt.Errorf("creating message session: %w", err)
		}
	}
	defer func() {
		logger.Info("closing message session")
		_ = sessionClient.Close(context.Background())
	}()

	// Initialize components
	store := state.NewStore()
	cls := classifier.New(cfg.OrderedProfiles, cfg.DefaultProfile)

	prov, err := runner.New(ctx, cfg.Runner.Image, logger)
	if err != nil {
		return fmt.Errorf("creating runner provisioner: %w", err)
	}

	proxyURL := fmt.Sprintf("http://%s%s", prov.GatewayIP(), cfg.Proxy.ListenAddr)

	s := scalerpkg.New(sessionClient, client, prov, cls, store, cfg, scaleSet.ID, proxyURL, logger)

	logger.Info("listener started, waiting for jobs...")
	return s.Run(ctx)
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
