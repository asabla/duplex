package plex

import (
	"context"
	"encoding/xml"
	"errors"
)

// Activity contains no media titles, user identities, or client details.
type Activity struct {
	Sessions int
	Scanning bool
}

// Activity reads server-wide sessions and, optionally, the dedicated scanner.
// Every reported session, including paused and unknown states, protects playback.
func (c *Client) Activity(ctx context.Context, section string) (Activity, error) {
	var response struct {
		XMLName xml.Name                     `xml:"MediaContainer"`
		Size    *int                         `xml:"size,attr"`
		Items   []struct{ XMLName xml.Name } `xml:",any"`
	}

	if err := c.get(ctx, "/status/sessions", &response); err != nil {
		return Activity{}, err
	}

	if response.Size == nil || *response.Size < 0 || *response.Size != len(response.Items) {
		return Activity{}, errors.New("plex did not report complete playback status")
	}

	activity := Activity{Sessions: *response.Size}
	if section == "" {
		return activity, nil
	}

	if !validSection(section) {
		return activity, errors.New("invalid Plex library section")
	}

	var err error

	activity.Scanning, err = c.libraryScanning(ctx, section)

	return activity, err
}
