//go:build unix

package source

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestResolverCancellationStopsItsChildProcess(t *testing.T) {
	dir := t.TempDir()
	heartbeat, pidFile := filepath.Join(dir, "heartbeat"), filepath.Join(dir, "pid")

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	command := exec.CommandContext(ctx, "sh", "-c", `(while :; do printf x >> "$1"; sleep 0.02; done) & echo $! > "$2"; wait`, "resolver-fixture", heartbeat, pidFile) //nolint:gosec // G204: Fixed controlled shell helper; both output paths are beneath t.TempDir and passed as arguments.
	configureResolverProcess(command)

	if err := command.Start(); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- command.Wait(); close(done) }()

	t.Cleanup(func() {
		cancel()

		if err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			t.Error(err)
		}

		if err := <-done; err != nil && ctx.Err() == nil {
			t.Error(err)
		}
	})

	deadline := time.Now().Add(3 * time.Second)

	for {
		info, err := os.Stat(heartbeat)
		if err == nil && info.Size() >= 2 {
			break
		}

		if time.Now().After(deadline) {
			t.Fatal("controlled child did not start")
		}

		time.Sleep(time.Millisecond)
	}

	pidBytes, err := os.ReadFile(pidFile) //nolint:gosec // G304: Read only the child PID produced by the owned fixture in t.TempDir.
	if err != nil {
		t.Fatal(err)
	}

	child, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		t.Fatal(err)
	}

	cancel()

	if err = <-done; err == nil {
		t.Fatal("canceled resolver exited successfully")
	}

	before, err := os.Stat(heartbeat)
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(100 * time.Millisecond)

	after, err := os.Stat(heartbeat)
	if err != nil {
		t.Fatal(err)
	}

	if before.Size() != after.Size() {
		if err := syscall.Kill(child, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			t.Error(err)
		}

		t.Fatal("resolver helper continued after cancellation")
	}
}
