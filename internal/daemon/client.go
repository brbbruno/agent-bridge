package daemon

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/brbbruno/agent-bridge/internal/model"
)

const tokenHeader = "X-Agent-Bridge-Token"

type Client struct {
	base  string
	token string
	http  *http.Client
}

func NewClient(port int, token string) *Client {
	return &Client{base: "http://127.0.0.1:" + strconv.Itoa(port), token: token, http: &http.Client{}}
}

func (c *Client) SetHTTPClient(client *http.Client) {
	if client != nil {
		c.http = client
	}
}

func (c *Client) Health(ctx context.Context) error {
	var result map[string]any
	return c.request(ctx, http.MethodGet, "/health", nil, &result)
}

func (c *Client) Handle(ctx context.Context, event model.Event) (model.Resolution, error) {
	path := "/v1/" + strings.TrimPrefix(string(event.Type), "/")
	var result model.Resolution
	err := c.request(ctx, http.MethodPost, path, event, &result)
	return result, err
}

func (c *Client) SetAway(ctx context.Context, away bool) error {
	return c.request(ctx, http.MethodPost, "/admin/away", map[string]bool{"away": away}, nil)
}

type Status struct {
	Away      bool          `json:"away"`
	Uptime    time.Duration `json:"uptime_ns"`
	Waiting   int           `json:"waiting"`
	StartedAt time.Time     `json:"started_at"`
}

func (c *Client) GetStatus(ctx context.Context) (Status, error) {
	var status Status
	err := c.request(ctx, http.MethodGet, "/admin/status", nil, &status)
	return status, err
}

func (c *Client) Test(ctx context.Context) error {
	return c.request(ctx, http.MethodPost, "/admin/test", map[string]bool{"test": true}, nil)
}

func (c *Client) Stop(ctx context.Context) error {
	return c.request(ctx, http.MethodPost, "/admin/stop", map[string]bool{"stop": true}, nil)
}

func (c *Client) request(ctx context.Context, method, path string, input, output any) error {
	var body *bytes.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	} else {
		body = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set(tokenHeader, c.token)
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("daemon respondeu HTTP %d", resp.StatusCode)
	}
	if output != nil {
		if err := json.NewDecoder(resp.Body).Decode(output); err != nil {
			return err
		}
	}
	return nil
}

func authorized(got, want string) bool {
	if len(got) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
