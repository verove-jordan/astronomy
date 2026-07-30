package asi

import "github.com/verove-jordan/astronomy/internal/device"

// Compile-time proof that this driver satisfies the engine's camera interface. Without it, a
// signature drift would only surface when someone plugs a camera in at 2am.
var _ device.Camera = (*Camera)(nil)
