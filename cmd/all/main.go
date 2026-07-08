package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/reidg44/gh-runners-proxy-assignment/internal/bootstrap"
	"github.com/reidg44/gh-runners-proxy-assignment/internal/config"
	proxypkg "github.com/reidg44/gh-runners-proxy-assignment/internal/proxy"
	"github.com/reidg44/gh-runners-proxy-assignment/internal/state"
	"github.com/spf13/cobra"
)

func main() {
	var configPath string

	cmd := &cobra.Command{
		Use:   "gh-proxy",
		Short: "GitHub Actions runner proxy — listener + proxy combined",
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

	// Shared state store
	store := state.NewStore()

	// Start proxy first (runners need it available)
	proxySrv := proxypkg.NewServer(store, logger)
	proxyHTTP := &http.Server{
		Handler: proxySrv.Handler(),
	}

	proxyListener, err := net.Listen("tcp", cfg.Proxy.ListenAddr)
	if err != nil {
		return fmt.Errorf("binding proxy listener on %s: %w", cfg.Proxy.ListenAddr, err)
	}

	go func() {
		logger.Info("proxy listening", "addr", cfg.Proxy.ListenAddr)
		if err := proxyHTTP.Serve(proxyListener); err != nil && err != http.ErrServerClosed {
			logger.Error("proxy server error", "error", err)
		}
	}()

	comps, err := bootstrap.Setup(ctx, cfg, store, logger)
	if err != nil {
		return err
	}
	defer comps.Close()

	// Start scaler in goroutine
	scalerDone := make(chan error, 1)
	go func() {
		logger.Info("scaler started, waiting for jobs...")
		scalerDone <- comps.Scaler.Run(ctx)
	}()

	// Wait for shutdown signal or scaler error
	select {
	case sig := <-sigCh:
		logger.Info("received signal, initiating graceful shutdown", "signal", sig)
	case err := <-scalerDone:
		if err != nil && err != context.Canceled {
			logger.Error("scaler stopped with error", "error", err)
		}
	}

	// Graceful shutdown
	cancel()

	// Stop proxy
	logger.Info("stopping proxy")
	_ = proxyHTTP.Close()

	// Cleanup running containers
	runners := store.All()
	if len(runners) > 0 {
		logger.Info("cleaning up runner containers", "count", len(runners))
		containerIDs := make([]string, 0, len(runners))
		for _, r := range runners {
			if r.ContainerID != "" {
				containerIDs = append(containerIDs, r.ContainerID)
			}
		}
		comps.Provisioner.StopAll(context.Background(), containerIDs)
	}

	logger.Info("shutdown complete")
	return nil
}
