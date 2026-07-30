package annotate

import (
	"encoding/json"
	"image/png"
	"os"
	"path/filepath"
	"strings"
)

// runInfo is the slice of run.json this package needs (a local decode — the pipeline's Result
// type would drag the whole engine in as a dependency).
type runInfo struct {
	Object string `json:"object"`
	Final  *struct {
		Mode    string   `json:"mode"`
		Outputs []string `json:"outputs"`
	} `json:"final"`
	Outputs  []string `json:"outputs"` // planetary's flat shape
	Channels []struct {
		Filter     string `json:"filter"`
		OutputPath string `json:"output_path"`
	} `json:"channels"`
}

// inputs is the resolved pair of files one annotation works on.
type inputs struct {
	masterRel, masterAbs string // linear FITS the count/labels use
	finalRel, finalAbs   string // final image whose pixels the labels target
	info                 runInfo
}

// readRunInfo decodes run.json when present (absence is tolerated — conventions cover most runs).
func readRunInfo(locate func(string) (string, bool)) runInfo {
	var info runInfo
	abs, ok := locate("run.json")
	if !ok {
		return info
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return info
	}
	_ = json.Unmarshal(b, &info)
	return info
}

// selectInputs picks the linear master and the final image for the run's mode, probing candidates
// through locate (which transparently restores S3-freed files). First hit wins.
func selectInputs(mode string, locate func(string) (string, bool)) (inputs, error) {
	in := inputs{info: readRunInfo(locate)}
	if mode == "" && in.info.Final != nil {
		mode = in.info.Final.Mode
	}

	var masters, finals []string
	switch mode {
	case "planetary":
		return in, ErrUnsupportedMode
	case "comet":
		base := finalBase(in.info)
		masters = []string{"star_color_lin.fits"}
		if base != "" {
			masters = append(masters, base+".fits")
			finals = append(finals, base+".png")
		}
	case "milkyway":
		masters = []string{"stacked_sky.fits", "osc_master.fits"}
		finals = []string{"final.png"}
	default: // deepsky / nebula / livestack (and any future star-field mode)
		masters = []string{filepath.Join(linearDirName, "rgb_base_spcc.fits"), "rgb_base.fits"}
		for _, ch := range in.info.Channels {
			if ch.OutputPath != "" {
				masters = append(masters, filepath.Base(ch.OutputPath))
			}
		}
		finals = []string{"final.png"}
	}
	finals = append(finals, pngOutputs(in.info)...)

	for _, rel := range masters {
		if abs, ok := locate(rel); ok {
			in.masterRel, in.masterAbs = rel, abs
			break
		}
	}
	if in.masterAbs == "" {
		return in, ErrNoMaster
	}
	seen := map[string]bool{}
	for _, rel := range finals {
		if rel == "" || seen[rel] {
			continue
		}
		seen[rel] = true
		if abs, ok := locate(rel); ok {
			in.finalRel, in.finalAbs = rel, abs
			break
		}
	}
	if in.finalAbs == "" {
		return in, ErrNoFinal
	}
	return in, nil
}

// finalBase returns the run's final output base name (no extension) from run.json outputs.
func finalBase(info runInfo) string {
	for _, p := range pngOutputs(info) {
		return strings.TrimSuffix(p, ".png")
	}
	return ""
}

// pngOutputs lists the run-relative .png outputs recorded in run.json (final shape first, then the
// flat planetary shape).
func pngOutputs(info runInfo) []string {
	var out []string
	add := func(paths []string) {
		for _, p := range paths {
			if strings.HasSuffix(strings.ToLower(p), ".png") {
				out = append(out, filepath.Base(p))
			}
		}
	}
	if info.Final != nil {
		add(info.Final.Outputs)
	}
	add(info.Outputs)
	return out
}

// pngDims reads a PNG's dimensions from its header only.
func pngDims(path string) (w, h int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	cfg, err := png.DecodeConfig(f)
	if err != nil {
		return 0, 0, err
	}
	return cfg.Width, cfg.Height, nil
}
