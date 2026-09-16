// Command build-monitor serves a CircleCI build radiator on localhost.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/willowworks-io/build-monitor/internal/circleci"
	"github.com/willowworks-io/build-monitor/internal/config"
	"github.com/willowworks-io/build-monitor/internal/monitor"
	"github.com/willowworks-io/build-monitor/internal/web"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "build-monitor: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "config.yaml", "path to the config file")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	token, err := cfg.Token()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	mon := monitor.New(cfg, circleci.New(token), log)
	go mon.Run(ctx)

	srv := web.New(mon, log)
	log.Info("radiator up",
		"url", "http://"+cfg.Listen,
		"orgs", len(cfg.Orgs),
		"poll", cfg.PollInterval.String(),
		"branches", string(cfg.BranchFilter))

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe(cfg.Listen) }()

	select {
	case <-ctx.Done():
		log.Info("shutting down")
		return nil
	case err := <-errCh:
		return err
	}
}
