package source

import (
	"encoding/json"
	"errors"
	"strings"
)

type listing struct {
	ID         string            `json:"id"`
	ChannelID  string            `json:"channel_id"`
	Channel    string            `json:"channel"`
	Title      string            `json:"title"`
	Entries    []json.RawMessage `json:"entries"`
	Thumbnails []ArtworkImage    `json:"thumbnails"`
}

func decodeChannel(b []byte) (Channel, error) {
	var l listing
	if json.Unmarshal(b, &l) != nil {
		return Channel{}, errors.New("invalid channel response")
	}

	id := l.ChannelID
	if id == "" {
		id = l.ID
	}

	if len(id) != 24 || !strings.HasPrefix(id, "UC") {
		return Channel{}, errors.New("source did not return a stable channel ID")
	}

	title := l.Channel
	if title == "" {
		title = l.Title
	}

	result := Channel{ID: id, Title: title}
	result.Avatar, result.Banner = channelImages(l.Thumbnails)

	var err error

	group := 0

	result.Entries, err = flattenChannel(l.Entries, 0, &group)
	if err != nil {
		return Channel{}, err
	}

	seen := map[string]bool{}

	entries := result.Entries[:0]
	for _, e := range result.Entries {
		if !seen[e.ID] {
			entries = append(entries, e)
			seen[e.ID] = true
		}
	}

	result.Entries = entries

	return result, nil
}

func channelImages(images []ArtworkImage) (avatar, banner string) {
	for _, image := range images {
		if !AllowedURL(image.URL) {
			continue
		}

		if strings.Contains(image.ID, "avatar") || (image.Width > 0 && image.Width == image.Height) {
			avatar = image.URL
		}

		if strings.Contains(image.ID, "banner") || (image.Height > 0 && image.Width/image.Height >= 3) {
			banner = image.URL
		}
	}

	return avatar, banner
}

func flattenChannel(items []json.RawMessage, group int, counter *int) ([]Entry, error) {
	var entries []Entry

	for _, raw := range items {
		if string(raw) == "null" {
			return nil, errors.New("channel snapshot contains an inaccessible entry")
		}

		var item struct {
			Entry
			Entries []json.RawMessage `json:"entries"`
		}
		if json.Unmarshal(raw, &item) != nil {
			return nil, errors.New("invalid channel entry")
		}

		switch {
		case item.Entries != nil:
			*counter++

			children, err := flattenChannel(item.Entries, *counter, counter)
			if err != nil {
				return nil, err
			}

			entries = append(entries, children...)
		case videoID.MatchString(item.ID):
			item.Group = group
			entries = append(entries, item.Entry)
		default:
			return nil, errors.New("incomplete channel snapshot")
		}
	}

	return entries, nil
}

// ArtworkImage describes a public channel or playlist image candidate.
type ArtworkImage struct {
	ID     string `json:"id"`
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

func largestImage(images []ArtworkImage) string {
	best := ""
	area := int64(-1)

	for _, image := range images {
		if !AllowedURL(image.URL) {
			continue
		}

		n := int64(image.Width) * int64(image.Height)
		if n >= area {
			area = n
			best = image.URL
		}
	}

	return best
}
