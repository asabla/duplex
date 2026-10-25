// Package plex checks the actual server capability contract. A version number
// alone does not prove that an NFO metadata agent is available.
package plex

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ErrMissingNFO means the actual server does not advertise an NFO series agent.
var ErrMissingNFO = errors.New("plex server does not expose an NFO series provider; metadata acceptance gate failed")

// Client sends authenticated requests to one configured Plex origin without redirects.
type Client struct {
	base  *url.URL
	token string
	http  *http.Client
}

// Capabilities records observed server features rather than inferred version support.
type Capabilities struct {
	Version   string `json:"version"`
	Claimed   bool   `json:"claimed"`
	NFOSeries bool   `json:"nfo_series"`
	Provider  string `json:"provider,omitempty"`
}

// New validates a Plex origin and keeps its token out of URLs and diagnostics.
func New(base, token string) (*Client, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("plex URL must be an HTTP(S) origin without embedded credentials")
	}
	return &Client{base: u, token: strings.TrimSpace(token), http: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return errors.New("plex redirects are not permitted") }}}, nil
}

func (c *Client) get(ctx context.Context, path string, result any) error {
	return c.request(ctx, http.MethodGet, path, result)
}

func (c *Client) request(ctx context.Context, method, path string, result any) (failure error) {
	relative, err := url.ParseRequestURI(path)
	if err != nil || relative.IsAbs() || relative.Host != "" {
		return errors.New("invalid Plex request path")
	}
	u := c.base.ResolveReference(relative)
	req, err := http.NewRequestWithContext(ctx, method, u.String(), nil)
	if err != nil {
		return errors.New("invalid Plex request")
	}
	if c.token != "" {
		req.Header.Set("X-Plex-Token", c.token)
	}
	req.Header.Set("X-Plex-Product", "duplex")
	req.Header.Set("X-Plex-Client-Identifier", "duplex-qualification")
	r, err := c.http.Do(req)
	if err != nil {
		return errors.New("plex request failed")
	}
	defer func() { failure = errors.Join(failure, r.Body.Close()) }()
	if r.StatusCode < 200 || r.StatusCode > 299 {
		return fmt.Errorf("plex request rejected (HTTP %d)", r.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, 16*1024*1024+1))
	if err != nil || len(b) > 16*1024*1024 {
		return errors.New("invalid Plex response size")
	}
	if result == nil {
		return nil
	}
	if err = xml.Unmarshal(b, result); err != nil {
		return errors.New("invalid Plex XML response")
	}
	return nil
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
