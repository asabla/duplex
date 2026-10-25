package diagnostics

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestRecorderConcurrentAggregation(t *testing.T) {
	var (
		r  Recorder
		wg sync.WaitGroup
	)
	for range 10 {
		wg.Go(func() {
			for range 100 {
				r.Add("metadata", time.Millisecond)
			}
		})
	}

	wg.Wait()
	r.Begin("prepare")()

	v := r.Snapshot()
	if len(v) != 2 || v[0].Count != 1000 || v[0].Total != time.Second || v[0].Maximum != time.Millisecond {
		t.Fatal(v)
	}

	v[0].Count = 0
	if r.Snapshot()[0].Count != 1000 {
		t.Fatal("mutable snapshot")
	}

	var nilRecorder *Recorder
	nilRecorder.Begin("none")()

	if nilRecorder.Snapshot() != nil {
		t.Fatal("nil snapshot")
	}

	want := errors.New("operation failed")
	if v, err := Measure(&r, "failed", func() (int, error) { return 7, want }); v != 7 || !errors.Is(err, want) {
		t.Fatal("result lost")
	}

	if err := Run(&r, "run", func() error { return want }); !errors.Is(err, want) {
		t.Fatal("error lost")
	}
}

func TestPrivateProfilesAndCompletion(t *testing.T) {
	for _, kind := range []string{"cpu", "trace", "allocs", "block"} {
		t.Run(kind, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "profiles")

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			count := 0

			c, err := StartCapture(ctx, dir, kind, time.Hour, func() error {
				count++
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}

			if err = c.Stop(); err != nil {
				t.Fatal(err)
			}

			if err = c.Stop(); err != nil {
				t.Fatal(err)
			}

			if count != 1 {
				t.Fatal("duplicate completion")
			}

			files, err := os.ReadDir(dir)
			if err != nil || len(files) != 1 {
				t.Fatal(files, err)
			}

			info, err := files[0].Info()
			if err != nil {
				t.Fatal(err)
			}

			if info.Mode().Perm() != 0o600 || info.Size() == 0 {
				t.Fatal("profile is empty or not private")
			}
		})
	}
}

func TestProfileErrorsAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		dir, kind string
		duration  time.Duration
	}{{"relative", "cpu", time.Second}, {t.TempDir(), "../../bad", time.Second}, {t.TempDir(), "cpu", 0}, {t.TempDir(), "cpu", 2 * time.Hour}} {
		if _, err := StartCapture(context.Background(), tc.dir, tc.kind, tc.duration, nil); err == nil {
			t.Fatal("invalid profile accepted")
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	completed := make(chan struct{})
	want := errors.New("cannot save measurements")

	c, err := StartCapture(ctx, t.TempDir(), "allocs", time.Hour, func() error {
		close(completed)
		return want
	})
	if err != nil {
		t.Fatal(err)
	}

	cancel()

	select {
	case <-completed:
	case <-time.After(5 * time.Second):
		t.Fatal("profile did not stop")
	}

	if !errors.Is(c.Stop(), want) {
		t.Fatal("completion error lost")
	}

	if err := (*Capture)(nil).Stop(); err != nil {
		t.Fatal(err)
	}

	file := filepath.Join(t.TempDir(), "file")
	if err = os.WriteFile(file, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err = StartCapture(context.Background(), file, "allocs", time.Second, nil); err == nil {
		t.Fatal("file directory accepted")
	}
}
