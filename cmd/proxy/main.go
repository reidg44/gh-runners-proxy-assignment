package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/reidg44/gh-runners-proxy-assignment/internal/config"
	proxypkg "github.com/reidg44/gh-runners-proxy-assignment/internal/proxy"
	"github.com/reidg44/gh-runners-proxy-assignment/internal/state"
	"github.com/spf13/cobra"
)

func main() {
	var configPath string

	cmd := &cobra.Command{
		Use:   "proxy",
		Short: "HTTP CONNECT proxy for monitoring runner traffic",
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

	store := state.NewStore()
	srv := proxypkg.NewServer(store, logger)

	server := &http.Server{
		Addr:    cfg.Proxy.ListenAddr,
		Handler: srv.Handler(),
	}

	// Handle shutdown signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		logger.Info("shutting down proxy")
		server.Close()
	}()

	logger.Info("proxy listening", "addr", cfg.Proxy.ListenAddr)
	return server.ListenAndServe()
}
