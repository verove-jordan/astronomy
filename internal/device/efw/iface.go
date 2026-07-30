package efw

import "github.com/verove-jordan/astronomy/internal/device"

// Compile-time proof that these drivers satisfy the engine's device interfaces. Without it, a
// signature drift would only surface when someone plugs a camera in at 2am.
var _ device.FilterWheel = (*Wheel)(nil)
