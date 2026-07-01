// Package config loads runtime configuration from the environment.
package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

	// PreviewMaxEdge caps the longest edge (px) of the in-browser file-preview buffer the API decodes
	// for the file viewer; smaller = less memory/transfer, larger = more detail when zooming.
	PreviewMaxEdge int

	SirilBin  string
	GimpBin   string
	GimpHost  string
	GimpPort  int
	FfmpegBin string

	// Optional astro-AI host tools (invoked like Siril/GIMP, never bundled). Empty/missing →
	// the pipeline falls back to Siril (subsky) and skips star removal.
	GraxpertBin string // GraXpert: AI background-gradient extraction + denoise
	StarnetBin  string // StarNet++ v2: star removal (for star-reduced finishing)

	// Optional local LLM "supervisor" (opt-in via the run request / --supervise). The engine drives a
	// host-run, OpenAI-compatible model server (LM Studio / mlx-vlm) over HTTP to auto-tune the finish.
	// An empty URL or an unreachable server → the run uses the normal single-pass finish.
	LLMBaseURL     string        // OpenAI-compatible base, e.g. http://127.0.0.1:1234/v1
	LLMModel       string        // chat/vision model id served there
	LLMImageFormat string        // vision wire-format: "openai" (default) or "mlxvlm"
	LLMTimeout     time.Duration // max wall-clock for one chat/vision completion; 0 → no limit
	// LLMAssistPromptExtra is appended to the AstroAgent chat system prompt (tone/policy tweaks without
	// recompiling); the grounding rules + knob menu stay fixed in code.
	LLMAssistPromptExtra string

	// Resource limits keep a heavy stack from freezing the host (Siril defaults to all cores and
	// 90% of RAM, which thrashes swap). MaxCPUs caps Siril's threads (setcpu); SirilMemRatio caps
	// the fraction of available RAM it may use (setmem); SirilNice lowers siril-cli OS priority;
	// MaxWorkers bounds concurrent jobs in the API worker pool (0 → runtime.NumCPU()/2).
	MaxCPUs       int
	SirilMemRatio float64
	SirilNice     int
	MaxWorkers    int

	// Plate-solving + SPCC (color calibration). Focal/pixel describe the rig (the FITS rarely
	// carries FOCALLEN); the SPCC names must match Siril's catalogs. Empty values fall back to
	// Siril defaults. PlateSolveCatalog empty → Siril chooses automatically.
	FocalLenMM     float64
	PixelSizeUm    float64
	SpccMonoSensor string
	SpccRFilter    string
	SpccGFilter    string
	SpccBFilter    string
	SpccWhiteRef   string
	// NightscapeOSCSensor is the SPCC OSC sensor name for the milkyway/nightscape path (the one-shot
	// camera, e.g. a DSLR). Empty (the default) disables SPCC for nightscapes — a phone sensor is rarely
	// in Siril's SPCC database — so the run uses the background-neutralization colour path instead.
	NightscapeOSCSensor string
	PlateSolveCatalog   string
	SirilCatalogDir     string // Siril's bundled object catalogues (for name→coords resolution)

	// Observing site + rig for the "tonight" visibility planner. Latitude/longitude default to Paris
	// so the page works out of the box; the web UI overrides them per-session (and persists locally).
	// ApertureMM and the sensor dimensions complete the optical setup; FocalLenMM/PixelSizeUm above are
	// reused for image scale and field of view.
	LatDeg     float64
	LonDeg     float64
	ElevationM float64
	Timezone   string // IANA name, e.g. "Europe/Paris"
	ApertureMM float64
	SensorWpx  int
	SensorHpx  int
	// EyepieceKit is the default visual-observing eyepiece set for the tonight planner's eyepiece mode,
	// encoded as "focalMM:apparentFOVdeg[:label]" items separated by commas. The web UI overrides it
	// per-session; an empty value disables the per-target eyepiece recommendation.
	EyepieceKit string
	// BarlowX is the default Barlow/amplifier factor applied to the focal length for the tonight planner
	// (image scale, FOV, f-ratio and eyepiece magnification). 1 means no Barlow.
	BarlowX float64

	// Cross-session reuse. Reuse pools prior light frames of the same target (to grow integration)
	// and prior raw bias/darks (for deeper, lower-noise masters). ReuseEnabled gates the whole
	// feature; ReuseConeDeg is the coordinate-match radius; ReuseDarkRecencyDays bounds how old a
	// dark may be (0 = unbounded); ReuseTempTolC is the dark temperature tolerance (°C).
	ReuseEnabled         bool
	ReuseConeDeg         float64
	ReuseDarkRecencyDays int
	ReuseTempTolC        float64

	// Live stacking. The source is polled every LivePollSec; a local file is only ingested once its
	// size has been stable for LiveStabilitySec (so a half-written FITS is never read). The (full
	// winsorized) re-stack is debounced: it runs after at least LiveRestackEvery new lights have been
	// folded in AND at least LiveMinIntervalSec has elapsed since the previous re-stack — the knobs that
	// trade live-preview freshness for CPU on long sessions.
	LivePollSec        int
	LiveStabilitySec   int
	LiveRestackEvery   int
	LiveMinIntervalSec int

	// S3 source for live stacking. Credentials are read from the environment ONLY (never the UI, never
	// logged). Endpoint empty → AWS S3 default for the region; non-empty targets any S3-compatible store
	// (MinIO/Wasabi/Backblaze B2/Cloudflare R2). Bucket/prefix are supplied per-job by the request.
	S3Endpoint        string
	S3Region          string
	S3AccessKeyID     string
	S3SecretAccessKey string
	S3UseSSL          bool

	// Light pollution. Per-site artificial sky brightness (VIIRS-derived) feeds the visibility scores
	// (a sky-glow factor parallel to the Moon) and the location-map overlay. Sourcing is hybrid and
	// soft-failing: the keyed online API (latest data) is primary, a locally-downloaded atlas
	// (`just update-light-pollution-data`) is the offline fallback, and SkyDefaultSQM is the last
	// resort — so a score always computes. The API key is read from the environment ONLY (never the
	// UI, never logged). The URLs are templates: {lat} {lon} {key} for the point API, {z} {x} {y}
	// {key} for the overlay tiles. SkyDefaultSQM is mag/arcsec² (higher = darker; 21.3 ≈ Bortle 4).
	LightPollutionAPIURL        string
	LightPollutionAPIKey        string
	LightPollutionTileURL       string
	LightPollutionAtlas         string // offline raster path; empty → <DataDir>/lightpollution/atlas.bin
	LightPollutionCacheTTLHours int
	SkyDefaultSQM               float64

	// Dark-sky finder + horizon openness. The finder grids a map area for low light pollution, then
	// scores the top candidates' horizon openness from terrain sampled via the keyless Open-Meteo
	// Elevation API. DarkSkyMaxCells caps the grid scan; the Horizon* knobs tune the terrain ring.
	ElevationAPIURL         string
	ElevationCacheTTLHours  int
	DarkSkyMaxCells         int       // cap on grid cells scanned per area search
	HorizonCandidates       int       // how many top dark candidates get horizon scoring
	HorizonAzimuths         int       // azimuth samples around the horizon
	HorizonRadiiM           []float64 // sample distances (m) along each azimuth
	HorizonOpenThresholdDeg float64   // an azimuth is "open" below this horizon elevation angle

	// Astronomy weather. Free + key-less by default: Open-Meteo (forecast + air quality), 7Timer! ASTRO
	// (seeing/transparency) and NOAA SWPC (Kp/aurora) feed the /tonight weather overlays + forecast
	// panel. Weather is a forecast timeline, so it does NOT change visibility scores — it is shown as map
	// layers + a panel + a badge. The grid is one Open-Meteo multi-point call over ±GridRadiusDeg around
	// the site (GridSize×GridSize cells), cached per bbox/site+hour. A meteoblue key is optional (server
	// env ONLY, never UI/logged) for a future paid satellite-map upgrade.
	WeatherOpenMeteoURL  string
	WeatherAirQualityURL string
	WeatherSevenTimerURL string
	WeatherSWPCURL       string
	WeatherGridRadiusDeg float64
	WeatherGridSize      int
	WeatherCacheTTLMin   int
	WeatherMeteoblueKey  string
}

