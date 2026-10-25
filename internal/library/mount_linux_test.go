//go:build linux

package library

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestFuseSnapshotAndOpenHandle(t *testing.T) {
	if os.Getenv("DUPLEX_FUSE_TEST") != "1" {
		t.Skip("requires explicit Linux FUSE test container")
	}
	dir := t.TempDir()
	m, e := MountReadOnly(dir, []Entry{{Path: "Channel/episode.mp4", Media: blob{id: "old", data: []byte("old bytes")}}})
	if e != nil {
		t.Fatal(e)
	}
	defer m.Close()
	if !m.Ready() {
		t.Fatal("mounted filesystem not ready")
	}
	old, e := os.Open(filepath.Join(dir, "Channel/episode.mp4"))
	if e != nil {
		t.Fatal(e)
	}
	defer old.Close()
	if e = m.Update([]Entry{{Path: "Channel/episode.mp4", Media: blob{id: "new", data: []byte("new representation")}}}); e != nil {
		t.Fatal(e)
	}
	data, e := os.ReadFile(filepath.Join(dir, "Channel/episode.mp4"))
	if e != nil || string(data) != "new representation" {
		t.Fatal(string(data), e)
	}
	b := make([]byte, 9)
	if _, e = old.ReadAt(b, 0); e != nil || string(b) != "old bytes" {
		t.Fatal("open handle changed revision", string(b), e)
	}
	if _, e = os.OpenFile(filepath.Join(dir, "Channel/episode.mp4"), os.O_WRONLY, 0); e == nil {
		t.Fatal("writable media")
	}
	if e = m.Update(nil); e != nil {
		t.Fatal(e)
	}
	if _, e = old.ReadAt(b, 0); e != nil || string(b) != "old bytes" {
		t.Fatal("unlinked active handle failed", e)
	}
}

func TestFuseCrashRecovery(t *testing.T) {
	if os.Getenv("DUPLEX_FUSE_TEST") != "1" {
		t.Skip("requires explicit Linux FUSE test container")
	}
	if dir := os.Getenv("DUPLEX_CRASH_CHILD"); dir != "" {
		m, e := MountReadOnly(dir, []Entry{{Path: "episode.mp4", Media: blob{id: "before", data: []byte("before crash")}}})
		if e != nil {
			t.Fatal(e)
		}
		defer m.Close()
		if e = os.WriteFile(dir+".ready", []byte("ready"), 0600); e != nil {
			t.Fatal(e)
		}
		select {}
	}
	root := t.TempDir()
	dir := filepath.Join(root, "library")
	binary, _ := os.Executable()
	child := exec.Command(binary, "-test.run=^TestFuseCrashRecovery$")
	child.Env = append(os.Environ(), "DUPLEX_CRASH_CHILD="+dir)
	if e := child.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, e := os.Stat(dir + ".ready"); e == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child mount not ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	old, e := os.Open(filepath.Join(dir, "episode.mp4"))
	if e != nil {
		t.Fatal(e)
	}
	defer old.Close()
	if e = child.Process.Kill(); e != nil {
		t.Fatal(e)
	}
	_ = child.Wait()
	next, e := MountReadOnly(dir, []Entry{{Path: "episode.mp4", Media: blob{id: "after", data: []byte("after restart")}}})
	if e != nil {
		t.Fatal(e)
	}
	defer next.Close()
	b := make([]byte, 12)
	if _, e = old.ReadAt(b, 0); e == nil {
		t.Fatal("stale handle returned success")
	}
	data, e := os.ReadFile(filepath.Join(dir, "episode.mp4"))
	if e != nil || string(data) != "after restart" {
		t.Fatal(string(data), e)
	}
	if _, e = MountReadOnly(dir, nil); e == nil {
		t.Fatal("live mount was replaced")
	}
}
