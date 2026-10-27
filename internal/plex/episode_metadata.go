package plex

import (
	"context"
	"errors"
	"net/http"
)

// RefreshEpisode verifies library membership and occurrence identity before a
// targeted refresh. It never changes indexes, GUIDs, or watch state.
func (c *Client) RefreshEpisode(ctx context.Context, section, key, occurrence string) error {
	if !validSection(section) || !validSection(key) || occurrence == "" {
		return errors.New("invalid Plex episode metadata target")
	}

	var response struct {
		Items []struct {
			Section string `xml:"librarySectionID,attr"`
			Type    string `xml:"type,attr"`
			GUID    string `xml:"guid,attr"`
		} `xml:"Video"`
	}

	path := "/library/metadata/" + key
	if err := c.get(ctx, path, &response); err != nil {
		return err
	}

	if len(response.Items) != 1 {
		return errors.New("plex episode metadata target is ambiguous")
	}

	item := response.Items[0]
	if item.Section != section || item.Type != "episode" || OccurrenceID(item.GUID) != occurrence {
		return errors.New("plex episode metadata target changed")
	}

	return c.request(ctx, http.MethodPut, path+"/refresh", nil)
}
