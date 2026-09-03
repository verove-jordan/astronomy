package toolhealth

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/config"
)

// fakeBin writes an executable shell script that prints body, and returns its path. It is how these
// tests get a binary that resolves and answers `-version` without depending on the host's ffmpeg.
func fakeBin(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755))
	return path
}

// The banner ffmpeg actually prints. The version is the third field of the first line, and
// everything after it is build noise that must not leak into the report.
const ffmpegBanner = `echo "ffmpeg version 7.1 Copyright (c) 2000-2024 the FFmpeg developers"
echo "built with Apple clang version 16.0.0"`

func TestFFmpegHealth(t *testing.T) {
	tests := []struct {
		name string
		// setup returns the FfmpegBin to configure.
		setup      func(t *testing.T, dir string) string
		wantOK     bool
		wantDetail string
		wantErrSub string
	}{
		{
			name: "ffmpeg and ffprobe both present",
			setup: func(t *testing.T, dir string) string {
				fakeBin(t, dir, "ffprobe", `echo "ffprobe version 7.1"`)
				return fakeBin(t, dir, "ffmpeg", ffmpegBanner)
			},
			wantOK:     true,
			wantDetail: "7.1",
		},
		{
			// ffprobe missing is reported but is NOT a failure: the video probers degrade to 8-bit
			// extraction rather than stopping, so calling it broken would overstate the problem.
			name: "ffmpeg present, ffprobe missing",
			setup: func(t *testing.T, dir string) string {
				return fakeBin(t, dir, "ffmpeg", ffmpegBanner)
			},
			wantOK:     true,
			wantDetail: "7.1 (no ffprobe — video probing degrades to 8-bit)",
		},
		{
			// The binary resolves but will not answer --version. It still counts as present, because
			// what the pipeline needs is something to exec; only the version label is lost.
			name: "ffmpeg present but unversionable",
			setup: func(t *testing.T, dir string) string {
				fakeBin(t, dir, "ffprobe", `echo "ffprobe version 7.1"`)
				return fakeBin(t, dir, "ffmpeg", "exit 1")
			},
			wantOK:     true,
			wantDetail: "",
		},
		{
			name:       "ffmpeg absent",
			setup:      func(t *testing.T, dir string) string { return filepath.Join(dir, "nope-ffmpeg") },
			wantOK:     false,
			wantErrSub: "not found",
		},
		{
			// An empty FFMPEG_BIN must fall back to the PATH name rather than looking up "", which
			// would report the confusing `ffmpeg binary "" not found`.
			name:       "empty config falls back to the bare name",
			setup:      func(t *testing.T, dir string) string { return "" },
			wantOK:     false,
			wantErrSub: `"ffmpeg" not found`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			// Keep the host's real ffmpeg out of the "absent" cases: LookPath consults PATH for a
			// bare name, and this machine has one installed.
			t.Setenv("PATH", dir)
			t.Setenv("FFPROBE_BIN", "")

			c := &Checker{cfg: &config.Config{FfmpegBin: tt.setup(t, dir)}}
			got := c.ffmpegHealth(context.Background())

			assert.Equal(t, tt.wantOK, got.OK)
			if tt.wantErrSub != "" {
				assert.Contains(t, got.Err, tt.wantErrSub)
				return
			}
			assert.Empty(t, got.Err)
			assert.Equal(t, tt.wantDetail, got.Detail)
		})
	}
}

// ffprobe is resolved the same way the video probers resolve it, so the report cannot claim a
// probe the pipeline would not find (or miss one it would).
func TestFFprobeBin(t *testing.T) {
	t.Run("defaults to ffmpeg's sibling", func(t *testing.T) {
		t.Setenv("FFPROBE_BIN", "")
		assert.Equal(t, "/opt/bin/ffprobe", ffprobeBin("/opt/bin/ffmpeg"))
	})
	t.Run("FFPROBE_BIN wins", func(t *testing.T) {
		t.Setenv("FFPROBE_BIN", "/custom/ffprobe")
		assert.Equal(t, "/custom/ffprobe", ffprobeBin("/opt/bin/ffmpeg"))
	})
}