// Load reads configuration from the environment, applying sensible defaults.
func Load() *Config {
	sirilBin := env("SIRIL_BIN", "/Applications/Siril.app/Contents/MacOS/siril-cli")
	catalogDir := env("ASTRO_SIRIL_CATALOG_DIR", "")
	if catalogDir == "" { // derive from the Siril app bundle (macOS host-engine)
		catalogDir = filepath.Clean(filepath.Join(filepath.Dir(sirilBin), "..", "Resources", "share", "siril", "catalogue"))
	}
	return &Config{
		DatabaseURL:    env("DATABASE_URL", "postgres://astro:astro@localhost:5432/astrostack?sslmode=disable"),
		APIAddr:        env("API_ADDR", ":8080"),
		LogLevel:       env("LOG_LEVEL", "info"),
		DataDir:        env("ASTRO_DATA_DIR", "./data"),
		WorkDir:        env("ASTRO_WORK_DIR", "./work"),
		OutputDir:      env("ASTRO_OUTPUT_DIR", "./output"),
		LibraryDir:     env("ASTRO_LIBRARY_DIR", "./library"),
		PreviewMaxEdge: envInt("PREVIEW_MAX_EDGE", 1500),
		SirilBin:       sirilBin,
		GimpBin:        env("GIMP_BIN", "/Applications/GIMP.app/Contents/MacOS/gimp-console-2.10"),
		GimpHost:       env("GIMP_HOST", "127.0.0.1"),
		GimpPort:       envInt("GIMP_PORT", 10008),
		FfmpegBin:      env("FFMPEG_BIN", "ffmpeg"),
		// GraXpert/StarNet are resolved via PATH by default (pip/pipx installs land in PATH as
		// `graxpert`); the old default pointed at a GraXpert.app that pip installs don't create, so AI
		// background extraction was silently skipped. exec.LookPath accepts a bare name or an abs path.
		GraxpertBin: env("GRAXPERT_BIN", "graxpert"),
		StarnetBin:  env("STARNET_BIN", "starnet++"),

		LLMBaseURL:           env("ASTRO_LLM_URL", "http://127.0.0.1:1234/v1"),
		LLMModel:             env("ASTRO_LLM_MODEL", ""),
		LLMImageFormat:       env("ASTRO_LLM_IMAGE_FORMAT", "openai"),
		LLMTimeout:           time.Duration(envInt("ASTRO_LLM_TIMEOUT_SEC", 3600)) * time.Second,
		LLMAssistPromptExtra: env("ASTRO_LLM_ASSIST_PROMPT_EXTRA", ""),

		MaxCPUs:       envInt("ASTRO_MAX_CPUS", 10),
		SirilMemRatio: envFloat("ASTRO_SIRIL_MEM_RATIO", 0.5),
		SirilNice:     envInt("ASTRO_SIRIL_NICE", 10),
		MaxWorkers:    envInt("ASTRO_MAX_WORKERS", 0),

		FocalLenMM:  envFloat("ASTRO_FOCAL_MM", 740), // Takahashi FC-100 DF native
		PixelSizeUm: envFloat("ASTRO_PIXEL_UM", 3.8), // ASI1600MM Pro
		// SPCC names MUST match Siril's spcc-database exactly (case/spacing). The ASI1600MM Pro's
		// sensor entry is "ZWO ASI1600MM" (no " Pro" — that name does not exist in the DB and makes
		// SPCC abort, silently falling back to green-only neutralization → a brown sky). For a mono
		// sensor SPCC also needs the per-channel filter names; default to ZWO's CMOS-optimized LRGB.
		SpccMonoSensor:      env("ASTRO_SPCC_SENSOR", "ZWO ASI1600MM"),
		SpccRFilter:         env("ASTRO_SPCC_RFILTER", "ZWO Optimized for CMOS Red"),
		SpccGFilter:         env("ASTRO_SPCC_GFILTER", "ZWO Optimized for CMOS Green"),
		SpccBFilter:         env("ASTRO_SPCC_BFILTER", "ZWO Optimized for CMOS Blue"),
		SpccWhiteRef:        env("ASTRO_SPCC_WHITEREF", "Average Spiral Galaxy"),
		NightscapeOSCSensor: env("ASTRO_NIGHTSCAPE_OSC_SENSOR", ""),
		PlateSolveCatalog:   env("ASTRO_PLATESOLVE_CATALOG", ""),
		SirilCatalogDir:     catalogDir,

		LatDeg:     envFloat("ASTRO_LAT", 48.8566), // Paris by default; overridable in the UI
		LonDeg:     envFloat("ASTRO_LON", 2.3522),
		ElevationM: envFloat("ASTRO_ELEVATION_M", 0),
		Timezone:   env("ASTRO_TIMEZONE", "Europe/Paris"),
		ApertureMM: envFloat("ASTRO_APERTURE_MM", 100), // Takahashi FC-100 DF
		SensorWpx:  envInt("ASTRO_SENSOR_W", 4656),     // ASI1600MM Pro
		SensorHpx:  envInt("ASTRO_SENSOR_H", 3520),
		// A sane visual kit for the 740 mm f/7.4 FC-100 (exit pupils 4.1 → 0.8 mm).
		EyepieceKit: env("ASTRO_EYEPIECES", "30:68:30mm,18:65:18mm,10:60:10mm,6:60:6mm"),
		BarlowX:     envFloat("ASTRO_BARLOW", 1),

		ReuseEnabled:         envBool("ASTRO_REUSE_ENABLED", true),
		ReuseConeDeg:         envFloat("ASTRO_REUSE_CONE_DEG", 0.5),
		ReuseDarkRecencyDays: envInt("ASTRO_REUSE_DARK_RECENCY_DAYS", 0),
		ReuseTempTolC:        envFloat("ASTRO_REUSE_TEMP_TOL_C", 5.0),

		LivePollSec:        envInt("ASTRO_LIVESTACK_POLL_SEC", 3),
		LiveStabilitySec:   envInt("ASTRO_LIVESTACK_STABILITY_SEC", 2),
		LiveRestackEvery:   envInt("ASTRO_LIVESTACK_RESTACK_EVERY", 1),
		LiveMinIntervalSec: envInt("ASTRO_LIVESTACK_MIN_INTERVAL_SEC", 0),

		S3Endpoint:        env("ASTRO_S3_ENDPOINT", ""),
		S3Region:          env("ASTRO_S3_REGION", "us-east-1"),
		S3AccessKeyID:     env("ASTRO_S3_ACCESS_KEY_ID", ""),
		S3SecretAccessKey: env("ASTRO_S3_SECRET_ACCESS_KEY", ""),
		S3UseSSL:          envBool("ASTRO_S3_USE_SSL", true),

		LightPollutionAPIURL: env("ASTRO_LIGHTPOLLUTION_API_URL", ""),
		LightPollutionAPIKey: env("ASTRO_LIGHTPOLLUTION_API_KEY", ""),
		// Default to NASA GIBS VIIRS Black Marble night-lights — keyless, no download. It serves the map
		// overlay AND, sampled per-site, the sky-brightness estimate; both work out of the box. Override
		// or blank it to disable. {z}/{y}/{x} matches GIBS's GoogleMapsCompatible row/col order.
		LightPollutionTileURL: env("ASTRO_LIGHTPOLLUTION_TILE_URL",
			"https://gibs.earthdata.nasa.gov/wmts/epsg3857/best/VIIRS_Black_Marble/default/2016-01-01/GoogleMapsCompatible_Level8/{z}/{y}/{x}.png"),
		LightPollutionAtlas:         env("ASTRO_LIGHTPOLLUTION_ATLAS", ""),
		LightPollutionCacheTTLHours: envInt("ASTRO_LIGHTPOLLUTION_CACHE_TTL", 720),
		SkyDefaultSQM:               envFloat("ASTRO_SKY_DEFAULT_SQM", 21.3),

		ElevationAPIURL:         env("ASTRO_ELEVATION_API_URL", "https://api.open-meteo.com/v1/elevation"),
		ElevationCacheTTLHours:  envInt("ASTRO_ELEVATION_CACHE_TTL", 720),
		DarkSkyMaxCells:         envInt("ASTRO_DARKSKY_MAX_CELLS", 4000),
		HorizonCandidates:       envInt("ASTRO_HORIZON_CANDIDATES", 10),
		HorizonAzimuths:         envInt("ASTRO_HORIZON_AZIMUTHS", 12),
		HorizonRadiiM:           envFloatList("ASTRO_HORIZON_RADII_M", []float64{1000, 2500}),
		HorizonOpenThresholdDeg: envFloat("ASTRO_HORIZON_OPEN_THRESHOLD_DEG", 3),

		WeatherOpenMeteoURL:  env("ASTRO_WEATHER_OPENMETEO_URL", "https://api.open-meteo.com/v1/forecast"),
		WeatherAirQualityURL: env("ASTRO_WEATHER_AIRQUALITY_URL", "https://air-quality-api.open-meteo.com/v1/air-quality"),
		WeatherSevenTimerURL: env("ASTRO_WEATHER_SEVENTIMER_URL", "https://www.7timer.info/bin/api.pl"),
		WeatherSWPCURL:       env("ASTRO_WEATHER_SWPC_URL", "https://services.swpc.noaa.gov/products/noaa-planetary-k-index.json"),
		WeatherGridRadiusDeg: envFloat("ASTRO_WEATHER_GRID_RADIUS_DEG", 4),
		// 22×22 over the 8° box ≈ 3.1 cells/° — sharp enough to read as a weather map (was 16). Grid
		// coords are trimmed to 3 decimals (see joinFloats) so the single bulk Open-Meteo GET stays ~7KB.
		WeatherGridSize:     envInt("ASTRO_WEATHER_GRID_SIZE", 22),
		WeatherCacheTTLMin:  envInt("ASTRO_WEATHER_CACHE_TTL_MIN", 30),
		WeatherMeteoblueKey: env("ASTRO_WEATHER_METEOBLUE_KEY", ""),
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

// envFloatList parses a comma-separated list of floats (e.g. "1000,2500"), falling back to def when the
// var is unset, empty, or malformed.
func envFloatList(key string, def []float64) []float64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	var out []float64
	for _, part := range strings.Split(v, ",") {
		f, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err != nil {
			return def
		}
		out = append(out, f)
	}
	if len(out) == 0 {
		return def
	}
	return out
}

// DarkSinceMs is the epoch-ms cutoff below which darks are too old to reuse, or 0 (unbounded) when
// ReuseDarkRecencyDays is 0.
func (c *Config) DarkSinceMs() int64 {
	if c.ReuseDarkRecencyDays <= 0 {
		return 0
	}
	return time.Now().AddDate(0, 0, -c.ReuseDarkRecencyDays).UnixMilli()
}

// Location resolves the configured observing timezone, falling back to UTC if it cannot be loaded.
func (c *Config) Location() *time.Location {
	if loc, err := time.LoadLocation(c.Timezone); err == nil {
		return loc
	}
	return time.UTC
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
