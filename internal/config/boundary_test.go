package config

import "testing"

func TestUnlistedIdentityValidation(t *testing.T) {
	channel := Channel{ID: "UCaaaaaaaaaaaaaaaaaaaaaa", URL: "https://www.youtube.com/channel/UCaaaaaaaaaaaaaaaaaaaaaa", Limit: 50}
	for _, id := range []string{"aqz-KE-bpKQ", "aAZ09_-bpKQ"} {
		channel.Unlisted = []string{id}
		if err := validateChannels([]Channel{channel}); err != nil {
			t.Fatal(id, err)
		}
	}
	for _, id := range []string{"", "too-short", "aqz/KE-bpKQ", "aqz-KE-bpK?", "aqz-KE-bpKé"} {
		channel.Unlisted = []string{id}
		if err := validateChannels([]Channel{channel}); err == nil {
			t.Fatal("invalid unlisted ID accepted", id)
		}
	}
}

func TestPlexSettingsRequireAnOrigin(t *testing.T) {
	for _, raw := range []string{"", "http://plex:32400", "https://plex.example/"} {
		if err := validatePlex(Plex{URL: raw}); err != nil {
			t.Fatal(err)
		}
	}
	for _, raw := range []string{"https://user:secret@plex", "http://plex/library", "http://plex?token=secret", "http://plex#private", "file:///private", ":bad"} {
		if err := validatePlex(Plex{URL: raw}); err == nil {
			t.Fatal("unsafe origin accepted", raw)
		}
	}
	if err := (Selection{Mode: "all"}).ValidateUploads(); err != nil {
		t.Fatal(err)
	}
	if err := (Selection{Mode: "skip"}).ValidateUploads(); err == nil {
		t.Fatal("upload skip policy accepted")
	}
}
