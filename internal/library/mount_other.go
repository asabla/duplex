//go:build !linux

package library

import "errors"

type Mount struct{}

func (m *Mount) Ready() bool { return false }

func (m *Mount) Close() error           { return nil }
func (m *Mount) Update(_ []Entry) error { return errors.New("FUSE mounting requires Linux") }
func MountReadOnly(_ string, _ []Entry) (*Mount, error) {
	return nil, errors.New("FUSE mounting requires Linux; use Colima for macOS testing")
}
