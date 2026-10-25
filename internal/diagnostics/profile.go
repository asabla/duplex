package diagnostics

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"runtime/trace"
	"sync"
	"time"
)

// Capture records one profile kind. Profiles never have an HTTP listener.
type Capture struct {
	once sync.Once
	stop func() error
	err  error
	done chan struct{}
}

// StartCapture enables one explicit profile and returns an idempotent closer.
func StartCapture(ctx context.Context, directory, kind string, duration time.Duration, completed func() error) (*Capture, error) {
	if duration < time.Second || duration > time.Hour {
		return nil, errors.New("profile duration must be between one second and one hour")
	}
	if !filepath.IsAbs(directory) {
		return nil, errors.New("profile directory must be absolute")
	}
	if kind != "cpu" && kind != "trace" && kind != "allocs" && kind != "block" {
		return nil, errors.New("profile kind must be cpu, allocs, block, or trace")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(directory, fmt.Sprintf("%s-%d.pprof", kind, time.Now().UnixNano())), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: The operator selects the private profile directory; kind is validated against fixed values.
	if err != nil {
		return nil, err
	}
	finish, err := profileWriter(file, kind)
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	c := &Capture{done: make(chan struct{})}
	c.stop = func() error {
		writeErr := finish()
		closeErr := file.Close()
		var resultErr error
		if completed != nil {
			resultErr = completed()
		}
		return errors.Join(writeErr, closeErr, resultErr)
	}
	go func() {
		timer := time.NewTimer(duration)
		defer timer.Stop()
		select {
		case <-ctx.Done():
		case <-timer.C:
		case <-c.done:
			return
		}
		c.finish()
	}()
	return c, nil
}

func profileWriter(file *os.File, kind string) (func() error, error) {
	switch kind {
	case "cpu":
		if err := pprof.StartCPUProfile(file); err != nil {
			return nil, err
		}
		return func() error { pprof.StopCPUProfile(); return nil }, nil
	case "trace":
		if err := trace.Start(file); err != nil {
			return nil, err
		}
		return func() error { trace.Stop(); return nil }, nil
	case "allocs":
		return func() error { return pprof.Lookup("allocs").WriteTo(file, 0) }, nil
	case "block":
		runtime.SetBlockProfileRate(1)
		return func() error { runtime.SetBlockProfileRate(0); return pprof.Lookup("block").WriteTo(file, 0) }, nil
	default:
		return nil, errors.New("profile kind must be cpu, allocs, block, or trace")
	}
}

// Stop flushes the profile and returns the same result on subsequent calls.
func (c *Capture) Stop() error {
	if c == nil {
		return nil
	}
	c.finish()
	return c.err
}

func (c *Capture) finish() { c.once.Do(func() { c.err = c.stop(); close(c.done) }) }
