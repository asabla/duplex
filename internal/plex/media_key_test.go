package plex

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestActivityRetainsOnlyOpaqueManagedMediaKeys(t *testing.T) {
	name := "Channel-UC123 - S2026E0007.mp4"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status/sessions" {
			writeTestResponse(t, w, []byte(`<MediaContainer size="2"><Video><Media><Part file="/duplex/Channel-UC123/Season 2026/`+name+`"/></Media></Video><Video><Media><Part file="/private/Other title.mkv"/></Media></Video></MediaContainer>`))
			return
		}

		writeTestResponse(t, w, []byte(`<MediaContainer size="0"/>`))
	}))
	defer server.Close()

	client, err := New(server.URL, "")
	if err != nil {
		t.Fatal(err)
	}

	activity, err := client.Activity(t.Context(), "")
	if err != nil || len(activity.MediaKeys) != 1 || activity.MediaKeys[0] != MediaKey(name) {
		t.Fatal(activity, err)
	}

	if MediaKey(`C:\library\`+name) != MediaKey("/other/root/"+name) || len(MediaKey(name)) != 64 {
		t.Fatal("mount-root matching failed")
	}

	if MediaKey("other.mp4") != "" || MediaKey("Channel-test.txt") != "" {
		t.Fatal("non-managed title retained")
	}
}
