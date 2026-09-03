package efw

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// listSDK builds a function table reporting n wheels, failing EFWGetID for any index in bad. It
// counts EFWOpen calls: discovery must never open a wheel, because opening is what re-homes it, and
// a re-home mid-sequence puts a filter edge across the frame.
func listSDK(n int32, bad map[int32]bool) (*sdk, *int) {
	opens := 0
	s := &sdk{
		getNum: func() int32 { return n },
		getID: func(index int32, id *int32) int32 {
			if index < 0 || index >= n || bad[index] {
				return 1
			}
			*id = index * 10
			return 0
		},
		open: func(int32) int32 { opens++; return 0 },
	}
	return s, &opens
}

func TestListWheels(t *testing.T) {
	tests := []struct {
		name    string
		count   int32
		bad     map[int32]bool
		wantIDs []int32
	}{
		{
			name:    "one wheel is listed with the id the SDK reports",
			count:   1,
			wantIDs: []int32{0},
		},
		{
			name:    "every connected wheel is listed",
			count:   2,
			wantIDs: []int32{0, 10},
		},
		{
			name:  "nothing plugged in lists nothing",
			count: 0,
		},
		{
			name:    "a wheel that will not identify does not hide the others",
			count:   3,
			bad:     map[int32]bool{1: true},
			wantIDs: []int32{0, 20},
		},
		{
			name:  "an implausible count is reported as nothing rather than garbage",
			count: 999,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, opens := listSDK(tt.count, tt.bad)

			got := listWheels(s)

			require.Len(t, got, len(tt.wantIDs))
			var ids []int32
			for _, d := range got {
				ids = append(ids, d.ID)
			}
			assert.Equal(t, tt.wantIDs, ids)
			assert.Zero(t, *opens, "discovery must not open a wheel — opening re-homes it, and a "+
				"re-home mid-sequence puts a filter edge across the frame")
		})
	}
}
