//go:build !android

package mediasession

// Service does nothing off Android, the way the MPRIS service does nothing
// without a session bus — except here the absence is known at compile time, so
// there is no error to report and New cannot fail. The caller keeps one
// unconditional call site and the desktop keeps zero moving parts.
type Service struct{}

// New returns a Service that discards everything.
func New(c Controls) *Service { return nil }

// Update, Tick and Close are nil-safe no-ops, matching the Android surface.
func (s *Service) Update() {}
func (s *Service) Tick()   {}
func (s *Service) Close()  {}
