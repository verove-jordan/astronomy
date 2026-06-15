package siril

import (
	"fmt"
	"strings"
)

// scriptHeader requires a modern Siril and fixes the output extension so produced masters and
// stacks are predictably named `<name>.fits`.
const scriptHeader = "requires 1.2.0\nsetext fits\n"

// CalibMasters holds absolute paths to the master calibration frames to apply (any may be empty).
type CalibMasters struct {
	Bias string
	Dark string
	Flat string
}

// StackMasterScript links the FITS already in the work dir as sequence `seq` and stacks them
// with Winsorized sigma rejection into outName (extension added by Siril). Used for darks/bias:
// no normalization.
func StackMasterScript(seq, outName string) string {
	var b strings.Builder
	b.WriteString(scriptHeader)
	fmt.Fprintf(&b, "link %s -out=.\n", seq)
	fmt.Fprintf(&b, "stack %s rej winsorized 3 3 -nonorm -out=%s\n", seq, outName)
	return b.String()
}

// StackFlatScript builds a master flat: optionally bias-calibrate the flats, then stack with
// multiplicative normalization (the correct normalization for flat fields).
func StackFlatScript(seq, outName, biasPath string) string {
	var b strings.Builder
	b.WriteString(scriptHeader)
	fmt.Fprintf(&b, "link %s -out=.\n", seq)
	target := seq
	if biasPath != "" {
		fmt.Fprintf(&b, "calibrate %s -bias=%s -prefix=pp_\n", seq, biasPath)
		target = "pp_" + seq
	}
	fmt.Fprintf(&b, "stack %s rej winsorized 3 3 -norm=mul -out=%s\n", target, outName)
	return b.String()
}

// LightStackScript calibrates a light sequence with the matched masters, registers it
// (global star alignment), and stacks the registered frames into outName.
func LightStackScript(seq string, m CalibMasters, outName string) string {
	var b strings.Builder
	b.WriteString(scriptHeader)
	fmt.Fprintf(&b, "link %s -out=.\n", seq)

	target := seq
	if args := calibrateArgs(m); len(args) > 0 {
		fmt.Fprintf(&b, "calibrate %s %s -prefix=pp_\n", seq, strings.Join(args, " "))
		target = "pp_" + seq
	}
	fmt.Fprintf(&b, "register %s\n", target)
	fmt.Fprintf(&b, "stack r_%s rej winsorized 3 3 -norm=addscale -output_norm -out=%s\n", target, outName)
	return b.String()
}

// AlignMastersScript links the per-channel master stacks in the work dir as sequence `seq` and
// registers them together (global star alignment), co-registering the channels to one reference.
func AlignMastersScript(seq string) string {
	var b strings.Builder
	b.WriteString(scriptHeader)
	fmt.Fprintf(&b, "link %s -out=.\n", seq)
	fmt.Fprintf(&b, "register %s\n", seq)
	return b.String()
}

// CalibrateRegisterScript calibrates (if masters are given) and registers a light sequence
// WITHOUT stacking, so the per-frame registration metrics are written to the .seq for grading.
func CalibrateRegisterScript(seq string, m CalibMasters) string {
	var b strings.Builder
	b.WriteString(scriptHeader)
	fmt.Fprintf(&b, "link %s -out=.\n", seq)
	target := seq
	if args := calibrateArgs(m); len(args) > 0 {
		fmt.Fprintf(&b, "calibrate %s %s -prefix=pp_\n", seq, strings.Join(args, " "))
		target = "pp_" + seq
	}
	fmt.Fprintf(&b, "register %s\n", target)
	return b.String()
}

// CalibratedSeq is the sequence name after calibration — the input to registration and the
// stable, 1:1-with-inputs index space used for grading. Its .seq file is this name + "_.seq".
func CalibratedSeq(seq string, m CalibMasters) string {
	if len(calibrateArgs(m)) > 0 {
		return "pp_" + seq
	}
	return seq
}

// RegisteredSeq is the registered sequence produced by CalibrateRegisterScript.
func RegisteredSeq(seq string, m CalibMasters) string {
	return "r_" + CalibratedSeq(seq, m)
}

// StackSelectedScript resets the registered sequence to all-included, unselects our graded-out
// frames (1-based registered indices), then stacks only the survivors. Winsorized sigma rejection
// additionally clips residual satellite/plane trail pixels.
func StackSelectedScript(regSeq string, regCount int, rejected []int, outName string) string {
	var b strings.Builder
	b.WriteString(scriptHeader)
	if regCount > 0 {
		fmt.Fprintf(&b, "select %s 1 %d\n", regSeq, regCount)
	}
	for _, idx := range rejected {
		fmt.Fprintf(&b, "unselect %s %d %d\n", regSeq, idx, idx)
	}
	fmt.Fprintf(&b, "stack %s rej winsorized 3 3 -norm=addscale -output_norm -filter-incl -out=%s\n", regSeq, outName)
	return b.String()
}

// ConvertScript converts the files in the work dir into a FITS sequence named `seq`.
func ConvertScript(seq string) string {
	return scriptHeader + fmt.Sprintf("convert %s -out=.\n", seq)
}

// PlanetaryStackScript stacks the best (selected) frames of a converted video sequence — no
// registration (planetary/lunar surfaces have no stars) — then optionally sharpens, stretches
// and saves to the given formats.
func PlanetaryStackScript(seq string, count int, rejected []int, outName string, sharpen bool, formats []string) string {
	var b strings.Builder
	b.WriteString(scriptHeader)
	if count > 0 {
		fmt.Fprintf(&b, "select %s 1 %d\n", seq, count)
	}
	for _, idx := range rejected {
		fmt.Fprintf(&b, "unselect %s %d %d\n", seq, idx, idx)
	}
	fmt.Fprintf(&b, "stack %s rej winsorized 3 3 -nonorm -filter-incl -out=%s\n", seq, outName)
	fmt.Fprintf(&b, "load %s\n", outName)
	if sharpen {
		b.WriteString("unsharp 3 0.8\n")
	}
	b.WriteString("autostretch\n")
	for _, f := range formats {
		b.WriteString(saveCmd(f, outName) + "\n")
	}
	return b.String()
}

func saveCmd(format, base string) string {
	switch format {
	case "png":
		return "savepng " + base
	case "tif", "tiff":
		return "savetif " + base
	case "jpg", "jpeg":
		return "savejpg " + base + " 95"
	default:
		return "save " + base
	}
}

func calibrateArgs(m CalibMasters) []string {
	var args []string
	if m.Dark != "" {
		args = append(args, "-dark="+m.Dark)
	}
	if m.Flat != "" {
		args = append(args, "-flat="+m.Flat)
	}
	if m.Bias != "" {
		args = append(args, "-bias="+m.Bias)
	}
	if m.Dark != "" {
		args = append(args, "-cc=dark") // cosmetic hot/cold pixel correction from the dark
	}
	return args
}
