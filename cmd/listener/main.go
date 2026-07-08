package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/reidg44/gh-runners-proxy-assignment/internal/bootstrap"
	"github.com/reidg44/gh-runners-proxy-assignment/internal/config"
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

	comps, err := bootstrap.Setup(ctx, cfg, state.NewStore(), logger)
	if err != nil {
		return err
	}
	defer comps.Close()

	logger.Info("listener started, waiting for jobs...")
	return comps.Scaler.Run(ctx)
}
