// Package config loads runtime configuration from the environment.
package config

import (
	"os"
	"path/filepath"
	"strconv"
	"time"
)

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
	GimpHost  string
	GimpPort  int
	FfmpegBin string

	// Optional astro-AI host tools (invoked like Siril/GIMP, never bundled). Empty/missing →
	// the pipeline falls back to Siril (subsky) and skips star removal.
	GraxpertBin string // GraXpert: AI background-gradient extraction + denoise
	StarnetBin  string // StarNet++ v2: star removal (for star-reduced finishing)

	// Plate-solving + SPCC (color calibration). Focal/pixel describe the rig (the FITS rarely
	// carries FOCALLEN); the SPCC names must match Siril's catalogs. Empty values fall back to
	// Siril defaults. PlateSolveCatalog empty → Siril chooses automatically.
	FocalLenMM        float64
	PixelSizeUm       float64
	SpccMonoSensor    string
	SpccRFilter       string
	SpccGFilter       string
	SpccBFilter       string
	SpccWhiteRef      string
	PlateSolveCatalog string
	SirilCatalogDir   string // Siril's bundled object catalogues (for name→coords resolution)

	// Cross-session reuse. Reuse pools prior light frames of the same target (to grow integration)
	// and prior raw bias/darks (for deeper, lower-noise masters). ReuseEnabled gates the whole
	// feature; ReuseConeDeg is the coordinate-match radius; ReuseDarkRecencyDays bounds how old a
	// dark may be (0 = unbounded); ReuseTempTolC is the dark temperature tolerance (°C).
	ReuseEnabled         bool
	ReuseConeDeg         float64
	ReuseDarkRecencyDays int
	ReuseTempTolC        float64
}

// Load reads configuration from the environment, applying sensible defaults.
func Load() *Config {
	sirilBin := env("SIRIL_BIN", "/Applications/Siril.app/Contents/MacOS/siril-cli")
	catalogDir := env("ASTRO_SIRIL_CATALOG_DIR", "")
	if catalogDir == "" { // derive from the Siril app bundle (macOS host-engine)
		catalogDir = filepath.Clean(filepath.Join(filepath.Dir(sirilBin), "..", "Resources", "share", "siril", "catalogue"))
	}
	return &Config{
		DatabaseURL: env("DATABASE_URL", "postgres://astro:astro@localhost:5432/astrostack?sslmode=disable"),
		APIAddr:     env("API_ADDR", ":8080"),
		LogLevel:    env("LOG_LEVEL", "info"),
		DataDir:     env("ASTRO_DATA_DIR", "./data"),
		WorkDir:     env("ASTRO_WORK_DIR", "./work"),
		OutputDir:   env("ASTRO_OUTPUT_DIR", "./output"),
		LibraryDir:  env("ASTRO_LIBRARY_DIR", "./library"),
		SirilBin:    sirilBin,
		GimpBin:     env("GIMP_BIN", "/Applications/GIMP.app/Contents/MacOS/gimp-console-2.10"),
		GimpHost:    env("GIMP_HOST", "127.0.0.1"),
		GimpPort:    envInt("GIMP_PORT", 10008),
		FfmpegBin:   env("FFMPEG_BIN", "ffmpeg"),
		GraxpertBin: env("GRAXPERT_BIN", "/Applications/GraXpert.app/Contents/MacOS/GraXpert"),
		StarnetBin:  env("STARNET_BIN", "starnet++"),

		FocalLenMM:        envFloat("ASTRO_FOCAL_MM", 740), // Takahashi FC-100 DF native
		PixelSizeUm:       envFloat("ASTRO_PIXEL_UM", 3.8), // ASI1600MM Pro
		SpccMonoSensor:    env("ASTRO_SPCC_SENSOR", "ZWO ASI1600MM Pro"),
		SpccRFilter:       env("ASTRO_SPCC_RFILTER", ""),
		SpccGFilter:       env("ASTRO_SPCC_GFILTER", ""),
		SpccBFilter:       env("ASTRO_SPCC_BFILTER", ""),
		SpccWhiteRef:      env("ASTRO_SPCC_WHITEREF", "Average Spiral Galaxy"),
		PlateSolveCatalog: env("ASTRO_PLATESOLVE_CATALOG", ""),
		SirilCatalogDir:   catalogDir,

		ReuseEnabled:         envBool("ASTRO_REUSE_ENABLED", true),
		ReuseConeDeg:         envFloat("ASTRO_REUSE_CONE_DEG", 0.5),
		ReuseDarkRecencyDays: envInt("ASTRO_REUSE_DARK_RECENCY_DAYS", 0),
		ReuseTempTolC:        envFloat("ASTRO_REUSE_TEMP_TOL_C", 5.0),
	}
}

func envFloat(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// DarkSinceMs is the epoch-ms cutoff below which darks are too old to reuse, or 0 (unbounded) when
// ReuseDarkRecencyDays is 0.
func (c *Config) DarkSinceMs() int64 {
	if c.ReuseDarkRecencyDays <= 0 {
		return 0
	}
	return time.Now().AddDate(0, 0, -c.ReuseDarkRecencyDays).UnixMilli()
}

func envBool(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
