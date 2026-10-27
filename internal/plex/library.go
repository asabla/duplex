package plex

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Binding identifies a Plex item without tying its key to a filesystem path.
type Binding struct {
	Index int    `xml:"index,attr" json:"index"`
	Key   string `xml:"ratingKey,attr" json:"key"`
	GUID  string `xml:"guid,attr" json:"guid"`
	Title string `xml:"title,attr" json:"title"`
}

// SetEpisodeOrder edits only a known item's display index. Its scanner path and
// NFO identity stay fixed, so Plex does not transfer another item's watch state.
func (c *Client) SetEpisodeOrder(ctx context.Context, section, key string, index int) error {
	if !validSection(section) || !validSection(key) || index < 1 || int64(index) > 1<<31-1 {
		return errors.New("invalid Plex episode order")
	}

	query := url.Values{"type": {"4"}, "id": {key}, "index.value": {strconv.Itoa(index)}}

	return c.request(ctx, http.MethodPut, "/library/sections/"+section+"/all?"+query.Encode(), nil)
}

// WaitScan lets the asynchronously requested scanner finish before bindings are
// read. The caller owns the deadline; a long scan remains visible as incomplete.
func (c *Client) WaitScan(ctx context.Context, section string) error {
	if !validSection(section) {
		return errors.New("invalid Plex library section")
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}

		busy, err := c.libraryScanning(ctx, section)
		if err != nil || !busy {
			return err
		}
	}
}

// WaitIdle checks immediately before a targeted metadata update. After starting
// a new asynchronous scan, callers must use WaitScan to retain its initial grace.
func (c *Client) WaitIdle(ctx context.Context, section string) error {
	if !validSection(section) {
		return errors.New("invalid Plex library section")
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	busy, err := c.libraryScanning(ctx, section)
	if err != nil || !busy {
		return err
	}

	return c.WaitScan(ctx, section)
}

func (c *Client) libraryScanning(ctx context.Context, section string) (bool, error) {
	var response struct {
		Items []struct {
			Key        string `xml:"key,attr"`
			Refreshing string `xml:"refreshing,attr"`
		} `xml:"Directory"`
	}
	if err := c.get(ctx, "/library/sections", &response); err != nil {
		return false, err
	}

	for _, item := range response.Items {
		if item.Key == section {
			if item.Refreshing == "0" {
				return false, nil
			}

			if item.Refreshing == "1" {
				return true, nil
			}

			return false, errors.New("plex did not report library scanner state")
		}
	}

	return false, errors.New("dedicated Plex library does not exist")
}

// PlexAdapter defines the library integration contract used by duplex.
type PlexAdapter interface { //nolint:revive // Keep the architecture interface name explicit at the Plex boundary.
	Check(context.Context) (Capabilities, error)
	Scan(context.Context, string) error
	Bindings(context.Context, string) ([]Binding, error)
	LibraryHealth(context.Context, string) error
}

func validSection(section string) bool {
	n, e := strconv.Atoi(section)
	return e == nil && n > 0
}

// Scan requests a refresh of the configured library section.
func (c *Client) Scan(ctx context.Context, section string) error {
	if !validSection(section) {
		return errors.New("invalid Plex library section")
	}

	return c.get(ctx, "/library/sections/"+section+"/refresh", nil)
}

// Bindings reads the episode identifiers exposed by one library section.
func (c *Client) Bindings(ctx context.Context, section string) ([]Binding, error) {
	items := []Binding{}
	err := c.WalkBindings(ctx, section, func(batch []Binding) error {
		items = append(items, batch...)
		return nil
	})

	return items, err
}

// WalkBindings reads bounded Plex pages and does not retain the complete library.
func (c *Client) WalkBindings(ctx context.Context, section string, consume func([]Binding) error) error {
	if !validSection(section) {
		return errors.New("invalid Plex library section")
	}

	const pageSize = 100

	for start := 0; ; {
		var out struct {
			Offset int       `xml:"offset,attr"`
			Total  int       `xml:"totalSize,attr"`
			Items  []Binding `xml:"Video"`
		}

		path := "/library/sections/" + section + "/allLeaves?X-Plex-Container-Start=" + strconv.Itoa(start) + "&X-Plex-Container-Size=" + strconv.Itoa(pageSize)
		if err := c.get(ctx, path, &out); err != nil {
			return err
		}

		if len(out.Items) > pageSize || out.Offset != start {
			return errors.New("plex did not honor bounded library pagination")
		}

		if err := consume(out.Items); err != nil {
			return err
		}

		if len(out.Items) < pageSize || (out.Total > 0 && start >= out.Total-len(out.Items)) {
			return nil
		}

		if start > int(^uint(0)>>1)-len(out.Items) {
			return errors.New("plex library page offset overflow")
		}

		start += len(out.Items)
	}
}

// RefreshShow targets one NFO show after local poster changes. A show which has
// not yet been scanned is picked up by the next normal library scan.
func (c *Client) RefreshShow(ctx context.Context, section, channel string) error {
	if !validSection(section) {
		return errors.New("invalid Plex library section")
	}

	var shows struct {
		Items []Binding `xml:"Directory"`
	}
	if err := c.get(ctx, "/library/sections/"+section+"/all?type=2", &shows); err != nil {
		return err
	}

	for _, show := range shows.Items {
		u, err := url.Parse(show.GUID)
		if err != nil {
			continue
		}

		if u.Scheme != "tv.plex.agents.nfo.series" || u.Host != "show" {
			continue
		}

		id, err := url.PathUnescape(u.EscapedPath())
		if err != nil {
			return err
		}

		if strings.TrimPrefix(id, "/") != "youtube-channel:"+channel {
			continue
		}

		if _, err = strconv.ParseUint(show.Key, 10, 64); err != nil {
			return errors.New("invalid Plex show key")
		}

		return c.request(ctx, "PUT", "/library/metadata/"+show.Key+"/refresh", nil)
	}

	return nil
}
