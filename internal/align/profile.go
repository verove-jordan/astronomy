package align

// Profile captures how a particular mount / alignment routine wants its calibration stars chosen: the
// usable altitude band, the star-count range, and brand rules (all stars on one side of the meridian,
// a keep-away zone around the meridian, an alt-az bias away from the zenith). Profiles are the brand
// presets the user picks on the /goto page; the selection algorithm reads their constraints.
type Profile struct {
	Key       string `json:"key"`
	Label     string `json:"label"`
	MountType string `json:"mount_type"` // "eq" | "altaz"

	MinAltDeg float64 `json:"min_alt_deg"`
	MaxAltDeg float64 `json:"max_alt_deg"`

	DefaultStars int `json:"default_stars"`
	MinStars     int `json:"min_stars"`
	MaxStars     int `json:"max_stars"`

	MagLimit         float64 `json:"mag_limit"`          // faintest eligible star (full dense catalog ≈ 3.5)
	SameMeridianSide bool    `json:"same_meridian_side"` // brand rule: all align stars on one side
	AvoidMeridianDeg float64 `json:"avoid_meridian_deg"` // reject |hour angle| < this (Celestron); 0 = off
	ZenithBias       float64 `json:"zenith_bias"`        // push selection away from the zenith (alt-az)

	// Hand-controller coherence + two-phase routines (see starlists/README.md). StarList names an
	// embedded hand-controller star list — only stars on it are suggested, labeled as the HC shows
	// them. AlignStars > 0 splits the plan into that many alignment stars followed by calibration
	// stars; CalibOppositeSide applies the brand rule that calibration stars sit across the meridian
	// from the alignment pair (models cone error).
	StarList          string `json:"star_list,omitempty"`
	AlignStars        int    `json:"align_stars,omitempty"`
	CalibOppositeSide bool   `json:"calib_opposite_side,omitempty"`

	Note string `json:"note"`
}

// profiles is the registry of supported mounts/routines. The brightness term in scoring keeps the
// brightest, easiest-to-find stars first; MagLimit only widens the candidate pool so skips always
// have a replacement.
var profiles = []Profile{
	{
		Key: "eq-generic", Label: "Equatorial (generic)", MountType: "eq",
		MinAltDeg: 20, MaxAltDeg: 75, DefaultStars: 3, MinStars: 2, MaxStars: 6, MagLimit: 3.5,
		Note: "German-equatorial mounts. A wide, bright spread across the sky for a strong pointing model.",
	},
	{
		Key: "synscan-eq", Label: "SkyWatcher SynScan (EQ)", MountType: "eq",
		MinAltDeg: 20, MaxAltDeg: 72, DefaultStars: 3, MinStars: 1, MaxStars: 3, MagLimit: 3.5,
		SameMeridianSide: true, StarList: "synscan",
		Note: "SynScan 1/2/3-star: bright stars on the same side of the meridian, well separated.",
	},
	{
		Key: "celestron-eq", Label: "Celestron NexStar / AVX (EQ)", MountType: "eq",
		MinAltDeg: 20, MaxAltDeg: 72, DefaultStars: 6, MinStars: 2, MaxStars: 6, MagLimit: 3.5,
		SameMeridianSide: true, AvoidMeridianDeg: 10,
		StarList: "celestron", AlignStars: 2, CalibOppositeSide: true,
		Note: "Two align stars on one side of the meridian, then up to four calibration stars on the opposite side (models cone error).",
	},
	{
		Key: "altaz-generic", Label: "Alt-Az (generic)", MountType: "altaz",
		MinAltDeg: 20, MaxAltDeg: 65, DefaultStars: 3, MinStars: 2, MaxStars: 6, MagLimit: 3.5,
		ZenithBias: 0.5,
		Note:       "Alt-azimuth mounts. A wide spread in azimuth, away from the zenith and the horizon.",
	},
	{
		Key: "synscan-altaz", Label: "SkyWatcher SynScan (Alt-Az)", MountType: "altaz",
		MinAltDeg: 20, MaxAltDeg: 65, DefaultStars: 2, MinStars: 1, MaxStars: 3, MagLimit: 3.5,
		ZenithBias: 0.5, StarList: "synscan",
		Note: "Brightest-star / 2-star: two bright stars wide apart in azimuth at moderate altitude.",
	},
	{
		Key: "celestron-altaz", Label: "Celestron SkyAlign (Alt-Az)", MountType: "altaz",
		MinAltDeg: 15, MaxAltDeg: 65, DefaultStars: 3, MinStars: 3, MaxStars: 3, MagLimit: 3.5,
		ZenithBias: 0.5,
		Note:       "SkyAlign: three bright stars spread widely across the sky, in any direction.",
	},
}

// Lookup returns the profile for key, or the default profile when key is empty or unknown.
func Lookup(key string) Profile {
	for _, p := range profiles {
		if p.Key == key {
			return p
		}
	}
	return Default()
}

// Default is the profile used when none is specified (generic equatorial).
func Default() Profile { return profiles[0] }

// Profiles returns a copy of the registry (for listing the presets in the UI / API).
func Profiles() []Profile { return append([]Profile(nil), profiles...) }

// ClampCount bounds a requested star count to the profile's [MinStars, MaxStars]; a non-positive
// request falls back to DefaultStars.
func (p Profile) ClampCount(n int) int {
	if n <= 0 {
		n = p.DefaultStars
	}
	if n < p.MinStars {
		return p.MinStars
	}
	if n > p.MaxStars {
		return p.MaxStars
	}
	return n
}
