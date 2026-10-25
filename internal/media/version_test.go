package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
)

func TestProviderVersionRequiresFreshRangeProofs(t *testing.T) {
	data := make([]byte, 1<<20)
	for i := range data {
		data[i] = byte(i % 251)
	}
	var dataMu sync.RWMutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dataMu.RLock()
		defer dataMu.RUnlock()
		var start, end int
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil || start < 0 || end >= len(data) {
			http.Error(w, "bad range", http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
		w.Header().Set("Content-Length", strconv.Itoa(end-start+1))
		w.WriteHeader(http.StatusPartialContent)
		if _, err := w.Write(data[start : end+1]); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	h := sha256.Sum256([]byte("authenticated provider object version"))
	version := SourceVersion{Key: hex.EncodeToString(h[:]), Size: int64(len(data))}
	open := func(lease string) (*HTTPSource, error) {
		return OpenVersionedHTTP(t.Context(), server.Client(), server.URL+"?lease="+lease, "video:format", nil, version)
	}
	a, err := open("one")
	if err != nil {
		t.Fatal(err)
	}
	b, err := open("two")
	if err != nil {
		t.Fatal(err)
	}
	if a.ID() != b.ID() || b.Requests.Load() != 4 {
		t.Fatal("reusable version not independently verified")
	}
	for _, offset := range []int{1, len(data) / 2, len(data) - 1} {
		dataMu.Lock()
		data[offset] ^= 1
		dataMu.Unlock()
		changed, err := open("changed")
		if err != nil {
			t.Fatal(err)
		}
		if changed.ID() == a.ID() {
			t.Fatal("changed sampled media reused old namespace")
		}
		dataMu.Lock()
		data[offset] ^= 1
		dataMu.Unlock()
	}
	version.Size++
	if _, err = open("wrong-length"); !errors.Is(err, ErrChanged) {
		t.Fatal("provider size not verified", err)
	}
	version.Size--
	version.Key = "not-a-version"
	if _, err = open("invalid"); err == nil {
		t.Fatal("malformed version accepted")
	}
	plainA, err := OpenHTTP(context.Background(), server.Client(), server.URL, "unversioned", nil)
	if err != nil {
		t.Fatal(err)
	}
	plainB, err := OpenHTTP(context.Background(), server.Client(), server.URL, "unversioned", nil)
	if err != nil {
		t.Fatal(err)
	}
	if plainA.ID() == plainB.ID() {
		t.Fatal("unverified sources reused namespace")
	}
}
