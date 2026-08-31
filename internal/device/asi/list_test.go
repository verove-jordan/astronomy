package asi

import (
	"encoding/binary"
	"fmt"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// listSDK builds a function table reporting n cameras, failing the property read for any index in
// bad. It counts ASIOpenCamera calls, because "never opens a camera" is the contract that makes
// discovery safe to poll while a sequence is running.
func listSDK(n int32, bad map[int32]bool) (*sdk, *int) {
	opens := 0
	s := &sdk{
		getNumOfConnectedCameras: func() int32 { return n },
		getCameraProperty: func(info *byte, index int32) int32 {
			if index < 0 || index >= n || bad[index] {
				return int32(asiErrorInvalidIndex)
			}
			b := unsafe.Slice(info, infoStructSize)
			for i := range b {
				b[i] = 0
			}
			copy(b[offName:], fmt.Sprintf("ZWO ASI%d", 1600+index))
			binary.LittleEndian.PutUint32(b[offCameraID:], uint32(index*10))
			return 0
		},
		openCamera: func(int32) int32 { opens++; return 0 },
	}
	return s, &opens
}

func TestListCameras(t *testing.T) {
	tests := []struct {
		name      string
		count     int32
		bad       map[int32]bool
		wantNames []string
		wantIDs   []int32
	}{
		{
			name:      "one camera is listed with the name the SDK reports",
			count:     1,
			wantNames: []string{"ZWO ASI1600"},
			wantIDs:   []int32{0},
		},
		{
			name:      "every connected camera is listed, not just the first",
			count:     3,
			wantNames: []string{"ZWO ASI1600", "ZWO ASI1601", "ZWO ASI1602"},
			wantIDs:   []int32{0, 10, 20},
		},
		{
			name:  "nothing plugged in lists nothing",
			count: 0,
		},
		{
			name:      "a camera that will not identify does not hide the others",
			count:     3,
			bad:       map[int32]bool{1: true},
			wantNames: []string{"ZWO ASI1600", "ZWO ASI1602"},
			wantIDs:   []int32{0, 20},
		},
		{
			name:  "an implausible count is reported as nothing rather than garbage",
			count: 9999,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, opens := listSDK(tt.count, tt.bad)

			got := listCameras(s)

			require.Len(t, got, len(tt.wantNames))
			var names []string
			var ids []int32
			for _, d := range got {
				names = append(names, d.Name)
				ids = append(ids, d.ID)
			}
			assert.Equal(t, tt.wantNames, names)
			if len(tt.wantIDs) > 0 {
				assert.Equal(t, tt.wantIDs, ids)
			}
			assert.Zero(t, *opens, "discovery must not open a camera — it is polled, and opening "+
				"would steal the camera from an exposure already in flight")
		})
	}
}
