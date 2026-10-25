package plex

import (
	"context"
	"errors"
	"net/url"
	"strconv"
)

type Binding struct {
	Key   string `xml:"ratingKey,attr" json:"key"`
	GUID  string `xml:"guid,attr" json:"guid"`
	Title string `xml:"title,attr" json:"title"`
}
type PlexAdapter interface {
	Check(context.Context) (Capabilities, error)
	Scan(context.Context, string) error
	Bindings(context.Context, string) ([]Binding, error)
	LibraryHealth(context.Context, string) error
}

func validSection(section string) bool { n, e := strconv.Atoi(section); return e == nil && n > 0 }
func (c *Client) Scan(ctx context.Context, section string) error {
	if !validSection(section) {
		return errors.New("invalid Plex library section")
	}
	return c.get(ctx, "/library/sections/"+section+"/refresh", nil)
}
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
