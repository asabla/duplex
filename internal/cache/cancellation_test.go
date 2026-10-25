package cache

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type observedWaitContext struct {
	context.Context
	once    sync.Once
	waiting chan struct{}
}

func (c *observedWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func TestCancelledFetchDoesNotFailAnotherReader(t *testing.T) {
	store, err := New(t.TempDir(), 4096)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTest(t, store)
	parent, stop := context.WithTimeout(t.Context(), 3*time.Second)
	defer stop()
	leader, cancel := context.WithCancel(parent)
	defer cancel()
	entered := make(chan struct{})
	leaderResult := make(chan error, 1)
	var fetches atomic.Int32
	go func() {
		_, release, failure := store.Get(leader, "shared", 0, 100, func(ctx context.Context) ([]byte, error) {
			fetches.Add(1)
			close(entered)
			<-ctx.Done()
			return nil, ctx.Err()
		})
		if release != nil {
			release()
		}
		leaderResult <- failure
	}()
	<-entered
	follower := &observedWaitContext{Context: parent, waiting: make(chan struct{})}
	followerResult := make(chan error, 1)
	go func() {
		data, release, failure := store.Get(follower, "shared", 0, 100, func(context.Context) ([]byte, error) {
			fetches.Add(1)
			return make([]byte, 100), nil
		})
		if release != nil {
			release()
		}
		if failure == nil && len(data) != 100 {
			failure = errors.New("short shared read")
		}
		followerResult <- failure
	}()
	<-follower.waiting
	cancel()
	if err = <-leaderResult; !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled reader did not stop", err)
	}
	if err = <-followerResult; err != nil {
		t.Fatal("independent reader inherited another reader's cancellation", err)
	}
	if fetches.Load() != 2 {
		t.Fatal("unexpected retry count", fetches.Load())
	}
	if got := store.Stats(); got.Allocated != 4096 || got.Chunks != 1 {
		t.Fatal("retry leaked its reservation", got)
	}
}
