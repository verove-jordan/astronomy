package postprocess

import (
	"github.com/verove-jordan/astronomy/internal/config"
	"github.com/verove-jordan/astronomy/internal/siril"
)

// SolveSpccFromConfig builds the plate-solve and SPCC option sets every pipeline entry shares, from
// the runtime config. It wires the local Gaia catalogue paths only when the files are actually
// present (see config.LocalGaia*), which makes the generated scripts prefer offline solving/SPCC —
// the missing piece that made SPCC silently fall back to neutralization on every networkless run.
func SolveSpccFromConfig(cfg *config.Config) (siril.SolveOptions, siril.SpccOptions) {
	solve := siril.SolveOptions{
		FocalMM: cfg.FocalLenMM, PixelUm: cfg.PixelSizeUm,
		Catalog: cfg.PlateSolveCatalog, LocalAsnet: cfg.LocalAsnet,
		AstroCat: cfg.LocalGaiaAstroCat(), XpsampDir: cfg.LocalGaiaXpsampDir(),
	}
	spcc := siril.SpccOptions{
		MonoSensor: cfg.SpccMonoSensor, OSCSensor: cfg.NightscapeOSCSensor,
		RFilter: cfg.SpccRFilter, GFilter: cfg.SpccGFilter,
		BFilter: cfg.SpccBFilter, WhiteRef: cfg.SpccWhiteRef,
		Catalog: cfg.SpccCatalog,
	}
	return solve, spcc
}
