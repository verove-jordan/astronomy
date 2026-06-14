package main

import (
	"context"
	"errors"
	"fmt"
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
)

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

	runner := siril.New(cfg.SirilBin)
	if err := runner.Available(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "warning: Siril unavailable — processing jobs will fail (%v)\n", err)
	}

	mgr := job.NewManager(st, runner, cfg)
	workers := runtime.NumCPU() / 2
	if workers < 1 {
		workers = 1
	}
	mgr.Start(ctx, workers)

	srv := &http.Server{
		Addr:    cfg.APIAddr,
		Handler: api.New(mgr, st, cfg).Handler(),
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
