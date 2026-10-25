package source

import (
	"net/url"
	"sort"
	"strings"
)

// Candidate pairs a supported video track with original-language audio, or a combined file.
type Candidate struct {
	Video    Format
	Audio    *Format
	Height   int
	Combined bool
}

// Candidates ranks finite supported video and original-language audio combinations.
func Candidates(m Metadata, maxHeight int) []Candidate {
	audio, videos, combined := candidateFormats(m.Formats, maxHeight)
	sort.SliceStable(audio, func(i, j int) bool {
		a, b := audio[i], audio[j]
		ao, bo := strings.Contains(strings.ToLower(a.Note), "original"), strings.Contains(strings.ToLower(b.Note), "original")
		if ao != bo {
			return ao
		}
		if a.LanguagePreference != b.LanguagePreference {
			return a.LanguagePreference > b.LanguagePreference
		}
		aacA, aacB := strings.HasPrefix(a.ACodec, "mp4a"), strings.HasPrefix(b.ACodec, "mp4a")
		if aacA != aacB {
			return aacA
		}
		return a.ABR > b.ABR
	})
	sort.SliceStable(videos, func(i, j int) bool {
		if videos[i].Height != videos[j].Height {
			return videos[i].Height > videos[j].Height
		}
		return videos[i].FPS > videos[j].FPS
	})
	result := []Candidate{}
	if len(audio) > 0 {
		for _, v := range videos {
			result = append(result, Candidate{Video: v, Audio: matchingAudio(audio, v), Height: v.Height})
		}
	}
	for _, v := range combined {
		result = append(result, Candidate{Video: v, Height: v.Height, Combined: true})
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Height != result[j].Height {
			return result[i].Height > result[j].Height
		}
		return result[i].Combined
	})
	return result
}

// matchingAudio keeps the preferred language and codec while choosing the same
// resolver client as the video. One rejected client must not hide a usable pair.
func matchingAudio(audio []Format, video Format) *Format {
	best := &audio[0]
	client := mediaClientName(video.URL)
	if client == "" {
		return best
	}
	for i := range audio {
		candidate := &audio[i]
		sameOriginal := strings.Contains(strings.ToLower(candidate.Note), "original") == strings.Contains(strings.ToLower(best.Note), "original")
		sameCodec := strings.HasPrefix(candidate.ACodec, "mp4a") == strings.HasPrefix(best.ACodec, "mp4a")
		if sameOriginal && sameCodec && candidate.LanguagePreference == best.LanguagePreference && mediaClientName(candidate.URL) == client {
			return candidate
		}
	}
	return best
}

func mediaClientName(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return parsed.Query().Get("c")
}

func candidateFormats(formats []Format, maxHeight int) (audio, videos, combined []Format) {
	for _, f := range formats {
		if f.Protocol != "https" || !AllowedURL(f.URL) || (f.Ext != "mp4" && f.Ext != "m4a") {
			continue
		}
		switch {
		case f.VCodec == "none" && (strings.HasPrefix(f.ACodec, "mp4a") || f.ACodec == "opus"):
			audio = append(audio, f)
		case f.Height > 0 && f.Height <= maxHeight && supportedVideoCodec(f.VCodec):
			if f.ACodec == "none" {
				videos = append(videos, f)
			} else if strings.HasPrefix(f.ACodec, "mp4a") {
				combined = append(combined, f)
			}
		}
	}
	return audio, videos, combined
}

func supportedVideoCodec(codec string) bool {
	return strings.HasPrefix(codec, "avc") || strings.HasPrefix(codec, "av01") || strings.HasPrefix(codec, "vp09")
}
