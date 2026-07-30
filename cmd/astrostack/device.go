package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/verove-jordan/astronomy/internal/config"
	"github.com/verove-jordan/astronomy/internal/devsrv"
)

// runDevice starts the device server: the process that owns the camera, filter wheel and mount.
//
// It is separate from `serve` on purpose. `just dev` runs the engine under air, which restarts it
// on every source save — that would drop a USB connection mid-sequence and abandon a cooling ramp.
// A vendor SDK that segfaults takes its process down with it, and losing the device server is
// survivable where losing a three-hour stack is not. And while the engine's workers saturate the
// CPU stacking, this process keeps the live view and focus meter responsive.
//
// The engine reverse-proxies /api/device/* here, so the browser only ever talks to one origin.
func runDevice(_ []string) error {
	cfg := config.Load()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	server := devsrv.New(cfg)
	defer server.Close()

	srv := &http.Server{
		Addr:              cfg.DeviceAddr,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: the live-view SSE stream and long exposures both outlive any sane one.
	}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	log.Printf("astrostack device: serving on http://%s (drivers: %s). Ctrl-C to stop.",
		cfg.DeviceAddr, driverNames(server))
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("device server: %w", err)
	}
	return nil
}

// driverNames renders the available drivers for the startup banner, so it is obvious at a glance
// whether real hardware was found or only the simulator.
func driverNames(s *devsrv.Server) string {
	names := ""
	for _, d := range s.Drivers() {
		if !d.Available {
			continue
		}
		if names != "" {
			names += ", "
		}
		names += d.Name
	}
	if names == "" {
		return "none"
	}
	return names
}
