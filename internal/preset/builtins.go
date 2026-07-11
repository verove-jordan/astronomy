package preset

// Builtins is the curated "best params per situation" catalog. Each entry is a starting point the user can
// apply with one click and then fine-tune. Slugs are stable (they key the i18n labels + the anti-drift
// test); the numeric recipes are deltas on top of each mode's default preset (mode.For), expressed with
// the same knob keys the Advanced box and the supervisor use, so they can never fall out of range without
// the catalog test catching it. supervise stays off everywhere — it is slow and needs the local VLM, so
// it is opt-in per run, not baked into a preset.
func Builtins() []Item {
	out := make([]Item, 0, 16)
	out = append(out, deepskyBuiltins()...)
	out = append(out, nebulaBuiltins()...)
	out = append(out, narrowbandBuiltins()...)
	out = append(out, solarBuiltins()...)
	out = append(out, cometBuiltins()...)
	out = append(out, milkywayBuiltins()...)
	return out
}

// deepskyBuiltins — broadband deep-sky targets (galaxies, clusters, reflection nebulae): mode deepsky,
// natural palette, colour calibration + denoise on.
func deepskyBuiltins() []Item {
	return []Item{
		builtin("galaxy-lrgb", CategoryDeepsky, Payload{
			Mode: "deepsky", Format: "image", Palette: "natural",
			ColorCalibration: boolPtr(true), Denoise: boolPtr(true),
			// Bright core + faint outer arms: a touch more highlight headroom so the core keeps colour,
			// gentle star reduction, moderate luminance denoise for the faint disk.
			Params: mustParams(map[string]any{
				"stretch_headroom": 0.92, "star_reduce": 0.1, "denoise_lum": 0.4, "saturation": 0.14,
			}),
		}),
		builtin("galaxy-faint", CategoryDeepsky, Payload{
			Mode: "deepsky", Format: "image", Palette: "natural",
			ColorCalibration: boolPtr(true), Denoise: boolPtr(true),
			// Low-SNR faint galaxy: stronger denoise, AI background extraction on the combined masters,
			// a little more star reduction to keep the faint structure readable.
			Params: mustParams(map[string]any{
				"denoise_lum": 0.65, "denoise_chroma": 0.9, "background_ai": true,
				"combined_background_ai": true, "star_reduce": 0.15, "stretch_headroom": 0.88,
			}),
		}),
		builtin("star-cluster", CategoryDeepsky, Payload{
			Mode: "deepsky", Format: "image", Palette: "natural",
			ColorCalibration: boolPtr(true), Denoise: boolPtr(true),
			// Globular / open cluster: never reduce stars; tame the colour discs/mottle around bright
			// stars (star desaturation + chroma blur) while keeping full luminance — the cluster-finish fix.
			Params: mustParams(map[string]any{
				"star_reduce": 0, "star_desat": 0.6, "chroma_blur": 4, "lum_opacity": 1.0,
			}),
		}),
		builtin("reflection-nebula", CategoryDeepsky, Payload{
			Mode: "deepsky", Format: "image", Palette: "natural",
			ColorCalibration: boolPtr(true), Denoise: boolPtr(true),
			// Blue broadband reflection nebula: no Ha screen, a little more saturation to hold the blues,
			// gentle star reduction.
			Params: mustParams(map[string]any{
				"ha_screen": 0, "saturation": 0.16, "denoise_lum": 0.45, "star_reduce": 0.1,
			}),
		}),
	}
}

// nebulaBuiltins — broadband + Ha emission nebulae: mode nebula (Ha-forward), colour calibration on.
func nebulaBuiltins() []Item {
	return []Item{
		builtin("emission-hargb", CategoryNebula, Payload{
			Mode: "nebula", Format: "image", Palette: "hargb",
			ColorCalibration: boolPtr(true), Denoise: boolPtr(true), HaExcludeStars: boolPtr(true),
			// Ha blended into RGB: a strong Ha screen with a raised black point to keep the background
			// clean, moderate star reduction so the nebula reads over the star field.
			Params: mustParams(map[string]any{
				"ha_screen": 0.55, "ha_black_point": 0.12, "star_reduce": 0.4, "saturation": 0.15,
			}),
		}),
		builtin("emission-broadband", CategoryNebula, Payload{
			Mode: "nebula", Format: "image", Palette: "natural",
			ColorCalibration: boolPtr(true), Denoise: boolPtr(true), HaExcludeStars: boolPtr(true),
			// Broadband emission nebula (no dedicated Ha stack): a mild Ha screen, light star reduction.
			Params: mustParams(map[string]any{
				"ha_screen": 0.35, "star_reduce": 0.3, "saturation": 0.14,
			}),
		}),
		builtin("planetary-nebula", CategoryNebula, Payload{
			Mode: "nebula", Format: "image", Palette: "natural",
			ColorCalibration: boolPtr(true), Denoise: boolPtr(true),
			// Small, bright planetary nebula: keep every star, extra saturation for the shell colours, a
			// gentler stretch so the bright core does not blow out.
			Params: mustParams(map[string]any{
				"star_reduce": 0, "saturation": 0.18, "stretch_headroom": 0.9, "ha_screen": 0.45,
			}),
		}),
	}
}

