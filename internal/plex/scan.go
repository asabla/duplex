package plex

import (
	"context"
	"errors"
	"net/url"
	"path"
	"strings"
)

// ScanChannels narrows a single-channel batch using Plex's own library location.
// Multiple channels or roots use one section request, rather than a request storm.
func (c *Client) ScanChannels(ctx context.Context, section string, channels []string) error {
	if !validSection(section) {
		return errors.New("invalid Plex library section")
	}

	if len(channels) != 1 {
		return c.Scan(ctx, section)
	}

	channel := channels[0]
	if channel == "" || strings.ContainsAny(channel, "/\\\x00") || channel == "." || channel == ".." {
		return errors.New("invalid Plex channel scan")
	}

	root, err := c.singleLibraryRoot(ctx, section)
	if err != nil {
		return err
	}

	if root == "" {
		return c.Scan(ctx, section)
	}

	query := url.Values{"path": {path.Join(root, "Channel-"+channel)}}

	return c.requestScan(ctx, section, "?"+query.Encode())
}

func (c *Client) singleLibraryRoot(ctx context.Context, section string) (string, error) {
	var response struct {
		Items []struct {
			Key       string `xml:"key,attr"`
			Locations []struct {
				Path string `xml:"path,attr"`
			} `xml:"Location"`
		} `xml:"Directory"`
	}
	if err := c.get(ctx, "/library/sections", &response); err != nil {
		return "", err
	}

	for _, item := range response.Items {
		if item.Key != section {
			continue
		}

		if len(item.Locations) != 1 {
			return "", nil
		}

		root := item.Locations[0].Path
		if !path.IsAbs(root) || strings.ContainsAny(root, "\\\x00") {
			return "", nil
		}

		return path.Clean(root), nil
	}

	return "", errors.New("dedicated Plex library does not exist")
}

// Counters describe bounded request outcomes and contain no paths or item IDs.
func (c *Client) requestScan(ctx context.Context, section, query string) error {
	if c.Count != nil {
		c.Count("plex.scan.requests", 1)

		if query == "" {
			c.Count("plex.scan.section", 1)
		} else {
			c.Count("plex.scan.scoped", 1)
		}
	}

	err := c.get(ctx, "/library/sections/"+section+"/refresh"+query, nil)

	outcome := "plex.scan.accepted"
	if err != nil {
		outcome = "plex.scan.failed"
	}

	if errors.Is(err, ErrRequestUncertain) {
		outcome = "plex.scan.uncertain"
	}

	if c.Count != nil {
		c.Count(outcome, 1)
	}

	return err
}
