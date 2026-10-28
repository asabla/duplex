//go:build !linux

package library

import (
	"context"
	"errors"
)

// ServeMountHelper requires Linux FUSE and Unix peer credentials.
func ServeMountHelper(context.Context) error { return errors.New("mount helper requires Linux") }

// MountProviderWithHelper requires Linux FUSE.
func MountProviderWithHelper(string, Provider, string) (*Mount, error) {
	return nil, errors.New("mount helper requires Linux")
}