// narrowbandBuiltins — palette-mapped narrowband (SHO/HOO/Foraxx): mode nebula with SPCC OFF (narrowband
// palettes do their own colour mapping) and the magenta star haloes tamed.
func narrowbandBuiltins() []Item {
	return []Item{
		builtin("narrowband-sho", CategoryNarrowband, Payload{
			Mode: "nebula", Format: "image", Palette: "sho",
			ColorCalibration: boolPtr(false), Denoise: boolPtr(true), HaExcludeStars: boolPtr(true),
			Params: mustParams(map[string]any{
				"star_reduce": 0.5, "star_desat": 0.4, "saturation": 0.16,
			}),
		}),
		builtin("narrowband-hoo", CategoryNarrowband, Payload{
			Mode: "nebula", Format: "image", Palette: "hoo",
			ColorCalibration: boolPtr(false), Denoise: boolPtr(true), HaExcludeStars: boolPtr(true),
			Params: mustParams(map[string]any{
				"star_reduce": 0.4, "star_desat": 0.3, "saturation": 0.16,
			}),
		}),
		builtin("narrowband-foraxx", CategoryNarrowband, Payload{
			Mode: "nebula", Format: "image", Palette: "foraxx",
			ColorCalibration: boolPtr(false), Denoise: boolPtr(true), HaExcludeStars: boolPtr(true),
			Params: mustParams(map[string]any{
				"star_reduce": 0.5, "star_desat": 0.35, "saturation": 0.17,
			}),
		}),
	}
}

// solarBuiltins — Moon & planets via lucky imaging: mode planetary.
func solarBuiltins() []Item {
	return []Item{
		builtin("moon", CategorySolar, Payload{
			Mode: "planetary", Format: "image",
			// Bright full disk: reserve highlight headroom so it does not burn, moderate sharpening +
			// local contrast for craters, keep a healthy fraction of frames (the Moon is bright & steady).
			Params: mustParams(map[string]any{
				"headroom": 0.85, "sharpen": 1.2, "clahe": 1.5, "best_percent": 30, "saturation": 0.2,
			}),
		}),
		builtin("planet", CategorySolar, Payload{
			Mode: "planetary", Format: "both",
			// Small bright disk: strict frame selection, alignment-point warping, a mild luminance
			// deconvolution and a saturation boost for the real disc colour.
			Params: mustParams(map[string]any{
				"best_percent": 10, "ap_align": true, "deconv_fwhm": 3, "deconv_iters": 20,
				"deconv_alpha": 1500, "sharpen": 1.4, "clahe": 1.0, "saturation": 0.8,
			}),
		}),
	}
}

// cometBuiltins — moving comet: mode comet (dual star/comet stack + star-layer recomposite).
func cometBuiltins() []Item {
	return []Item{
		builtin("comet", CategoryComet, Payload{
			Mode: "comet", Format: "image",
			// A gentle coma-gradient removal and a touch of saturation for the ion/dust tail colour; the
			// dual star/comet stacking is handled by the mode itself.
			Params: mustParams(map[string]any{
				"background_degree": 2, "saturation": 0.14,
			}),
		}),
	}
}

// milkywayBuiltins — wide-field nightscapes: mode milkyway. The recipe is the look + brightness (the
// nightscape renderer owns the rest); no Advanced knobs.
func milkywayBuiltins() []Item {
	return []Item{
		builtin("milkyway-natural", CategoryMilkyway, Payload{
			Mode: "milkyway", Format: "image", Look: "natural", Brightness: "balanced",
		}),
		builtin("milkyway-iphone", CategoryMilkyway, Payload{
			Mode: "milkyway", Format: "image", Look: "iphone", Brightness: "balanced",
		}),
		builtin("milkyway-deep", CategoryMilkyway, Payload{
			Mode: "milkyway", Format: "image", Look: "deepsky", Brightness: "brighter",
		}),
	}
}
