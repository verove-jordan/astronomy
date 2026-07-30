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
	KeepWork   bool   // keep run scratch after terminal jobs (debugging) — disables the work sweep
	OutputDir  string // where final stacks and reports are written
	LibraryDir string // persistent master-calibration library

	// BrowseRoots are EXTRA absolute roots the UI may browse for external drives, on top of the platform
	// removable-media defaults (macOS /Volumes; Linux /media, /mnt, /run/media). Colon- or comma-separated
	// in ASTRO_BROWSE_ROOTS. Every /api/local/* handler confines its paths to these roots + the defaults.
	BrowseRoots []string

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
	GraxpertBin   string // GraXpert: AI background-gradient extraction + denoise
	GraxpertURL   string // optional host GraXpert HTTP service (cmd/graxpert-host); empty → exec GraxpertBin locally
	GraxpertGPU   bool   // pass -gpu true (helps background-extraction on Apple Silicon; denoise stays CPU — its model is CoreML-incompatible)
	GraxpertBatch int    // GraXpert denoise -batch_size (tiles denoised in parallel; 0 → GraXpert's default of 4)
	// DenoiseScale (0,1) runs the joint AI colour denoise on a downscaled copy and transfers only
	// the chroma back (luminance untouched, ~scale² of the cost — best for LRGB where L carries
	// detail). 1.0 (the default) keeps the full-resolution pass byte-identical.
	DenoiseScale float64
	// ChannelParallel stacks up to N deep-sky channels concurrently (each Siril instance gets an
	// equal share of the CPU/memory budget). 1 (the default) keeps the proven serial loop.
	ChannelParallel int
	StarnetBin      string // StarNet++ v2: star removal (for star-reduced finishing)

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
	FocalLenMM  float64
	PixelSizeUm float64

	// MountWormPeriodSec is the RA worm's revolution time, the period the tracking analysis folds
	// on. 478 s is Celestron's figure for the Advanced VX; other mounts differ, and the fit searches
	// around this value rather than trusting it.
	MountWormPeriodSec float64
	// TrackingSolveEveryNth solves one light in N to measure tracking. 1 is affordable at minute-long
	// subs; raise it for short subs so the solves cannot fall behind the capture cadence.
	TrackingSolveEveryNth int
	SpccMonoSensor        string
	SpccRFilter           string
	SpccGFilter           string
	SpccBFilter           string
	SpccWhiteRef          string
	// NightscapeOSCSensor is the SPCC OSC sensor name for the milkyway/nightscape path (the one-shot
	// camera, e.g. a DSLR). Empty (the default) disables SPCC for nightscapes — a phone sensor is rarely
	// in Siril's SPCC database — so the run uses the background-neutralization colour path instead.
	NightscapeOSCSensor string
	PlateSolveCatalog   string
	SirilCatalogDir     string // Siril's bundled object catalogues (for name→coords resolution)

	// DeviceAddr is where the device server (camera / filter wheel / mount) listens, and where the
	// engine proxies /api/device/* to. It runs as its own process so an engine restart — air does
	// one on every source save — cannot drop a USB connection mid-sequence.
	DeviceAddr string
	// Local Gaia DR3 catalogues (downloaded once via `just download-catalogues[-spcc]`) make
	// plate-solving and SPCC work fully offline. GaiaAstroCat is the astrometric extract FILE;
	// GaiaXpsampDir is the DIRECTORY holding the xp_sampled chunk files. Use the LocalGaia*()
	// accessors, which return them only when the files are actually present. LocalAsnet switches
	// solving to a local astrometry.net install instead. SpccCatalog forces the SPCC source
	// ("gaia" online / "localgaia"); empty lets Siril prefer local when installed.
	GaiaAstroCat  string
	GaiaXpsampDir string
	LocalAsnet    bool
	SpccCatalog   string

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
	// S3Concurrency bounds how many files a transfer uploads/downloads in PARALLEL (0 → the transfer
	// engine's default of 6). Raise it to saturate a fat uplink; lower it for a slow source drive whose
	// parallel reads would thrash. Env ASTRO_S3_CONCURRENCY.
	S3Concurrency int
	// S3LowDisk enables the STAGED low-disk mode for full-S3 deep-sky/nebula processing runs: inputs are
	// scanned remotely (ranged FITS-header reads) and downloaded/verified-freed one frame-type/channel wave
	// at a time, so peak local disk ≈ one channel's frames instead of the whole dataset. The server default;
	// a run can override it (RunRequest.LowDisk). Env ASTRO_S3_LOW_DISK (default true).
	S3LowDisk bool

	// EncryptionKey / SecretKeyFile secure the UI-managed S3 connections at rest (their secret access keys
	// are AES-256-GCM encrypted in the DB). EncryptionKey (base64 std, 32 bytes) is the master key; when
	// empty a random key is generated once and persisted to SecretKeyFile (default under the user config
	// dir — deliberately OUTSIDE the data/library/output roots so it is never swept into a backup).
	EncryptionKey string
	SecretKeyFile string

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
	LightPollutionAtlas         string // offline raster path; empty → <WorkDir>/lightpollution/atlas.bin
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

	// Tree/forest canopy horizon. When a canopy source is active (an ETH canopy-height atlas installed, or
	// the keyless tree-cover tiles), the dark-sky finder samples canopy height along a NEAR-field ring and
	// adds it to the terrain elevation, so a site hemmed in by a forest scores its low horizon correctly (a
	// 20 m treeline 30 m away blocks ~34° of sky). It is opt-in: with no canopy source the horizon is
	// byte-identical to the terrain-only result. CanopyTileURL is a {z}/{x}/{y} tree-cover-% raster;
	// CanopyAssumedHeightM is the height assumed where a tile/land-cover cell only reports forest presence.
	CanopyAtlas          string  // offline canopy-height raster; empty → <WorkDir>/canopy/atlas.bin
	CanopyTileURL        string  // keyless tree-cover-% XYZ tiles ({z}/{x}/{y}); empty → disabled
	CanopyAssumedHeightM float64 // canopy height (m) assumed for a "forested" tile/land-cover cell
	CanopyTreeCoverPct   float64 // a tile pixel counts as forest at/above this tree-cover %
	CanopyCacheTTLHours  int
	CanopySourceURL      string  // ETH 3° canopy-height COG URL template ({tile} → e.g. N45E003) for the in-app "download canopy for this area" build
	CanopyBuildResDeg    float64 // target resolution (deg) of a downloaded canopy atlas (~0.0008 ≈ 90 m)

	// Canopy-mode horizon ring (used ONLY when a canopy source is active; terrain-only keeps HorizonRadiiM /
	// HorizonAzimuths above). Trees are a near-field effect, so the ring reaches from tens of metres out.
	// HorizonEyeHeightM is the observer's eye/telescope height, subtracted from each obstruction angle.
	HorizonCanopyRadiiM   []float64 // sample distances (m) along each azimuth in canopy mode
	HorizonCanopyAzimuths int       // azimuth samples in canopy mode (finer than terrain-only)
	HorizonEyeHeightM     float64   // observer eye height (m) subtracted from obstruction angles

	// Dark-site score weights. The place score blends darkness and horizon openness. DarkSkyDarkWeight is
	// the darkness share (openness gets the remainder). DarkSkySouthWeight blends a south-weighted openness
	// into the openness term (the low southern horizon matters most for N-hemisphere deep-sky).
	// DarkSkyMaxSouthBlockDeg (0 = disabled) is a hard gate on southern obstruction. Defaults reproduce
	// today's 0.6 darkness / 0.4 openness score.
	DarkSkyDarkWeight       float64
	DarkSkySouthWeight      float64
	DarkSkyMaxSouthBlockDeg float64

	// Driving distance for the dark-site finder. Road distance + time from the observer to each candidate,
	// via an OSRM routing server (keyless public demo by default — rate-limited, best-effort). It is
	// display-only and soft-failing: on any error the finder shows the straight-line distance instead.
	// Blank RoutingURL to disable.
	RoutingURL           string
	RoutingCacheTTLHours int

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
	// WeatherOpenMeteoModels is Open-Meteo's optional `models=` selector: empty = best_match (the API
	// auto-picks the finest regional model, e.g. AROME over France). Set EXACTLY ONE model to pin it
	// (a comma list multiplies the per-location call weight against the free-tier quota).
	WeatherOpenMeteoModels string
	WeatherGridRadiusDeg   float64
	WeatherGridSize        int
	WeatherCacheTTLMin     int
	WeatherMeteoblueKey    string
}

