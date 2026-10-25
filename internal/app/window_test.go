package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"duplex/internal/catalog"
	"duplex/internal/source"
)

type windowSource struct {
	failingSource
	items map[string]source.Metadata
	fail  string
}

func (s windowSource) Metadata(_ context.Context, id string) (source.Metadata, error) {
	if s.fail == id {
		return source.Metadata{}, errors.New("partial metadata failure")
	}
	return s.items[id], nil
}
func TestGlobalWindowPrecedesPackaging(t *testing.T) {
	snap := source.Channel{ID: "channel"}
	adapter := windowSource{items: map[string]source.Metadata{}}
	for group := range 2 {
		for n := range 4 {
			id := fmt.Sprintf("g%dn%d", group, n)
			snap.Entries = append(snap.Entries, source.Entry{ID: id, Group: group})
			adapter.items[id] = source.Metadata{ID: id, ChannelID: "channel", Duration: 60, UploadDate: fmt.Sprintf("202610%02d", 25-group*10-n)}
		}
	}
	called := []string{}
	prepare := func(m source.Metadata) (catalog.Prepared, error) {
		called = append(called, m.ID)
		return catalog.Prepared{Metadata: m, Revision: m.ID}, nil
	}
	out, e := prepareWindow(context.Background(), adapter, snap, 2, prepare)
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(called, []string{"g0n0", "g0n1"}) || len(out) != 2 {
		t.Fatal(called, out)
	}
	called = nil
	prepare = func(m source.Metadata) (catalog.Prepared, error) {
		called = append(called, m.ID)
		p := catalog.Prepared{Metadata: m}
		if m.ID != "g0n0" {
			p.Revision = m.ID
		}
		return p, nil
	}
	out, e = prepareWindow(context.Background(), adapter, snap, 2, prepare)
	if e != nil || len(out) != 3 || !reflect.DeepEqual(called, []string{"g0n0", "g0n1", "g0n2"}) {
		t.Fatal(called, out, e)
	}
	// An older tab can still contain a newer fallback than a later upload. The
	// complete metadata window is ranked before expensive representation work.
	adapter.fail = "g1n0"
	called = nil
	if _, e = prepareWindow(context.Background(), adapter, snap, 2, prepare); e == nil || len(called) != 0 {
		t.Fatal("partial metadata snapshot prepared media", called, e)
	}
}
