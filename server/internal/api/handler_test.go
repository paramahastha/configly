package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/paramahastha/configly/server/internal/auth"
	"github.com/paramahastha/configly/server/internal/model"
	"github.com/paramahastha/configly/server/internal/sse"
	"github.com/paramahastha/configly/server/internal/store"
)

// testEnv holds a live test server and the credentials needed to talk to it.
type testEnv struct {
	server    *httptest.Server
	st        store.Store
	adminKey  string
	viewerKey string
}

// newTestEnv spins up an in-process HTTP server backed by a temp SQLite DB.
// It creates:
//   - admin user  (system role: admin, project role: admin)
//   - viewer user (system role: viewer, project role: viewer)
//   - environment "testproject / dev"
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()

	st, err := store.NewSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	broker := sse.NewBroker()
	h := &Handler{Store: st, Broker: broker}
	srv := httptest.NewServer(h.Routes())
	t.Cleanup(srv.Close)

	ctx := context.Background()

	adminKey := mustCreateUser(t, ctx, st, "admin@test.com", model.RoleAdmin)
	viewerKey := mustCreateUser(t, ctx, st, "viewer@test.com", model.RoleViewer)

	// Create test project environment.
	env := &model.Environment{Project: "testproject", Name: "dev"}
	if err := st.CreateEnvironment(ctx, env); err != nil {
		t.Fatalf("create environment: %v", err)
	}

	// Grant project-scoped roles so RequireRole can find memberships.
	adminUser, _ := st.GetUserByEmail(ctx, "admin@test.com")
	viewerUser, _ := st.GetUserByEmail(ctx, "viewer@test.com")
	st.SetProjectMember(ctx, &model.ProjectMember{UserID: adminUser.ID, Project: "testproject", Role: model.RoleAdmin})   //nolint:errcheck
	st.SetProjectMember(ctx, &model.ProjectMember{UserID: viewerUser.ID, Project: "testproject", Role: model.RoleViewer}) //nolint:errcheck

	return &testEnv{
		server:    srv,
		st:        st,
		adminKey:  adminKey,
		viewerKey: viewerKey,
	}
}

// mustCreateUser creates a user + default API key and returns the raw key.
func mustCreateUser(t *testing.T, ctx context.Context, st store.Store, email string, role model.Role) string {
	t.Helper()

	hash, err := auth.HashPassword("pass")
	if err != nil {
		t.Fatal(err)
	}
	u := &model.User{Email: email, PasswordHash: hash, Role: role}
	if err := st.CreateUser(ctx, u); err != nil {
		t.Fatalf("create user %s: %v", email, err)
	}

	rawKey, err := auth.NewAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	k := &model.APIKey{
		UserID:  u.ID,
		Name:    "default",
		Prefix:  rawKey[:8],
		KeyHash: store.HashAPIKey(rawKey),
	}
	if err := st.CreateAPIKey(ctx, k, "system"); err != nil {
		t.Fatalf("create api key for %s: %v", email, err)
	}
	return rawKey
}

