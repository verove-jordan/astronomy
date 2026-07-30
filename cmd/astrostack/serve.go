package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/verove-jordan/astronomy/internal/api"
	"github.com/verove-jordan/astronomy/internal/config"
	"github.com/verove-jordan/astronomy/internal/job"
	"github.com/verove-jordan/astronomy/internal/siril"
	"github.com/verove-jordan/astronomy/internal/store"
	"github.com/verove-jordan/astronomy/internal/turns"
)

// sirilLimits builds the Siril resource caps (thread count, memory ratio, OS niceness) from config,
// so a heavy stack stays within bounds instead of saturating CPU and RAM.
func sirilLimits(cfg *config.Config) siril.Limits {
	return siril.Limits{MaxCPUs: cfg.MaxCPUs, MemRatio: cfg.SirilMemRatio, Nice: cfg.SirilNice}
}

func runServe(_ []string) error {
	cfg := config.Load()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("the API needs Postgres — start it with `just up` (%w)", err)
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	// Nothing can be mid-exposure across a restart: the capture runner lives in this process. Rows
	// still marked running were orphaned by a stop, a hot reload or a crash, and would otherwise show
	// as phantom active runs in the sessions list for good.
	if n, err := st.InterruptOrphanedCaptureSessions(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not reconcile capture sessions: %v\n", err)
	} else if n > 0 {
		log.Printf("marked %d interrupted capture session(s) left over from a previous run", n)
	}

	runner := siril.New(cfg.SirilBin, sirilLimits(cfg))
	if err := runner.Available(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "warning: Siril unavailable — processing jobs will fail (%v)\n", err)
	}

	// One shared turn hub drives both the AstroAgent chat and supervised-job conversations, so a
	// supervised finish streams (and takes steering) over the same SSE transport as the agent.
	hub := turns.NewSessions()
	mgr := job.NewManager(st, runner, cfg, hub)
	workers := cfg.MaxWorkers
	if workers <= 0 {
		workers = runtime.NumCPU() / 2
	}
	if workers < 1 {
		workers = 1
	}
	mgr.Start(ctx, workers)

	srv := &http.Server{
		Addr:    cfg.APIAddr,
		Handler: api.New(mgr, st, cfg, hub).Handler(),
	}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	fmt.Printf("astrostack API listening on %s (%d workers)\n", cfg.APIAddr, workers)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
