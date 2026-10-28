//go:build !linux

package library

import (
	"context"
	"testing"
)

func TestNativeMountReportsLinuxRequirement(t *testing.T) {
	if err := ServeMountHelper(context.Background()); err == nil {
		t.Fatal("native mount helper succeeded")
	}

	if _, err := MountProviderWithHelper(t.TempDir(), nil, "socket"); err == nil {
		t.Fatal("native helper mount succeeded")
	}

	if _, err := MountReadOnly(t.TempDir(), nil); err == nil {
		t.Fatal("unsupported native mount succeeded")
	}

	if _, err := MountProvider(t.TempDir(), nil); err == nil {
		t.Fatal("unsupported provider mount succeeded")
	}

	mount := &Mount{}
	if mount.Done() != nil {
		t.Fatal("native mount has a helper connection")
	}

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
