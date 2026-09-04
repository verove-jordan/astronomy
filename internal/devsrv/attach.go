package devsrv

import "github.com/verove-jordan/astronomy/internal/device"

// Attaching and detaching devices, and reading the one currently attached.
//
// Every one of these takes s.mu only long enough to read or swap a pointer, and calls Close()
// afterwards, outside the lock. That is the whole rule: a driver call is hardware time, and hardware
// time must never be a lock other requests are queued behind. See the Server doc comment.

func (s *Server) currentCamera() device.Camera {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.camera
}

func (s *Server) currentWheel() device.FilterWheel {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.wheel
}

func (s *Server) currentMount() device.Mount {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mount
}

// attachCamera publishes a connected camera. The atomic flag is what /health reads, so it is set
// only once the driver has actually answered.
func (s *Server) attachCamera(cam device.Camera) {
	s.mu.Lock()
	s.camera = cam
	s.mu.Unlock()
	s.camOn.Store(true)
	s.inv.invalidate()
}

// detachCamera closes whatever camera is attached, if any. Safe to call when there is none.
func (s *Server) detachCamera() {
	s.mu.Lock()
	cam := s.camera
	s.camera = nil
	s.mu.Unlock()
	s.camOn.Store(false)
	if cam != nil {
		_ = cam.Close()
	}
	s.inv.invalidate()
}

func (s *Server) attachWheel(wheel device.FilterWheel) {
	s.mu.Lock()
	s.wheel = wheel
	s.mu.Unlock()
	s.wheelOn.Store(true)
	s.inv.invalidate()
}

func (s *Server) detachWheel() {
	s.mu.Lock()
	wheel := s.wheel
	s.wheel = nil
	s.mu.Unlock()
	s.wheelOn.Store(false)
	if wheel != nil {
		_ = wheel.Close()
	}
	s.inv.invalidate()
}

func (s *Server) attachMount(mount device.Mount) {
	s.mu.Lock()
	s.mount = mount
	s.mu.Unlock()
	s.mountOn.Store(true)
	s.inv.invalidate()
}

func (s *Server) detachMount() {
	s.mu.Lock()
	mount := s.mount
	s.mount = nil
	s.mu.Unlock()
	s.mountOn.Store(false)
	if mount != nil {
		_ = mount.Close()
	}
	s.inv.invalidate()
}
