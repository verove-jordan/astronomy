// Command graxpert-host is a tiny native HTTP service that runs the host-installed GraXpert on demand, so
// the containerized engine can offload denoise / background-extraction to a NATIVE process — Docker on
// macOS can't exec a host binary, and CoreML is unreachable from the Linux container. It shares the
// engine's absolute file paths (the compose bind mounts), so requests carry only paths, never image
// bytes. Mirrors the finish-supervisor's host model (scripts/ia-model.sh / `just run-ia-model`): a host
// tool, invoked, never vendored. Run it with `just run-graxpert-service`.
//
// GraXpert's DENOISE AI model does not compile on Apple's CoreML (verified), so this service forces
// -gpu false for denoise — GPU would crash with a CoreML error. Background-extraction IS CoreML-
// compatible and honours the requested GPU flag.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/verove-jordan/astronomy/internal/config"
	"github.com/verove-jordan/astronomy/internal/graxpert"
)

func main() {
	cfg := config.Load()
	// A LOCAL runner (empty URL) — this service is the offload target, so it must exec GraXpert directly,
	// never forward to itself.
	runner := graxpert.New(cfg.GraxpertBin, "")
	addr := "127.0.0.1:" + port()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		if err := runner.Available(r.Context()); err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("POST /run", func(w http.ResponseWriter, r *http.Request) { handleRun(runner, w, r) })

	// No WriteTimeout: a denoise can stream for many minutes; the client's request context bounds it.
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Printf("graxpert-host: serving on http://%s (GRAXPERT_BIN=%q). Ctrl-C to stop.", addr, cfg.GraxpertBin)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("graxpert-host: %v", err)
	}
}

func port() string {
	if p := os.Getenv("ASTRO_GRAXPERT_PORT"); p != "" {
		return p
	}
	return "8083"
}

// handleRun runs one GraXpert operation, streaming its output back line-by-line, then a terminal
// ResultPrefix line ("ok" or "error:<msg>"). GraXpert's progress lines flow through unchanged, so the
// engine's job log looks identical whether GraXpert ran locally or here.
func handleRun(runner *graxpert.Runner, w http.ResponseWriter, r *http.Request) {
	var req graxpert.RemoteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	onProgress := func(p graxpert.Progress) {
		if p.Line == "" {
			return
		}
		fmt.Fprintln(w, p.Line)
		flusher.Flush()
	}

	if err := runOp(r.Context(), runner, req, onProgress); err != nil {
		fmt.Fprintf(w, "%serror:%s\n", graxpert.ResultPrefix, err.Error())
	} else {
		fmt.Fprintf(w, "%sok\n", graxpert.ResultPrefix)
	}
	flusher.Flush()
}

func runOp(ctx context.Context, runner *graxpert.Runner, req graxpert.RemoteRequest, onProgress func(graxpert.Progress)) error {
	switch req.Op {
	case graxpert.OpDenoise:
		// Denoise model is CoreML-incompatible → force CPU so a requested -gpu true never crashes here.
		return runner.Denoise(ctx, req.In, req.Out, graxpert.DenoiseOptions{GPU: false, Batch: req.Batch, Strength: req.Strength}, onProgress)
	case graxpert.OpBackground:
		return runner.ExtractBackground(ctx, req.In, req.Out, graxpert.BackgroundOptions{GPU: req.GPU}, onProgress)
	default:
		return fmt.Errorf("unknown op %q", req.Op)
	}
}
