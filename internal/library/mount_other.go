//go:build !linux

package library

import "errors"

// Mount is unavailable on platforms without Linux FUSE support.
type Mount struct{}

// Ready reports false because this platform cannot host the production mount.
func (m *Mount) Ready() bool { return false }

// Close releases the unsupported-platform mount placeholder.
func (m *Mount) Close() error { return nil }

// Update rejects publication when Linux FUSE is unavailable.
func (m *Mount) Update(_ []Entry) error { return errors.New("FUSE mounting requires Linux") }

// MountReadOnly requires Linux; macOS tests must use a Linux VM.
func MountReadOnly(_ string, _ []Entry) (*Mount, error) {
	return nil, errors.New("FUSE mounting requires Linux; use Colima for macOS testing")
}
