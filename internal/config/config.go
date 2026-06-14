// Package config loads runtime configuration from the environment.
package config

import "os"

// Config holds all runtime configuration for the engine.
type Config struct {
	DatabaseURL string
	APIAddr     string
	LogLevel    string

	DataDir    string // root the UI may browse for capture folders
	WorkDir    string // scratch space for intermediate FITS / sequences
	OutputDir  string // where final stacks and reports are written
	LibraryDir string // persistent master-calibration library

	SirilBin  string
	GimpBin   string
	FfmpegBin string
}

// Load reads configuration from the environment, applying sensible defaults.
func Load() *Config {
	return &Config{
		DatabaseURL: env("DATABASE_URL", "postgres://astro:astro@localhost:5432/astrostack?sslmode=disable"),
		APIAddr:     env("API_ADDR", ":8080"),
		LogLevel:    env("LOG_LEVEL", "info"),
		DataDir:     env("ASTRO_DATA_DIR", "./data"),
		WorkDir:     env("ASTRO_WORK_DIR", "./work"),
		OutputDir:   env("ASTRO_OUTPUT_DIR", "./output"),
		LibraryDir:  env("ASTRO_LIBRARY_DIR", "./library"),
		SirilBin:    env("SIRIL_BIN", "/Applications/Siril.app/Contents/MacOS/siril-cli"),
		GimpBin:     env("GIMP_BIN", "/Applications/GIMP.app/Contents/MacOS/gimp-console-2.10"),
		FfmpegBin:   env("FFMPEG_BIN", "ffmpeg"),
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
