package devsrv

import (
	"context"
	"net/http"

	"github.com/verove-jordan/astronomy/internal/device"
	"github.com/verove-jordan/astronomy/internal/device/nexstar"
)

// mountAxes reports the raw shaft angles.
//
// It is a separate endpoint from /mount rather than another field on the snapshot because it means
// something different: /mount reports where the hand controller BELIEVES the telescope points, which
// on an unaligned mount is fiction, while this reports what the motor controllers MEASURE. Merging
// them would invite reading one as the other.
func (s *Server) mountAxes(w http.ResponseWriter, r *http.Request) {
	reader, ok := s.currentMount().(interface {
		AxisAngles(context.Context) (nexstar.AxisAngles, error)
	})
	if !ok {
		deviceError(w, device.ErrUnsupported)
		return
	}
	axes, err := reader.AxisAngles(r.Context())
	if err != nil {
		deviceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"axes": axes})
}
