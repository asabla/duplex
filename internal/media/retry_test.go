package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"duplex/internal/cache"
)

func TestRetryDelayBoundsAndHTTPDates(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	for _, test := range []struct {
		header string
		want   time.Duration
	}{
		{"", time.Second},
		{"invalid", time.Second},
		{"0", time.Second},
		{"-1", time.Second},
		{" 60 ", time.Minute},
		{"999999", maximumRetry},
		{"999999999999999999999999", maximumRetry},
		{now.Add(90 * time.Second).Format(http.TimeFormat), 90 * time.Second},
		{now.Add(-time.Minute).Format(http.TimeFormat), time.Second},
		{now.Add(time.Hour).Format(http.TimeFormat), maximumRetry},
	} {
		t.Run(test.header, func(t *testing.T) {
			if got := retryDelay(test.header, now); got != test.want {
				t.Fatal(got, test.want)
			}
		})
	}
}

func TestRetryPolicyBoundsAndPreservesLaterDeadlines(t *testing.T) {
	var policy RetryPolicy

	now := time.Now()
	policy.postpone("one", "60", now)
	policy.postpone("one", "1", now.Add(time.Second))

	if !policy.blocked("one", now.Add(59*time.Second)) || policy.blocked("one", now.Add(time.Minute)) {
		t.Fatal("shorter response changed the original delay")
	}

	for i := range retryEntries + 5 {
		policy.postpone(fmt.Sprint(i), "60", now.Add(time.Duration(i)*time.Millisecond))
	}

	if len(policy.entries) != retryEntries || policy.blocked("0", now.Add(time.Second)) {
		t.Fatal("retry memory or eviction bound changed", len(policy.entries))
	}

	policy.postpone("fresh", "1", now.Add(time.Hour))

	if len(policy.entries) != 1 {
		t.Fatal("expired retry state was retained")
	}
}

func TestRateLimitsShareDelaysAcrossReadersAndLocators(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			data := bytes.Repeat([]byte{37}, 3*ChunkSize)

			var (
				limited  atomic.Bool
				requests atomic.Int64
			)

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)

				if limited.Load() && r.URL.Query().Get("other") == "" {
					w.Header().Set("Retry-After", time.Now().Add(time.Minute).UTC().Format(http.TimeFormat))
					http.Error(w, "temporarily limited", status)

					return
				}

				w.Header().Set("ETag", `"immutable-fixture"`)
				http.ServeContent(w, r, "media", time.Time{}, bytes.NewReader(data))
			}))
			defer server.Close()

			chunks, err := cache.New(t.TempDir(), 8<<20)
			if err != nil {
				t.Fatal(err)
			}

			defer func() {
				if err := chunks.Close(); err != nil {
					t.Error(err)
				}
			}()

			var policy RetryPolicy

			one, err := policy.OpenVersionedHTTP(t.Context(), server.Client(), server.URL, "same:format", chunks, SourceVersion{})
			if err != nil {
				t.Fatal(err)
			}

			two, err := policy.OpenVersionedHTTP(t.Context(), server.Client(), server.URL+"?lease=2", "same:format", chunks, SourceVersion{})
			if err != nil {
				t.Fatal(err)
			}

			if _, err = one.ReadAt(t.Context(), make([]byte, 4), 0); err != nil {
				t.Fatal(err)
			}

			limited.Store(true)

			if _, err = one.ReadAt(t.Context(), make([]byte, 4), ChunkSize); !errors.Is(err, ErrDeferred) {
				t.Fatal("rate limit was not retryable", err)
			}

			before := requests.Load()

			var readers sync.WaitGroup

			for range 16 {
				readers.Go(func() {
					if _, err := two.ReadAt(t.Context(), make([]byte, 4), 2*ChunkSize); !errors.Is(err, ErrDeferred) {
						t.Error(err)
					}
				})
			}

			readers.Wait()

			if _, err = policy.OpenVersionedHTTP(t.Context(), server.Client(), server.URL+"?lease=3", "same:format", chunks, SourceVersion{}); !errors.Is(err, ErrDeferred) {
				t.Fatal("renewed locator bypassed the delay", err)
			}

			cached := make([]byte, 4)
			if n, err := two.ReadAt(t.Context(), cached, 0); err != nil || n != 4 || !bytes.Equal(cached, data[:4]) {
				t.Fatal("cached bytes were blocked", n, err)
			}

			ctx, cancel := context.WithCancel(t.Context())
			cancel()

			if _, err = two.ReadAt(ctx, cached, ChunkSize); !errors.Is(err, context.Canceled) {
				t.Fatal("retry delay hid cancellation", err)
			}

			if requests.Load() != before {
				t.Fatal("delayed readers made more requests", requests.Load()-before)
			}

			if _, err = policy.OpenVersionedHTTP(t.Context(), server.Client(), server.URL+"?other=1", "other:format", chunks, SourceVersion{}); err != nil {
				t.Fatal("unrelated source was blocked", err)
			}

			policy.mu.Lock()
			policy.entries[two.retryIdentity] = retryDeadline{until: time.Now().Add(-time.Second)}
			policy.mu.Unlock()
			limited.Store(false)

			if n, err := two.ReadAt(t.Context(), cached, ChunkSize); err != nil || n != 4 || !bytes.Equal(cached, data[:4]) {
				t.Fatal("source did not recover after its deadline", n, err)
			}
		})
	}
}

func TestHTTPFailureClassesAndCanceledProbes(t *testing.T) {
	for _, test := range []struct {
		status int
		kind   error
	}{
		{401, ErrExpired},
		{403, ErrExpired},
		{412, ErrChanged},
		{429, ErrDeferred},
		{503, ErrDeferred},
		{500, nil},
	} {
		t.Run(fmt.Sprint(test.status), func(t *testing.T) {
			var requests atomic.Int64

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				http.Error(w, "fixture rejected probe", test.status)
			}))
			defer server.Close()

			var policy RetryPolicy

			_, err := policy.OpenVersionedHTTP(t.Context(), server.Client(), server.URL, "identity", nil, SourceVersion{})
			if err == nil || (test.kind != nil && !errors.Is(err, test.kind)) || (test.kind == nil && errors.Is(err, ErrDeferred)) {
				t.Fatal("HTTP failure changed class", test.status, err)
			}

			ctx, cancel := context.WithCancel(t.Context())
			cancel()

			if _, err = policy.OpenVersionedHTTP(ctx, server.Client(), server.URL, "identity", nil, SourceVersion{}); !errors.Is(err, context.Canceled) || requests.Load() != 1 {
				t.Fatal("canceled probe made a request or hid cancellation", err, requests.Load())
			}
		})
	}
}
