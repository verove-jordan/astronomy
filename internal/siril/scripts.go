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
