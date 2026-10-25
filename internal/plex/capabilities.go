package plex

import (
	"context"
	"errors"
	"strconv"
	"strings"
)

// Capabilities records observed server features rather than inferred version support.
type Capabilities struct {
	Version   string `json:"version"`
	Claimed   bool   `json:"claimed"`
	NFOSeries bool   `json:"nfo_series"`
	Provider  string `json:"provider,omitempty"`
}

// Check verifies the minimum server version and the advertised NFO series agent.
func (c *Client) Check(ctx context.Context) (Capabilities, error) {
	var identity struct {
		Version string `xml:"version,attr"`
		Claimed int    `xml:"claimed,attr"`
	}
	var result Capabilities
	if err := c.get(ctx, "/identity", &identity); err != nil {
		return result, err
	}
	result.Version = identity.Version
	result.Claimed = identity.Claimed == 1
	numbers := strings.Split(strings.Split(identity.Version, "-")[0], ".")
	if len(numbers) < 3 {
		return result, errors.New("unrecognized Plex version")
	}
	parts := [3]int{}
	for n := range 3 {
		v, err := strconv.Atoi(numbers[n])
		if err != nil {
			return result, errors.New("unrecognized Plex version")
		}
		parts[n] = v
	}
	if parts[0] < 1 || (parts[0] == 1 && (parts[1] < 43 || (parts[1] == 43 && parts[2] < 1))) {
		return result, errors.New("plex 1.43.1 or newer is required")
	}
	var providers struct {
		Items []struct {
			ID    string `xml:"identifier,attr"`
			Title string `xml:"title,attr"`
			Types []struct {
				Type int `xml:"type,attr"`
			} `xml:"MetadataType"`
		} `xml:"MetadataAgentProvider"`
	}
	if err := c.get(ctx, "/media/providers/metadata", &providers); err != nil {
		return result, err
	}
	for _, p := range providers.Items {
		if !strings.Contains(strings.ToLower(p.ID+" "+p.Title), "nfo") {
			continue
		}
		for _, t := range p.Types {
			if t.Type == 2 {
				result.NFOSeries = true
				result.Provider = p.ID
				return result, nil
			}
		}
	}
	return result, ErrMissingNFO
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
