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
