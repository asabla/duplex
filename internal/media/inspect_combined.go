package media

import (
	"context"

	"github.com/Eyevinn/mp4ff/mp4"
)

// InspectCombined checks finite MP4 sample tables and supported audio/video
// descriptions without fetching the full object. Decode acceptance is a release
// test; container checks are repeated for every selected source revision.
func InspectCombined(ctx context.Context, source Representation) (Details, error) {
	info := Details{Container: "mp4"}
	movie, err := readMoov(ctx, source)
	if err != nil {
		return info, err
	}
	if movie.Mvex != nil {
		return info, ErrUnsupported
	}
	if err = inspectCombinedTracks(movie.Traks, &info); err != nil {
		return info, err
	}
	if info.Height <= 0 || info.Width <= 0 || info.DurationSeconds <= 0 || info.DurationSeconds > 31*86400 {
		return info, ErrUnsupported
	}
	return info, nil
}

func inspectCombinedTracks(tracks []*mp4.TrakBox, info *Details) error {
	video, audio := false, false
	for _, track := range tracks {
		kind, err := combinedTrackKind(track)
		if err != nil {
			return err
		}
		switch track.Mdia.Hdlr.HandlerType {
		case "vide":
			if video || !videoCodec(kind) {
				return ErrUnsupported
			}
			video = true
			if err = combinedVideoDetails(track, info); err != nil {
				return err
			}
		case "soun":
			if audio || !audioCodec(kind) {
				return ErrUnsupported
			}
			audio = true
			info.AudioCodec = kind
		}
	}
	if !video || !audio {
		return ErrUnsupported
	}
	return nil
}

func combinedTrackKind(track *mp4.TrakBox) (string, error) {
	if track.Mdia == nil || track.Mdia.Hdlr == nil || track.Mdia.Minf == nil || track.Mdia.Minf.Stbl == nil {
		return "", ErrUnsupported
	}
	table := track.Mdia.Minf.Stbl
	if table.Stsd == nil || len(table.Stsd.Children) != 1 || table.Stsz == nil || table.Stsz.GetNrSamples() == 0 || table.Stts == nil {
		return "", ErrUnsupported
	}
	return table.Stsd.Children[0].Type(), nil
}

func combinedVideoDetails(track *mp4.TrakBox, info *Details) error {
	if track.Tkhd == nil || track.Mdia.Mdhd == nil || track.Mdia.Mdhd.Timescale == 0 {
		return ErrUnsupported
	}
	info.VideoCodec = track.Mdia.Minf.Stbl.Stsd.Children[0].Type()
	info.Width = int(track.Tkhd.Width >> 16)
	info.Height = int(track.Tkhd.Height >> 16)
	info.DurationSeconds = float64(track.Mdia.Mdhd.Duration) / float64(track.Mdia.Mdhd.Timescale)
	return nil
}

func videoCodec(codec string) bool {
	switch codec {
	case "avc1", "avc3", "av01", "vp09":
		return true
	default:
		return false
	}
}

func audioCodec(codec string) bool { return codec == "mp4a" || codec == "Opus" }
