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
	Tasks    int
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

	tasks, err := c.BackgroundTasks(ctx)
	if err != nil {
		return activity, err
	}

	activity.Tasks = tasks
	if section == "" {
		return activity, nil
	}

	if !validSection(section) {
		return activity, errors.New("invalid Plex library section")
	}

	activity.Scanning, err = c.libraryScanning(ctx, section)

	return activity, err
}

// BackgroundTasks counts reported PMS activities without retaining titles or
// identifiers. Basic analysis can remain active after the refreshing flag clears.
func (c *Client) BackgroundTasks(ctx context.Context) (int, error) {
	var response struct {
		XMLName xml.Name   `xml:"MediaContainer"`
		Size    *int       `xml:"size,attr"`
		Items   []struct{} `xml:"Activity"`
	}
	if err := c.get(ctx, "/activities", &response); err != nil {
		return 0, err
	}

	if response.Size != nil && (*response.Size < 0 || *response.Size != len(response.Items)) {
		return 0, errors.New("plex did not report complete activity status")
	}

	return len(response.Items), nil
}

// Scanning reports the dedicated scanner flag. It is not a process-idle check.
func (c *Client) Scanning(ctx context.Context, section string) (bool, error) {
	if !validSection(section) {
		return false, errors.New("invalid Plex library section")
	}

	return c.libraryScanning(ctx, section)
}
