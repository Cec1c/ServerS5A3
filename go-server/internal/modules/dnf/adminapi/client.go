package adminapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

// Client is configured by the operator, never by a browser request. Credentials
// are sent only to the configured endpoint; redirects are always rejected.
type Client struct {
	endpoint, token string
	http            *http.Client
}

func NewClient(endpoint, token string) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.User != nil || u.Host == "" || u.Path != Path || u.RawQuery != "" || u.Fragment != "" || len(token) < 32 {
		return nil, errors.New("invalid private game API configuration")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && ip != nil && (ip.IsLoopback() || ip.IsPrivate())) {
		return nil, errors.New("game API requires HTTPS or a private IP endpoint")
	}
	return &Client{endpoint: endpoint, token: token, http: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *Client) Call(ctx context.Context, input Request) (Response, error) {
	var result Response
	if c == nil {
		return result, errors.New("game runtime API unavailable")
	}
	input.Version = Version
	raw, err := json.Marshal(input)
	if err != nil {
		return result, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.endpoint, bytes.NewReader(raw))
	if err != nil {
		return result, errors.New("invalid game runtime request")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return result, errors.New("game runtime API unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return result, fmt.Errorf("game runtime API HTTP %d", resp.StatusCode)
	}
	raw, err = io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil || len(raw) > 65536 {
		return result, errors.New("invalid game runtime response")
	}
	if err = json.Unmarshal(raw, &result); err != nil || result.Version != Version {
		return Response{}, errors.New("incompatible game runtime response")
	}
	if !result.OK {
		return result, errors.New("game runtime operation failed: " + result.Error)
	}
	return result, nil
}