// Load reads configuration from the environment, applying sensible defaults.
func Load() *Config {
	sirilBin := env("SIRIL_BIN", "/Applications/Siril.app/Contents/MacOS/siril-cli")
	catalogDir := env("ASTRO_SIRIL_CATALOG_DIR", "")
	if catalogDir == "" { // derive from the Siril app bundle (macOS host-engine)
		catalogDir = filepath.Clean(filepath.Join(filepath.Dir(sirilBin), "..", "Resources", "share", "siril", "catalogue"))
	}
	libraryDir := env("ASTRO_LIBRARY_DIR", "./library")
	return &Config{
		DatabaseURL:    env("DATABASE_URL", "postgres://astro:astro@localhost:5432/astrostack?sslmode=disable"),
		APIAddr:        env("API_ADDR", ":8080"),
		LogLevel:       env("LOG_LEVEL", "info"),
		DataDir:        env("ASTRO_DATA_DIR", "./data"),
		WorkDir:        env("ASTRO_WORK_DIR", "./work"),
		KeepWork:       envBool("ASTRO_KEEP_WORK", false),
		OutputDir:      env("ASTRO_OUTPUT_DIR", "./output"),
		LibraryDir:     libraryDir,
		BrowseRoots:    envStrList("ASTRO_BROWSE_ROOTS"),
		PreviewMaxEdge: envInt("PREVIEW_MAX_EDGE", 1500),
		SirilBin:       sirilBin,
		GimpBin:        env("GIMP_BIN", "/Applications/GIMP.app/Contents/MacOS/gimp-console-2.10"),
		GimpHost:       env("GIMP_HOST", "127.0.0.1"),
		GimpPort:       envInt("GIMP_PORT", 10008),
		FfmpegBin:      env("FFMPEG_BIN", "ffmpeg"),
		// GraXpert/StarNet are resolved via PATH by default (pip/pipx installs land in PATH as
		// `graxpert`); the old default pointed at a GraXpert.app that pip installs don't create, so AI
		// background extraction was silently skipped. exec.LookPath accepts a bare name or an abs path.
		GraxpertBin:     env("GRAXPERT_BIN", "graxpert"),
		GraxpertURL:     env("ASTRO_GRAXPERT_URL", ""),
		GraxpertGPU:     envBool("ASTRO_GRAXPERT_GPU", false),
		GraxpertBatch:   envInt("ASTRO_GRAXPERT_BATCH", 0),
		DenoiseScale:    envFloat("ASTRO_DENOISE_SCALE", 1.0),
		ChannelParallel: envInt("ASTRO_CHANNEL_PARALLEL", 1),
		StarnetBin:      env("STARNET_BIN", "starnet++"),

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

		MountWormPeriodSec:    envFloat("ASTRO_WORM_PERIOD_SEC", 478), // Celestron Advanced VX
		TrackingSolveEveryNth: envInt("ASTRO_TRACKING_SOLVE_EVERY", 1),
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
		DeviceAddr:          env("ASTRO_DEVICE_ADDR", "127.0.0.1:8084"),
		GaiaAstroCat:        env("ASTRO_GAIA_ASTRO_CAT", filepath.Join(libraryDir, "catalogues", "siril_cat_healpix8_astro.dat")),
		GaiaXpsampDir:       env("ASTRO_GAIA_XPSAMP_DIR", filepath.Join(libraryDir, "catalogues")),
		LocalAsnet:          envBool("ASTRO_LOCAL_ASNET", false),
		SpccCatalog:         env("ASTRO_SPCC_CATALOG", ""),

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
		S3Concurrency:     envInt("ASTRO_S3_CONCURRENCY", 0),
		S3LowDisk:         envBool("ASTRO_S3_LOW_DISK", true),
		EncryptionKey:     env("ASTRO_ENCRYPTION_KEY", ""),
		SecretKeyFile:     env("ASTRO_SECRET_KEY_FILE", ""),

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

		CanopyAtlas:          env("ASTRO_CANOPY_ATLAS", ""),
		CanopyTileURL:        env("ASTRO_CANOPY_TILE_URL", ""),
		CanopyAssumedHeightM: envFloat("ASTRO_CANOPY_ASSUMED_HEIGHT_M", 18),
		CanopyTreeCoverPct:   envFloat("ASTRO_CANOPY_TREECOVER_PCT", 30),
		CanopyCacheTTLHours:  envInt("ASTRO_CANOPY_CACHE_TTL", 720),
		// ETH Global Canopy Height 2020 (Lang et al., 10 m, CC BY 4.0) 3° COG tiles — public, range-readable,
		// so gdal's /vsicurl/ downloads only the windows a build needs. {tile} = SW-corner token (e.g. N45E003).
		CanopySourceURL: env("ASTRO_CANOPY_SOURCE_URL",
			"https://libdrive.ethz.ch/index.php/s/cO8or7iOe5dT2Rt/download?path=%2F3deg_cogs&files=ETH_GlobalCanopyHeight_10m_2020_{tile}_Map.tif"),
		CanopyBuildResDeg: envFloat("ASTRO_CANOPY_BUILD_RES_DEG", 0.0008),

		HorizonCanopyRadiiM:   envFloatList("ASTRO_HORIZON_CANOPY_RADII_M", []float64{30, 60, 120, 250, 500, 1000, 2500}),
		HorizonCanopyAzimuths: envInt("ASTRO_HORIZON_CANOPY_AZIMUTHS", 24),
		HorizonEyeHeightM:     envFloat("ASTRO_HORIZON_EYE_HEIGHT_M", 1.6),

		DarkSkyDarkWeight:       envFloat("ASTRO_DARKSKY_DARK_WEIGHT", 0.6),
		DarkSkySouthWeight:      envFloat("ASTRO_DARKSKY_SOUTH_WEIGHT", 0),
		DarkSkyMaxSouthBlockDeg: envFloat("ASTRO_DARKSKY_MAX_SOUTH_BLOCK", 0),

		RoutingURL:           env("ASTRO_ROUTING_URL", "https://router.project-osrm.org"),
		RoutingCacheTTLHours: envInt("ASTRO_ROUTING_CACHE_TTL", 720),

		WeatherOpenMeteoURL:    env("ASTRO_WEATHER_OPENMETEO_URL", "https://api.open-meteo.com/v1/forecast"),
		WeatherOpenMeteoModels: env("ASTRO_WEATHER_OPENMETEO_MODELS", ""),
		WeatherAirQualityURL:   env("ASTRO_WEATHER_AIRQUALITY_URL", "https://air-quality-api.open-meteo.com/v1/air-quality"),
		WeatherSevenTimerURL:   env("ASTRO_WEATHER_SEVENTIMER_URL", "https://www.7timer.info/bin/api.pl"),
		WeatherSWPCURL:         env("ASTRO_WEATHER_SWPC_URL", "https://services.swpc.noaa.gov/products/noaa-planetary-k-index.json"),
		WeatherGridRadiusDeg:   envFloat("ASTRO_WEATHER_GRID_RADIUS_DEG", 4),
		// 32×32 = 1024 pts over the default 8° box ≈ 0.25°/cell ≈ 27 km — about the forecast model's own
		// resolution, so the overlay is as sharp as the data allows (was 22). Fetched as 3 chunked
		// Open-Meteo GETs of ≤400 coords each, trimmed to 3 decimals (see fetchOpenMeteoGrid/joinFloats).
		WeatherGridSize:     envInt("ASTRO_WEATHER_GRID_SIZE", 32),
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
// envStrList splits key on ":" or "," into a trimmed, non-empty string slice (nil when unset) — used for
// path lists like ASTRO_BROWSE_ROOTS.
func envStrList(key string) []string {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	var out []string
	for _, part := range strings.FieldsFunc(v, func(r rune) bool { return r == ':' || r == ',' }) {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

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

// LocalGaiaAstroCat returns the local Gaia astrometric catalogue path when the file is actually
// present, else "" — callers wire it into siril.SolveOptions only when solving can really use it
// (download once with `just download-catalogues`).
func (c *Config) LocalGaiaAstroCat() string {
	if c.GaiaAstroCat == "" {
		return ""
	}
	if st, err := os.Stat(c.GaiaAstroCat); err != nil || st.IsDir() {
		return ""
	}
	return c.GaiaAstroCat
}

// LocalGaiaXpsampDir returns the xp_sampled chunk directory when it holds at least one chunk file
// (`just download-catalogues-spcc`), else "". Siril scans the directory itself, so a partial chunk
// set covering only the shot sky regions is fine.
func (c *Config) LocalGaiaXpsampDir() string {
	if c.GaiaXpsampDir == "" {
		return ""
	}
	matches, err := filepath.Glob(filepath.Join(c.GaiaXpsampDir, "siril_cat*_xpsamp_*.dat"))
	if err != nil || len(matches) == 0 {
		return ""
	}
	return c.GaiaXpsampDir
}
