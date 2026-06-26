package inspect

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/verove-jordan/astronomy/internal/fits/fitstest"
)

// dateObs formats a quoted FITS DATE-OBS at the given second offset within 2025-03-28.
func dateObs(sec int) string {
	return fmt.Sprintf("'2025-03-28T%02d:%02d:%02d.000'", sec/3600, (sec%3600)/60, sec%60)
}

// background level per filter — broadband bright (L) down to narrowband faint (Ha).
var levelFor = map[string]uint16{"L": 3000, "R": 1100, "G": 1180, "B": 1050, "Ha": 300}

func countLightSets(inv *Inventory) int {
	n := 0
	for _, s := range inv.Sets {
		if s.Key.Type == Light {
			n++
		}
	}
	return n
}

func TestScan_DetectsUnlabeledChannelsByWheelOrder(t *testing.T) {
	dir := t.TempDir()
	order := []string{"L", "R", "G", "B", "Ha"}
	sec := 0
	for bi, filt := range order {
		if bi > 0 {
			sec += 200 // wheel move between filter blocks
		}
		for k := 0; k < 6; k++ {
			// no FILTER, no IMAGETYP, generic name: only the signal + timing reveal the channel.
			fitstest.Write(t, dir, fmt.Sprintf("img_%02d_%02d.fits", bi, k), 16, 16, levelFor[filt], map[string]string{
				"GAIN": "300", "OFFSET": "50", "CCD-TEMP": "-20.0", "XBINNING": "1",
				"EXPTIME": "30.0", "DATE-OBS": dateObs(sec),
			})
			sec += 31
		}
	}

	inv, err := Scan(context.Background(), dir)
	require.NoError(t, err)

	assert.Equal(t, 5, countLightSets(inv), "unlabeled lights must separate into 5 filter sets, not collapse into one")
	require.NotNil(t, inv.ChannelDetection)
	got := make([]string, len(inv.ChannelDetection.Runs))
	for i, r := range inv.ChannelDetection.Runs {
		got[i] = r.Filter
	}
	assert.Equal(t, order, got, "runs assigned in wheel order L→R→G→B→Ha")
	assert.Greater(t, inv.ChannelDetection.OverallConfidence, 0.3)

	for _, fr := range inv.Frames {
		assert.Equal(t, SourceSignal, fr.ClassSource)
		assert.NotEmpty(t, fr.Filter)
	}
}

func TestScan_LabeledFiltersStayMetadataDriven(t *testing.T) {
	dir := t.TempDir()
	for bi, filt := range []string{"L", "R", "G", "B"} {
		for k := 0; k < 4; k++ {
			fitstest.Write(t, dir, fmt.Sprintf("Light_%s_%02d.fits", filt, k), 16, 16, 1500, map[string]string{
				"IMAGETYP": "'Light Frame'", "FILTER": "'" + filt + "'",
				"GAIN": "300", "OFFSET": "50", "CCD-TEMP": "-20.0", "XBINNING": "1", "EXPTIME": "30.0",
				"DATE-OBS": dateObs(bi*300 + k*31),
			})
		}
	}
	inv, err := Scan(context.Background(), dir)
	require.NoError(t, err)

	assert.Equal(t, 4, countLightSets(inv))
	assert.Nil(t, inv.ChannelDetection, "no signal detection when filters are already known")
	for _, fr := range inv.Frames {
		assert.NotEqual(t, SourceSignal, fr.ClassSource)
	}
}

func TestApplyFilterMapping_RelabelsAndIgnores(t *testing.T) {
	dir := t.TempDir()
	for _, filt := range []string{"R", "G", "B"} {
		fitstest.Write(t, dir, "Light_"+filt+".fits", 16, 16, 1500, map[string]string{
			"IMAGETYP": "'Light Frame'", "FILTER": "'" + filt + "'",
			"GAIN": "300", "OFFSET": "50", "EXPTIME": "30.0",
		})
	}
	inv, err := Scan(context.Background(), dir)
	require.NoError(t, err)
	require.Equal(t, 3, countLightSets(inv))

	ApplyFilterMapping(inv, map[string]string{"G": "ignore", "B": "Ha"})
	filters := map[string]bool{}
	for _, s := range inv.Sets {
		if s.Key.Type == Light {
			filters[s.Key.Filter] = true
		}
	}
	assert.True(t, filters["R"], "R kept")
	assert.True(t, filters["Ha"], "B relabeled to Ha")
	assert.False(t, filters["G"], "G ignored (excluded from light sets)")
	assert.Equal(t, 2, countLightSets(inv))
}
