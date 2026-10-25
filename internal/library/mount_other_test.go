//go:build !linux

package library

import "testing"

func TestNativeMountReportsLinuxRequirement(t *testing.T) {
	if _, err := MountReadOnly(t.TempDir(), nil); err == nil {
		t.Fatal("unsupported native mount succeeded")
	}

	if _, err := MountProvider(t.TempDir(), nil); err == nil {
		t.Fatal("unsupported provider mount succeeded")
	}

	mount := &Mount{}
	if mount.Ready() {
		t.Fatal("unsupported mount became ready")
	}

	if err := mount.Update(nil); err == nil {
		t.Fatal("unsupported publication succeeded")
	}

	if err := mount.Close(); err != nil {
		t.Fatal(err)
	}
}
