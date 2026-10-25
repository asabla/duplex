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
type PlexAdapter interface { //nolint:revive // Keep the architecture interface name explicit at the Plex boundary.
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
	items := []Binding{}
	err := c.WalkBindings(ctx, section, func(batch []Binding) error { items = append(items, batch...); return nil })
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

// LibraryHealth verifies the NFO agent, history retention, local assets, and season titles.
func (c *Client) LibraryHealth(ctx context.Context, section string) error {
	if !validSection(section) {
		return errors.New("configure a dedicated NFO series library section")
	}
	if err := c.requireNFOSection(ctx, section); err != nil {
		return err
	}
	settings, err := c.settings(ctx, "/:/prefs")
	if err != nil {
		return err
	}
	if !settingIs(settings, "autoEmptyTrash", false) {
		return errors.New("disable automatic trash emptying before duplex scans")
	}
	settings, err = c.settings(ctx, "/library/sections/"+section+"/prefs")
	if err != nil {
		return err
	}
	for _, name := range []string{"enableBIFGeneration", "enableIntroMarkerGeneration", "enableCreditsMarkerGeneration", "enableLoudnessAnalysis"} {
		if !settingIs(settings, name, false) {
			return errors.New("disable optional thumbnail, intro, credits and loudness analysis in the dedicated Plex library before scans")
		}
	}
	if !settingIs(settings, "useLocalAssets", true) || !settingIs(settings, "useSeasonTitles", true) {
		return errors.New("enable local assets and season titles in the dedicated Plex library before scans")
	}
	return nil
}

func (c *Client) requireNFOSection(ctx context.Context, section string) error {
	var response struct {
		Libraries []struct {
			Key   string `xml:"key,attr"`
			Agent string `xml:"agent,attr"`
		} `xml:"Directory"`
	}
	if err := c.get(ctx, "/library/sections", &response); err != nil {
		return err
	}
	for _, library := range response.Libraries {
		if library.Key != section {
			continue
		}
		if library.Agent != "tv.plex.agents.nfo.series" {
			return errors.New("dedicated library must use Plex NFO Series")
		}
		return nil
	}
	return errors.New("dedicated Plex library does not exist")
}

func (c *Client) settings(ctx context.Context, path string) (map[string]string, error) {
	var response struct {
		Items []struct {
			ID    string `xml:"id,attr"`
			Value string `xml:"value,attr"`
		} `xml:"Setting"`
	}
	if err := c.get(ctx, path, &response); err != nil {
		return nil, err
	}
	result := map[string]string{}
	for _, setting := range response.Items {
		result[setting.ID] = setting.Value
	}
	return result, nil
}

func settingIs(settings map[string]string, name string, wanted bool) bool {
	value, exists := settings[name]
	if !exists {
		return false
	}
	if wanted {
		return value == "true" || value == "1"
	}
	return value == "false" || value == "0"
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
