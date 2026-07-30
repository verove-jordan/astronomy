package gen

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const fixtureHeader = "id,hip,hd,proper,ra,dec,mag,bayer,flam,con,pmra,pmdec\n"

func TestParse_SlimsAndConverts(t *testing.T) {
	src := fixtureHeader +
		"0,,,Sol,0,0,-26.7,,,,0,0\n" + // the Sun: always dropped
		"1,1,48915,Sirius,6.752481,-16.716116,-1.44,Alp,9,CMa,-546.01,-1223.08\n" +
		"2,2,999,,1.0,10.0,9.5,,,,0,0\n" + // fainter than the limit: dropped
		"3,3,,,2.0,11.0,4.5,The-2,,Ori,12.4,-3.6\n"
	rows, err := parse(strings.NewReader(src), 9)
	require.NoError(t, err)
	require.Len(t, rows, 2)

	sirius := rows[0]
	assert.InDelta(t, 6.752481*15, sirius.raDeg, 1e-9, "RA hours → degrees")
	assert.Equal(t, "Sirius", sirius.proper)
	assert.Equal(t, 48915, sirius.hd)
	assert.Equal(t, 9, sirius.flam)

	theta := rows[1]
	assert.Equal(t, "The-2", theta.bayer)
	assert.Equal(t, "Ori", theta.con)
}

func TestParse_RejectsUnknownBayerToken(t *testing.T) {
	src := fixtureHeader + "1,1,10,,1.5,10,4.5,Foo,2,Ori,0,0\n"
	_, err := parse(strings.NewReader(src), 9)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown Bayer token "Foo"`)
}

func TestParse_MissingColumnFails(t *testing.T) {
	_, err := parse(strings.NewReader("id,hip,ra,dec\n1,1,1.0,2.0\n"), 9)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `missing the "mag" column`)
}
