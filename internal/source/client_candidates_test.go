package source

import "testing"

func TestCandidatesKeepOriginalAudioForEachClient(t *testing.T) {
	formats := []Format{
		{ID: "401-tv", URL: "https://rr1.googlevideo.com/videoplayback?c=TVHTML5", Protocol: "https", Ext: "mp4", Height: 2160, VCodec: "av01", ACodec: "none"},
		{ID: "401-vision", URL: "https://rr1.googlevideo.com/videoplayback?c=VISIONOS", Protocol: "https", Ext: "mp4", Height: 2160, VCodec: "av01", ACodec: "none"},
		{ID: "140-tv", URL: "https://rr1.googlevideo.com/videoplayback?c=TVHTML5", Protocol: "https", Ext: "m4a", VCodec: "none", ACodec: "mp4a.40.2", Note: "English original", LanguagePreference: 10, ABR: 130},
		{ID: "140-vision-dub", URL: "https://rr1.googlevideo.com/videoplayback?c=VISIONOS", Protocol: "https", Ext: "m4a", VCodec: "none", ACodec: "mp4a.40.2", Note: "German", LanguagePreference: -1, ABR: 140},
		{ID: "140-vision", URL: "https://rr1.googlevideo.com/videoplayback?c=VISIONOS", Protocol: "https", Ext: "m4a", VCodec: "none", ACodec: "mp4a.40.2", Note: "English original", LanguagePreference: 10, ABR: 128},
	}
	candidates := Candidates(Metadata{Formats: formats}, 2160)
	if len(candidates) != 2 || candidates[0].Audio.ID != "140-tv" || candidates[1].Audio.ID != "140-vision" {
		t.Fatal("client pair or original language changed", candidates)
	}
	formats = formats[:4]
	candidates = Candidates(Metadata{Formats: formats}, 2160)
	if candidates[1].Audio.ID != "140-tv" {
		t.Fatal("dub replaced original language")
	}
	if matchingAudio([]Format{formats[2]}, Format{URL: ":invalid"}).ID != "140-tv" || mediaClientName(":invalid") != "" {
		t.Fatal("invalid client hint lost safe default")
	}
}
