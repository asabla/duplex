package admin

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"duplex/internal/app"
	"duplex/internal/cache"
	"duplex/internal/catalog"
	"duplex/internal/config"
)

func TestPaginationBoundsAndSearchLinks(t *testing.T) {
	for _, tc := range []struct {
		query                         string
		total, number, size, from, to int
	}{
		{"", 0, 1, 25, 0, 0},
		{"page=-1&per_page=999999", 26, 1, 25, 1, 25},
		{"page=999999999&per_page=50", 101, 3, 50, 101, 101},
		{"page=2&per_page=100", 201, 2, 100, 101, 200},
	} {
		q, err := url.ParseQuery(tc.query)
		if err != nil {
			t.Fatal(err)
		}
		p := paginate(q, "/catalogue", "Videos", tc.total)
		if p.Number != tc.number || p.Size != tc.size || p.From != tc.from || p.To != tc.to {
			t.Fatalf("%s: %+v", tc.query, p)
		}
	}
	q := url.Values{"q": {"title & + <source>"}, "page": {"2"}, "per_page": {"50"}}
	p := paginate(q, "/catalogue", "Videos", 101)
	for _, link := range []string{p.FirstURL, p.PreviousURL, p.NextURL, p.LastURL} {
		u, err := url.Parse(link)
		if err != nil || u.Query().Get("q") != q.Get("q") || u.Query().Get("per_page") != "50" {
			t.Fatalf("lost search or size: %q", link)
		}
	}
}

func TestListPagesAndChannelPolling(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	if err := config.Create(file, filepath.Join(dir, "state")); err != nil {
		t.Fatal(err)
	}
	m, err := config.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	snap := m.Snapshot()
	for i := 0; i < 30; i++ {
		id := fmt.Sprintf("UC%022d", i)
		snap.Config.Channels = append(snap.Config.Channels, config.Channel{ID: id, URL: "https://www.youtube.com/channel/" + id, Limit: 50, Enabled: true})
	}
	if err = m.Save(snap.Config, snap.Revision); err != nil {
		t.Fatal(err)
	}
	db, err := catalog.Open(filepath.Join(dir, "catalogue.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeTest(t, db)
	for i, ch := range snap.Config.Channels {
		if err = db.EnsureChannel(ctx, ch.ID, fmt.Sprintf("Channel %02d", i)); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := db.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 61; i++ {
		_, err = tx.ExecContext(ctx, `INSERT INTO videos(id,channel_id,title,search_title,state,updated,selected) VALUES(?,?,?,?,'recorded',0,1)`, fmt.Sprintf("video%06d", i), snap.Config.Channels[0].ID, fmt.Sprintf("Video %03d", i), fmt.Sprintf("video %03d", i))
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 103; i++ {
		_, err = tx.ExecContext(ctx, `INSERT INTO sync_jobs(channel_id,state,started) VALUES(?,'complete',?)`, fmt.Sprintf("job-channel-%03d", i), i)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	c, err := cache.New(filepath.Join(dir, "cache"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTest(t, c)
	s, err := New(&app.App{Config: m, Catalog: db, Cache: c}, &Auth{})
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string) string {
		t.Helper()
		w := httptest.NewRecorder()
		s.route(w, httptest.NewRequestWithContext(t.Context(), "GET", path, nil))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	for _, path := range []string{"/jobs?page=5", "/fragments/jobs?page=5"} {
		body := get(path)
		if !strings.Contains(body, "job-channel-000") || strings.Contains(body, "job-channel-003") || strings.Count(body, "Showing 101–103 of 103") != 2 {
			t.Fatalf("older jobs inaccessible or pagination missing at %s", path)
		}
	}
	for _, path := range []string{"/channels?page=2&per_page=25", "/fragments/channels?page=2&per_page=25"} {
		body := get(path)
		if strings.Count(body, "Showing 26–30 of 30") != 2 || !strings.Contains(body, "Channel 29") || strings.Contains(body, "Channel 24") || !strings.Contains(body, `hx-get="/fragments/channels?page=2&amp;per_page=25"`) {
			t.Fatalf("channel page or polling lost state at %s", path)
		}
	}
	body := get("/fragments/jobs?channel=job-channel-000&state=complete&sort=oldest&per_page=50")
	if !strings.Contains(body, "job-channel-000") || strings.Contains(body, "job-channel-001") || !strings.Contains(body, `hx-get="/fragments/jobs?channel=job-channel-000&amp;page=1&amp;per_page=50&amp;sort=oldest&amp;state=complete"`) {
		t.Fatal("jobs polling lost filters or read the wrong bounded result")
	}
	body = get("/catalogue?page=2&per_page=25")
	if strings.Count(body, "Showing 26–50 of 61") != 2 || !strings.Contains(body, "Video 025") || strings.Contains(body, "Video 024") || strings.Contains(body, "Video 050") {
		t.Fatal("video page includes wrong rows or lacks two controls")
	}
	body = get("/catalogue?q=Video+060&page=9999&per_page=50")
	if !strings.Contains(body, "Showing 1–1 of 1") || !strings.Contains(body, "Video 060") {
		t.Fatal("filtered page was not clamped to its result set")
	}
	if body = get("/catalogue?q=missing"); !strings.Contains(body, "Showing 0–0 of 0") || !strings.Contains(body, "No matching videos") {
		t.Fatal("empty page missing")
	}
}
