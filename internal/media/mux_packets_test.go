package media

import (
	"encoding/json"
	"errors"
	"math/big"
	"testing"
)

type referencePacket struct {
	Stream   int    `json:"stream_index"`
	PTS      string `json:"pts_time"`
	DTS      string `json:"dts_time"`
	Duration string `json:"duration_time"`
	Hash     string `json:"data_hash"`
}

type probedPacket struct {
	referencePacket
	DecodeTicks *int64 `json:"dts"`
}

func packetSignatures(t *testing.T, path string) map[int][]referencePacket {
	t.Helper()

	data := run(t, "ffprobe", "-v", "error", "-show_packets", "-show_streams", "-show_data_hash", "sha256", "-show_entries", "packet=stream_index,pts_time,dts_time,dts,duration_time,data_hash:stream=index,time_base", "-of", "json", path)

	tracks, err := decodePacketSignatures(data)
	if err != nil {
		t.Fatal(err)
	}

	return tracks
}

func decodePacketSignatures(data []byte) (map[int][]referencePacket, error) {
	var probe struct {
		Packets []probedPacket `json:"packets"`
		Streams []struct {
			Index    int    `json:"index"`
			TimeBase string `json:"time_base"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, err
	}

	bases := map[int]string{}
	for _, stream := range probe.Streams {
		bases[stream.Index] = stream.TimeBase
	}

	packets := map[int][]probedPacket{}
	for _, packet := range probe.Packets {
		packets[packet.Stream] = append(packets[packet.Stream], packet)
	}

	result := map[int][]referencePacket{}

	for stream, track := range packets {
		for i, packet := range track {
			if packet.Duration == "" {
				duration, err := omittedPacketDuration(track, i, bases[stream])
				if err != nil {
					return nil, err
				}

				packet.Duration = duration
			}

			result[stream] = append(result[stream], packet.referencePacket)
		}
	}

	return result, nil
}

// FFprobe 6.1 can omit the first AAC duration even when adjacent DTS values
// establish it exactly. Only an absent, nonterminal duration is derived. Explicit
// durations, payload hashes, PTS and DTS remain part of the strict comparison.
func omittedPacketDuration(track []probedPacket, index int, timeBase string) (string, error) {
	if index+1 >= len(track) || track[index].DecodeTicks == nil || track[index+1].DecodeTicks == nil {
		return "", errors.New("packet duration is missing without adjacent decode timestamps")
	}

	base, valid := new(big.Rat).SetString(timeBase)
	if !valid || base.Sign() <= 0 {
		return "", errors.New("packet duration has no positive stream time base")
	}

	delta := new(big.Int).Sub(big.NewInt(*track[index+1].DecodeTicks), big.NewInt(*track[index].DecodeTicks))
	if delta.Sign() <= 0 {
		return "", errors.New("packet decode timestamps do not establish a positive duration")
	}

	return new(big.Rat).Mul(new(big.Rat).SetInt(delta), base).FloatString(6), nil
}

func TestPacketComparisonDerivesOnlyMissingNonterminalDurations(t *testing.T) {
	input := []byte(`{"streams":[{"index":0,"time_base":"1/48000"}],"packets":[{"stream_index":0,"dts":0,"pts_time":"0.000000","dts_time":"0.000000","data_hash":"first"},{"stream_index":0,"dts":1024,"pts_time":"0.021333","dts_time":"0.021333","duration_time":"0.005333","data_hash":"last"}]}`)

	tracks, err := decodePacketSignatures(input)
	if err != nil {
		t.Fatal(err)
	}

	if tracks[0][0].Duration != "0.021333" || tracks[0][1].Duration != "0.005333" || tracks[0][0].Hash != "first" || tracks[0][1].DTS != "0.021333" {
		t.Fatal("packet comparison changed an explicit field", tracks)
	}

	for _, input := range []string{
		`{`,
		`{"packets":[{"dts":0}]}`,
		`{"packets":[{}, {"dts":1024}]}`,
		`{"packets":[{"dts":0}, {"dts":1024}]}`,
		`{"streams":[{"index":0,"time_base":"0/1"}],"packets":[{"dts":0},{"dts":1024}]}`,
		`{"streams":[{"index":0,"time_base":"1/48000"}],"packets":[{"dts":1024},{"dts":0}]}`,
	} {
		if _, err = decodePacketSignatures([]byte(input)); err == nil {
			t.Fatal("accepted an unproven duration", input)
		}
	}
}
