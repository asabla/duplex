//go:build linux

package library

import (
	"errors"
	"os"
	"testing"
)

func TestMountPollingRejectsMissingProbe(t *testing.T) {
	if err := primeMountPolling(t.TempDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing poll probe was accepted", err)
	}
}
