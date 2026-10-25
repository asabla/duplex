package source

import "testing"

func TestCompleteNestedSnapshot(t *testing.T) {
	b := []byte(`{"channel_id":"UCSMOQeBJ2RAnuFungnQOxLg","channel":"Example","entries":[{"id":"uploads","entries":[{"id":"aqz-KE-bpKQ","title":"Upload"}]},{"id":"streams","entries":[{"id":"k0ILMhbWHNQ","title":"Replay"}]}]}`)

	c, e := decodeChannel(b)
	if e != nil || len(c.Entries) != 2 || c.Entries[0].Group == c.Entries[1].Group {
		t.Fatal(c, e)
	}

	if _, e = decodeChannel([]byte(`{"channel_id":"UCSMOQeBJ2RAnuFungnQOxLg","entries":[null]}`)); e == nil {
		t.Fatal("partial snapshot accepted")
	}
}

func TestMediaURLRestrictions(t *testing.T) {
	for _, u := range []string{"http://r1.googlevideo.com/a", "https://googlevideo.com.evil.test/a", "https://127.0.0.1/a", "https://user:secret@r1.googlevideo.com/a", "https://r1.googlevideo.com:444/a"} {
		if AllowedURL(u) {
			t.Fatal("accepted", u)
		}
	}

	if !AllowedURL("https://r1.googlevideo.com/videoplayback?expire=1") {
		t.Fatal("valid media host rejected")
	}
}

func TestBroadcastLifecycle(t *testing.T) {
	for _, v := range []struct {
		live, availability, want string
		duration                 float64
	}{
		{"is_upcoming", "public", "upcoming", 0}, {"is_live", "public", "active", 1}, {"post_live", "public", "processing", 1}, {"was_live", "public", "replay-ready", 60}, {"was_live", "private", "unavailable", 60}, {"was_live", "public", "unavailable", 0}, {"not_live", "unlisted", "recorded", 60},
	} {
		m := Metadata{LiveStatus: v.live, Availability: v.availability, Duration: v.duration}
		if m.State() != v.want {
			t.Fatalf("%s: %s", v.live, m.State())
		}
	}
}

func TestHighestQualityUsesOriginalAudio(t *testing.T) {
	m := Metadata{Formats: []Format{
		{ID: "4k", URL: "https://r1.googlevideo.com/v", Protocol: "https", Ext: "mp4", VCodec: "av01", ACodec: "none", Height: 2160},
		{ID: "1080", URL: "https://r1.googlevideo.com/v2", Protocol: "https", Ext: "mp4", VCodec: "avc1", ACodec: "none", Height: 1080},
		{ID: "8k", URL: "https://r1.googlevideo.com/v3", Protocol: "https", Ext: "mp4", VCodec: "av01", ACodec: "none", Height: 4320},
		{ID: "dub", URL: "https://r1.googlevideo.com/a", Protocol: "https", Ext: "m4a", VCodec: "none", ACodec: "mp4a", LanguagePreference: 10},
		{ID: "original", URL: "https://r1.googlevideo.com/a2", Protocol: "https", Ext: "m4a", VCodec: "none", ACodec: "mp4a", Note: "Original audio"},
	}}

	c := Candidates(m, 2160)
	if len(c) != 2 || c[0].Height != 2160 || c[0].Audio.ID != "original" {
		t.Fatal(c)
	}

	c = Candidates(m, 1080)
	if len(c) != 1 || c[0].Height != 1080 {
		t.Fatal(c)
	}
}
