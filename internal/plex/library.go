package plex

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
)

// Binding identifies a Plex item without tying its key to a filesystem path.
type Binding struct {
	Key   string `xml:"ratingKey,attr" json:"key"`
	GUID  string `xml:"guid,attr" json:"guid"`
	Title string `xml:"title,attr" json:"title"`
}

// PlexAdapter defines the library integration contract used by duplex.
type PlexAdapter interface {
	Check(context.Context) (Capabilities, error)
	Scan(context.Context, string) error
	Bindings(context.Context, string) ([]Binding, error)
	LibraryHealth(context.Context, string) error
}

func validSection(section string) bool { n, e := strconv.Atoi(section); return e == nil && n > 0 }

// Scan requests a refresh of the configured library section.
func (c *Client) Scan(ctx context.Context, section string) error {
	if !validSection(section) {
		return errors.New("invalid Plex library section")
	}
	return c.get(ctx, "/library/sections/"+section+"/refresh", nil)
}

// Bindings reads the episode identifiers exposed by one library section.
func (c *Client) Bindings(ctx context.Context, section string) ([]Binding, error) {
	if !validSection(section) {
		return nil, errors.New("invalid Plex library section")
	}
	var out struct {
		Items []Binding `xml:"Video"`
	}
	e := c.get(ctx, "/library/sections/"+section+"/allLeaves", &out)
	return out.Items, e
}

func (c *Client) LibraryHealth(ctx context.Context, section string) error {
	if !validSection(section) {
		return errors.New("configure a dedicated NFO series library section")
	}
	var out struct {
		Libraries []struct {
			Key   string `xml:"key,attr"`
			Agent string `xml:"agent,attr"`
		} `xml:"Directory"`
	}
	if e := c.get(ctx, "/library/sections", &out); e != nil {
		return e
	}
	for _, lib := range out.Libraries {
		if lib.Key == section {
			if lib.Agent != "tv.plex.agents.nfo.series" {
				return errors.New("dedicated library must use Plex NFO Series")
			}
			var prefs struct {
				Items []struct {
					ID    string `xml:"id,attr"`
					Value string `xml:"value,attr"`
				} `xml:"Setting"`
			}
			if e := c.get(ctx, "/:/prefs", &prefs); e != nil {
				return e
			}
			trashVerified := false
			for _, p := range prefs.Items {
				if p.ID == "autoEmptyTrash" {
					if p.Value != "0" {
						return errors.New("disable automatic trash emptying before duplex scans")
					}
					trashVerified = true
				}
			}
			if !trashVerified {
				return errors.New("Plex automatic trash setting could not be verified")
			}
			prefs.Items = nil
			if e := c.get(ctx, "/library/sections/"+section+"/prefs", &prefs); e != nil {
				return e
			}
			required := map[string]bool{"enableBIFGeneration": false, "enableIntroMarkerGeneration": false, "enableCreditsMarkerGeneration": false, "enableLoudnessAnalysis": false}
			for _, p := range prefs.Items {
				if _, ok := required[p.ID]; ok {
					if p.Value != "false" && p.Value != "0" {
						return errors.New("disable optional thumbnail, intro, credits and loudness analysis in the dedicated Plex library before scans")
					}
					required[p.ID] = true
				}
			}
			for _, verified := range required {
				if !verified {
					return errors.New("Plex optional library analysis settings could not be verified")
				}
			}
			return nil
		}
	}
	return errors.New("dedicated Plex library does not exist")
}

// SourceID extracts the canonical video ID from a year episode NFO GUID.
func SourceID(guid string) string {
	u, e := url.Parse(guid)
	if e != nil || u.Scheme != "tv.plex.agents.nfo.series" || u.Host != "episode" {
		return ""
	}
	s, e := url.PathUnescape(u.EscapedPath())
	if e != nil {
		return ""
	}
	const prefix = "/youtube-video:"
	if len(s) > len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):]
	}
	return ""
}

// OccurrenceID includes playlist scope so shared videos keep separate progress.
func OccurrenceID(guid string) string {
	u, err := url.Parse(guid)
	if err != nil || u.Scheme != "tv.plex.agents.nfo.series" || u.Host != "episode" {
		return ""
	}
	identity, err := url.PathUnescape(u.EscapedPath())
	if err != nil {
		return ""
	}
	identity = strings.TrimPrefix(identity, "/")
	if strings.HasPrefix(identity, "youtube-video:") || strings.HasPrefix(identity, "youtube-playlist:") {
		return identity
	}
	return ""
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
