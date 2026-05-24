// Package configly is a zero-dependency Go client for the Configly config platform.
// Reads are lock-free (atomic snapshot pointer); network I/O happens only in the background.
package configly

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// ConfigEntry is the SDK-facing projection of a single config value.
type ConfigEntry struct {
	Type    string `json:"type"`
	Value   string `json:"value"`
	Rollout int    `json:"rollout"`
}

// Snapshot holds the full config set for a (project, environment) pair.
type Snapshot struct {
	Project     string                 `json:"project"`
	Environment string                 `json:"environment"`
	ETag        string                 `json:"etag"`
	Configs     map[string]ConfigEntry `json:"configs"`
}

// Options configures the Configly client.
type Options struct {
	URL             string
	APIKey          string
	Project         string
	Environment     string
	PollWaitSeconds int // default 30, clamped to [1, 60]
}

// Client polls a Configly server and provides fast, lock-free config reads.
// Construct with New, call Start, then use the Get* / IsEnabled methods.
// Close when done.
type Client struct {
	opts   Options
	snap   atomic.Pointer[Snapshot]
	etag   atomic.Pointer[string]
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
	hc     *http.Client
}

// New validates options and returns a Client ready to Start.
func New(opts Options) (*Client, error) {
	if opts.URL == "" {
		return nil, fmt.Errorf("configly: URL is required")
	}
	if opts.APIKey == "" {
		return nil, fmt.Errorf("configly: APIKey is required")
	}
	if opts.Project == "" {
		return nil, fmt.Errorf("configly: Project is required")
	}
	if opts.Environment == "" {
		return nil, fmt.Errorf("configly: Environment is required")
	}
	if opts.PollWaitSeconds <= 0 {
		opts.PollWaitSeconds = 30
	}
	if opts.PollWaitSeconds > 60 {
		opts.PollWaitSeconds = 60
	}
	return &Client{
		opts: opts,
		done: make(chan struct{}),
		hc:   &http.Client{Timeout: time.Duration(opts.PollWaitSeconds+10) * time.Second},
	}, nil
}

// Start fetches the snapshot once synchronously, then starts the background poll loop.
// Returns an error if the initial fetch fails (bad credentials, unreachable server, etc.).
func (c *Client) Start(ctx context.Context) error {
	if err := c.fetchOnce(ctx, false); err != nil {
		return err
	}
	ctx2, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	go c.loop(ctx2)
	return nil
}

// Close stops the background loop and waits for it to exit. Idempotent.
func (c *Client) Close() {
	c.once.Do(func() {
		if c.cancel != nil {
			c.cancel()
		}
	})
	<-c.done
}

func (c *Client) loop(ctx context.Context) {
	defer close(c.done)
	backoff := time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		err := c.fetchOnce(ctx, true)
		if err == nil {
			backoff = time.Second
			continue
		}
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
	}
}

func (c *Client) fetchOnce(ctx context.Context, wait bool) error {
	url := fmt.Sprintf("%s/v1/snapshot/%s/%s", c.opts.URL, c.opts.Project, c.opts.Environment)
	if wait {
		url += fmt.Sprintf("?wait=%d", c.opts.PollWaitSeconds)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.opts.APIKey)
	if p := c.etag.Load(); p != nil {
		req.Header.Set("If-None-Match", *p)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("configly: server returned %d: %s", resp.StatusCode, string(body))
	}

	var snap Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		return fmt.Errorf("configly: decode snapshot: %w", err)
	}

	// Prefer ETag from header; fall back to body field.
	etag := resp.Header.Get("ETag")
	if etag == "" {
		etag = snap.ETag
	}
	c.snap.Store(&snap)
	c.etag.Store(&etag)
	return nil
}

func (c *Client) lookup(key string) (ConfigEntry, bool) {
	s := c.snap.Load()
	if s == nil {
		return ConfigEntry{}, false
	}
	e, ok := s.Configs[key]
	return e, ok
}

// GetString returns the string value for key, or def if the key is missing.
func (c *Client) GetString(key, def string) string {
	if e, ok := c.lookup(key); ok {
		return e.Value
	}
	return def
}

// GetInt returns the integer value for key, or def if missing or not parseable.
func (c *Client) GetInt(key string, def int64) int64 {
	e, ok := c.lookup(key)
	if !ok {
		return def
	}
	v, err := strconv.ParseInt(e.Value, 10, 64)
	if err != nil {
		return def
	}
	return v
}

// GetFloat returns the float value for key, or def if missing or not parseable.
func (c *Client) GetFloat(key string, def float64) float64 {
	e, ok := c.lookup(key)
	if !ok {
		return def
	}
	v, err := strconv.ParseFloat(e.Value, 64)
	if err != nil {
		return def
	}
	return v
}

// GetBool returns true if the value is exactly "true", or def if the key is missing.
func (c *Client) GetBool(key string, def bool) bool {
	e, ok := c.lookup(key)
	if !ok {
		return def
	}
	return e.Value == "true"
}

// GetJSON unmarshals the JSON value for key into out. Returns true on success.
func (c *Client) GetJSON(key string, out any) bool {
	e, ok := c.lookup(key)
	if !ok {
		return false
	}
	return json.Unmarshal([]byte(e.Value), out) == nil
}

// IsEnabled returns whether the feature flag key is enabled for userID.
// Uses FNV-1a → mod 100 for stable, cross-language bucket assignment.
func (c *Client) IsEnabled(key, userID string, def bool) bool {
	e, ok := c.lookup(key)
	if !ok {
		return def
	}
	if e.Type != "flag" {
		return e.Value == "true"
	}
	if e.Rollout >= 100 {
		return true
	}
	if e.Rollout <= 0 {
		return e.Value == "true"
	}
	if userID == "" {
		return e.Value == "true"
	}
	return hashBucket(key+":"+userID) < e.Rollout
}

// hashBucket maps s to [0, 99] via FNV-1a.
// Must match the JS SDK implementation exactly for cross-language rollout consistency.
func hashBucket(s string) int {
	var h uint32 = 0x811c9dc5
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 0x01000193
	}
	return int(h % 100)
}
