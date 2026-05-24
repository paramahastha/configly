package configly_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	configly "github.com/paramahastha/configly/sdk/go"
)

// testServer builds a minimal httptest.Server returning the given snapshot.
// On the first request it returns 200 + ETag; subsequent requests with matching
// If-None-Match get 304.
func testServer(t *testing.T, snap configly.Snapshot) (*httptest.Server, func() []http.Header) {
	t.Helper()
	var mu sync.Mutex
	var headers []http.Header

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		headers = append(headers, r.Header.Clone())
		n := len(headers)
		mu.Unlock()

		if n > 1 && r.Header.Get("If-None-Match") == snap.ETag {
			w.Header().Set("ETag", snap.ETag)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", snap.ETag)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(snap)
	}))

	return srv, func() []http.Header {
		mu.Lock()
		defer mu.Unlock()
		cp := make([]http.Header, len(headers))
		copy(cp, headers)
		return cp
	}
}

func newClient(t *testing.T, url string) *configly.Client {
	t.Helper()
	c, err := configly.New(configly.Options{
		URL:             url,
		APIKey:          "cfly_test",
		Project:         "test",
		Environment:     "dev",
		PollWaitSeconds: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func startClient(t *testing.T, c *configly.Client) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(func() {
		cancel()
		c.Close()
	})
	if err := c.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	return cancel
}

// ---- typed accessor tests ----

func TestGetString(t *testing.T) {
	snap := configly.Snapshot{
		ETag:    "etag-1",
		Configs: map[string]configly.ConfigEntry{"greeting": {Type: "string", Value: "hello"}},
	}
	srv, _ := testServer(t, snap)
	defer srv.Close()

	c := newClient(t, srv.URL)
	startClient(t, c)

	if got := c.GetString("greeting", "default"); got != "hello" {
		t.Fatalf("want hello, got %q", got)
	}
}

func TestGetInt(t *testing.T) {
	snap := configly.Snapshot{
		ETag:    "etag-2",
		Configs: map[string]configly.ConfigEntry{"timeout": {Type: "int", Value: "42"}},
	}
	srv, _ := testServer(t, snap)
	defer srv.Close()

	c := newClient(t, srv.URL)
	startClient(t, c)

	if got := c.GetInt("timeout", 0); got != 42 {
		t.Fatalf("want 42, got %d", got)
	}
}

func TestGetFloat(t *testing.T) {
	snap := configly.Snapshot{
		ETag:    "etag-3",
		Configs: map[string]configly.ConfigEntry{"rate": {Type: "float", Value: "3.14"}},
	}
	srv, _ := testServer(t, snap)
	defer srv.Close()

	c := newClient(t, srv.URL)
	startClient(t, c)

	if got := c.GetFloat("rate", 0); got != 3.14 {
		t.Fatalf("want 3.14, got %f", got)
	}
}

func TestGetBool(t *testing.T) {
	snap := configly.Snapshot{
		ETag: "etag-4",
		Configs: map[string]configly.ConfigEntry{
			"enabled":  {Type: "bool", Value: "true"},
			"disabled": {Type: "bool", Value: "false"},
		},
	}
	srv, _ := testServer(t, snap)
	defer srv.Close()

	c := newClient(t, srv.URL)
	startClient(t, c)

	if !c.GetBool("enabled", false) {
		t.Fatal("want true for enabled")
	}
	if c.GetBool("disabled", true) {
		t.Fatal("want false for disabled")
	}
}

func TestGetJSON(t *testing.T) {
	snap := configly.Snapshot{
		ETag:    "etag-5",
		Configs: map[string]configly.ConfigEntry{"cfg": {Type: "json", Value: `{"x":1}`}},
	}
	srv, _ := testServer(t, snap)
	defer srv.Close()

	c := newClient(t, srv.URL)
	startClient(t, c)

	var out struct{ X int }
	if !c.GetJSON("cfg", &out) {
		t.Fatal("GetJSON returned false")
	}
	if out.X != 1 {
		t.Fatalf("want X=1, got %d", out.X)
	}
}

// ---- missing key / bad value ----

func TestMissingKeyReturnsDefault(t *testing.T) {
	snap := configly.Snapshot{ETag: "etag-6", Configs: map[string]configly.ConfigEntry{}}
	srv, _ := testServer(t, snap)
	defer srv.Close()

	c := newClient(t, srv.URL)
	startClient(t, c)

	if got := c.GetString("missing", "fallback"); got != "fallback" {
		t.Fatalf("want fallback, got %q", got)
	}
	if got := c.GetInt("missing", -1); got != -1 {
		t.Fatalf("want -1, got %d", got)
	}
	if got := c.GetFloat("missing", 9.9); got != 9.9 {
		t.Fatalf("want 9.9, got %f", got)
	}
	if c.GetBool("missing", true) != true {
		t.Fatal("want true default")
	}
	var out any
	if c.GetJSON("missing", &out) {
		t.Fatal("GetJSON should return false for missing key")
	}
	if c.IsEnabled("missing", "u1", true) != true {
		t.Fatal("IsEnabled should return default for missing key")
	}
}

func TestGetIntBadValue(t *testing.T) {
	snap := configly.Snapshot{
		ETag:    "etag-7",
		Configs: map[string]configly.ConfigEntry{"x": {Type: "string", Value: "not-a-number"}},
	}
	srv, _ := testServer(t, snap)
	defer srv.Close()

	c := newClient(t, srv.URL)
	startClient(t, c)

	if got := c.GetInt("x", 99); got != 99 {
		t.Fatalf("want 99, got %d", got)
	}
}

// ---- rollout / IsEnabled ----

func TestRolloutUniformity(t *testing.T) {
	snap := configly.Snapshot{
		ETag: "etag-8",
		Configs: map[string]configly.ConfigEntry{
			"feat": {Type: "flag", Value: "true", Rollout: 30},
		},
	}
	srv, _ := testServer(t, snap)
	defer srv.Close()

	c := newClient(t, srv.URL)
	startClient(t, c)

	enabled := 0
	for i := 0; i < 10000; i++ {
		uid := fmt.Sprintf("user-%d", i)
		if c.IsEnabled("feat", uid, false) {
			enabled++
		}
	}
	if enabled < 2700 || enabled > 3300 {
		t.Fatalf("30%% rollout over 10k users: want 2700-3300 enabled, got %d", enabled)
	}
}

func TestRollout100AlwaysEnabled(t *testing.T) {
	snap := configly.Snapshot{
		ETag: "etag-9",
		Configs: map[string]configly.ConfigEntry{
			"feat": {Type: "flag", Value: "false", Rollout: 100},
		},
	}
	srv, _ := testServer(t, snap)
	defer srv.Close()

	c := newClient(t, srv.URL)
	startClient(t, c)

	for _, uid := range []string{"a", "b", "c", "", "user-99999"} {
		if !c.IsEnabled("feat", uid, false) {
			t.Fatalf("rollout=100 must always be enabled, failed for user %q", uid)
		}
	}
}

func TestRollout0WithFalseAlwaysDisabled(t *testing.T) {
	snap := configly.Snapshot{
		ETag: "etag-10",
		Configs: map[string]configly.ConfigEntry{
			"feat": {Type: "flag", Value: "false", Rollout: 0},
		},
	}
	srv, _ := testServer(t, snap)
	defer srv.Close()

	c := newClient(t, srv.URL)
	startClient(t, c)

	for _, uid := range []string{"a", "b", "c", "user-1", "user-2"} {
		if c.IsEnabled("feat", uid, true) {
			t.Fatalf("rollout=0 value=false must be disabled, was enabled for user %q", uid)
		}
	}
}

// ---- If-None-Match ----

func TestIfNoneMatch(t *testing.T) {
	snap := configly.Snapshot{
		ETag:    "unique-etag-xyz",
		Configs: map[string]configly.ConfigEntry{"k": {Type: "string", Value: "v"}},
	}
	srv, getHeaders := testServer(t, snap)
	defer srv.Close()

	c := newClient(t, srv.URL)
	startClient(t, c)

	// Wait for poll loop to fire at least one more request.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(getHeaders()) >= 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	hdrs := getHeaders()
	if len(hdrs) < 2 {
		t.Fatalf("expected at least 2 requests, got %d", len(hdrs))
	}
	if got := hdrs[1].Get("If-None-Match"); got != snap.ETag {
		t.Fatalf("second request: want If-None-Match %q, got %q", snap.ETag, got)
	}
}

// ---- New() validation ----

func TestNewMissingURL(t *testing.T) {
	_, err := configly.New(configly.Options{APIKey: "k", Project: "p", Environment: "e"})
	if err == nil {
		t.Fatal("expected error for missing URL")
	}
}

func TestNewMissingAPIKey(t *testing.T) {
	_, err := configly.New(configly.Options{URL: "http://x", Project: "p", Environment: "e"})
	if err == nil {
		t.Fatal("expected error for missing APIKey")
	}
}

func TestNewMissingProject(t *testing.T) {
	_, err := configly.New(configly.Options{URL: "http://x", APIKey: "k", Environment: "e"})
	if err == nil {
		t.Fatal("expected error for missing Project")
	}
}

func TestNewMissingEnvironment(t *testing.T) {
	_, err := configly.New(configly.Options{URL: "http://x", APIKey: "k", Project: "p"})
	if err == nil {
		t.Fatal("expected error for missing Environment")
	}
}