// req sends an authenticated HTTP request and returns the response.
// Pass an empty key string for an unauthenticated request.
func (te *testEnv) req(method, path, key string, body any) *http.Response {
	var buf *bytes.Buffer
	if body != nil {
		b, _ := json.Marshal(body)
		buf = bytes.NewBuffer(b)
	} else {
		buf = &bytes.Buffer{}
	}

	r, err := http.NewRequest(method, te.server.URL+path, buf)
	if err != nil {
		panic(err)
	}
	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}

	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		panic(err)
	}
	return resp
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestHealthz(t *testing.T) {
	te := newTestEnv(t)
	resp := te.req("GET", "/healthz", "", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
}

func TestSnapshotReturns200Then304(t *testing.T) {
	te := newTestEnv(t)
	path := "/v1/snapshot/testproject/dev"

	// First call: 200 with ETag.
	resp := te.req("GET", path, te.adminKey, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("missing ETag header")
	}

	// Second call with matching ETag: 304.
	r, _ := http.NewRequest("GET", te.server.URL+path, nil)
	r.Header.Set("Authorization", "Bearer "+te.adminKey)
	r.Header.Set("If-None-Match", etag)
	resp2, _ := http.DefaultClient.Do(r)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotModified {
		t.Fatalf("want 304, got %d", resp2.StatusCode)
	}
}

func TestUpsertBadTypeReturns400(t *testing.T) {
	te := newTestEnv(t)
	body := map[string]any{"key": "k", "type": "int", "value": "not-a-number"}
	resp := te.req("PUT", "/v1/configs/testproject/dev", te.adminKey, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
}

func TestUpsertWithoutAuthReturns401(t *testing.T) {
	te := newTestEnv(t)
	body := map[string]any{"key": "k", "type": "string", "value": "v"}
	resp := te.req("PUT", "/v1/configs/testproject/dev", "", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}
}

func TestUpsertByViewerReturns403(t *testing.T) {
	te := newTestEnv(t)
	body := map[string]any{"key": "k", "type": "string", "value": "v"}
	resp := te.req("PUT", "/v1/configs/testproject/dev", te.viewerKey, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403, got %d", resp.StatusCode)
	}
}

func TestLongPollWakesOnChange(t *testing.T) {
	te := newTestEnv(t)
	path := "/v1/snapshot/testproject/dev"

	// Seed initial snapshot to get a stable ETag.
	resp := te.req("GET", path, te.adminKey, nil)
	etag := resp.Header.Get("ETag")
	resp.Body.Close()

	// Start long-poll in the background.
	pollDone := make(chan int, 1)
	go func() {
		r, _ := http.NewRequest("GET", te.server.URL+path+"?wait=10", nil)
		r.Header.Set("Authorization", "Bearer "+te.adminKey)
		r.Header.Set("If-None-Match", etag)
		res, err := http.DefaultClient.Do(r)
		if err != nil {
			pollDone <- -1
			return
		}
		res.Body.Close()
		pollDone <- res.StatusCode
	}()

	// Give the goroutine time to reach the broker Subscribe call.
	time.Sleep(100 * time.Millisecond)

	// PUT a change to wake the long-poll.
	change := map[string]any{"key": "trigger", "type": "string", "value": "wake"}
	r := te.req("PUT", "/v1/configs/testproject/dev", te.adminKey, change)
	r.Body.Close()

	select {
	case status := <-pollDone:
		if status != http.StatusOK {
			t.Fatalf("long-poll returned %d, want 200", status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("long-poll did not wake within 2s")
	}
}

func TestDeleteRemovesFromSnapshot(t *testing.T) {
	te := newTestEnv(t)

	// Create a config.
	body := map[string]any{"key": "gone", "type": "string", "value": "bye"}
	resp := te.req("PUT", "/v1/configs/testproject/dev", te.adminKey, body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upsert failed: %d", resp.StatusCode)
	}

	// Delete it.
	resp = te.req("DELETE", "/v1/configs/testproject/dev/gone", te.adminKey, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete failed: %d", resp.StatusCode)
	}

	// Snapshot must not contain the deleted key.
	resp = te.req("GET", "/v1/snapshot/testproject/dev", te.adminKey, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("snapshot after delete: %d", resp.StatusCode)
	}
	var snap model.Snapshot
	json.NewDecoder(resp.Body).Decode(&snap) //nolint:errcheck
	if _, ok := snap.Configs["gone"]; ok {
		t.Fatal("deleted config still present in snapshot")
	}
}

func TestRollbackRestoresPriorValue(t *testing.T) {
	te := newTestEnv(t)

	// Write v1.
	body := map[string]any{"key": "mykey", "type": "string", "value": "v1"}
	resp := te.req("PUT", "/v1/configs/testproject/dev", te.adminKey, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upsert v1 failed: %d", resp.StatusCode)
	}
	var cfg model.Config
	json.NewDecoder(resp.Body).Decode(&cfg) //nolint:errcheck
	cfgID := cfg.ID

	// Write v2.
	body["value"] = "v2"
	resp2 := te.req("PUT", "/v1/configs/testproject/dev", te.adminKey, body)
	resp2.Body.Close()

	// Rollback to v1.
	rollbackResp := te.req("POST", "/v1/configs/"+cfgID+"/rollback/1", te.adminKey, nil)
	rollbackResp.Body.Close()
	if rollbackResp.StatusCode != http.StatusNoContent {
		t.Fatalf("rollback failed: %d", rollbackResp.StatusCode)
	}

	// Verify: current value is "v1" and version is 3.
	configs, err := te.st.ListConfigs(context.Background(), "testproject", "dev")
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, c := range configs {
		if c.Key != "mykey" {
			continue
		}
		found = true
		if c.Value != "v1" {
			t.Fatalf("want value %q, got %q", "v1", c.Value)
		}
		if c.Version != 3 {
			t.Fatalf("want version 3 after rollback, got %d", c.Version)
		}
	}
	if !found {
		t.Fatal("config not found after rollback")
	}
}
