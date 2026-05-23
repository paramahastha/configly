package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/paramahastha/configly/server/internal/auth"
	"github.com/paramahastha/configly/server/internal/model"
	"github.com/paramahastha/configly/server/internal/store"
)

// newTestStore opens a temp SQLite DB with full schema applied.
func newTestStore(t *testing.T) *store.SQLite {
	t.Helper()
	s, err := store.NewSQLite(filepath.Join(t.TempDir(), "auth_test.db"))
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// seedUser creates a user + API key and returns the raw key.
func seedUser(t *testing.T, s *store.SQLite, email string, role model.Role) (user *model.User, rawKey string) {
	t.Helper()
	hash, err := auth.HashPassword("password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	u := &model.User{Email: email, PasswordHash: hash, Role: role}
	if err := s.CreateUser(context.Background(), u); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	rawKey, err = auth.NewAPIKey()
	if err != nil {
		t.Fatalf("NewAPIKey: %v", err)
	}
	k := &model.APIKey{
		UserID:  u.ID,
		Name:    "test",
		Prefix:  rawKey[:8],
		KeyHash: store.HashAPIKey(rawKey),
	}
	if err := s.CreateAPIKey(context.Background(), k, u.Email); err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	return u, rawKey
}

// ---- HashPassword / CheckPassword ------------------------------------------------

func TestHashRoundTrip(t *testing.T) {
	hash, err := auth.HashPassword("s3cr3t")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !auth.CheckPassword(hash, "s3cr3t") {
		t.Fatal("CheckPassword: expected true for correct password")
	}
}

func TestWrongPasswordReturnsFalse(t *testing.T) {
	hash, _ := auth.HashPassword("right")
	if auth.CheckPassword(hash, "wrong") {
		t.Fatal("CheckPassword: expected false for wrong password")
	}
}

// ---- NewAPIKey -------------------------------------------------------------------

func TestNewAPIKeyPrefix(t *testing.T) {
	key, err := auth.NewAPIKey()
	if err != nil {
		t.Fatalf("NewAPIKey: %v", err)
	}
	if len(key) < 5 || key[:5] != "cfly_" {
		t.Fatalf("expected key to start with 'cfly_', got %q", key)
	}
}

func TestNewAPIKeyUniqueness(t *testing.T) {
	seen := make(map[string]bool, 1000)
	for i := 0; i < 1000; i++ {
		k, err := auth.NewAPIKey()
		if err != nil {
			t.Fatalf("NewAPIKey[%d]: %v", i, err)
		}
		if seen[k] {
			t.Fatalf("duplicate key generated: %q", k)
		}
		seen[k] = true
	}
}

// ---- Middleware ------------------------------------------------------------------

func okHandler(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

func TestMiddlewareNoKey(t *testing.T) {
	s := newTestStore(t)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	auth.Middleware(s)(http.HandlerFunc(okHandler)).ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestMiddlewareUnknownKey(t *testing.T) {
	s := newTestStore(t)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer cfly_notareal key")
	w := httptest.NewRecorder()

	auth.Middleware(s)(http.HandlerFunc(okHandler)).ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestMiddlewareValidKeyBearerHeader(t *testing.T) {
	s := newTestStore(t)
	_, rawKey := seedUser(t, s, "a@test.com", model.RoleViewer)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+rawKey)
	w := httptest.NewRecorder()

	auth.Middleware(s)(http.HandlerFunc(okHandler)).ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestMiddlewareValidKeyXAPIKeyHeader(t *testing.T) {
	s := newTestStore(t)
	_, rawKey := seedUser(t, s, "b@test.com", model.RoleViewer)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-API-Key", rawKey)
	w := httptest.NewRecorder()

	auth.Middleware(s)(http.HandlerFunc(okHandler)).ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestMiddlewareValidKeyQueryParam(t *testing.T) {
	s := newTestStore(t)
	_, rawKey := seedUser(t, s, "c@test.com", model.RoleViewer)

	r := httptest.NewRequest(http.MethodGet, "/?key="+rawKey, nil)
	w := httptest.NewRecorder()

	auth.Middleware(s)(http.HandlerFunc(okHandler)).ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

// ---- RequireRole (project-scoped) -----------------------------------------------

// serveWithProject wires up a chi router with the auth middleware and a RequireRole
// gate on a route that includes {project} in the path.
func serveWithProject(t *testing.T, s *store.SQLite, project string, rawKey string, min model.Role) *httptest.ResponseRecorder {
	t.Helper()
	rtr := chi.NewRouter()
	rtr.Use(auth.Middleware(s))
	rtr.With(auth.RequireRole(s, min)).Get("/v1/{project}/configs", okHandler)

	target := "/v1/" + project + "/configs"
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	w := httptest.NewRecorder()
	rtr.ServeHTTP(w, req)
	return w
}

func TestRequireRoleEditorAllowsEditor(t *testing.T) {
	s := newTestStore(t)
	user, rawKey := seedUser(t, s, "editor@test.com", model.RoleViewer) // global role irrelevant here
	if err := s.SetProjectMember(context.Background(), &model.ProjectMember{
		UserID: user.ID, Project: "myproject", Role: model.RoleEditor,
	}); err != nil {
		t.Fatalf("SetProjectMember: %v", err)
	}

	w := serveWithProject(t, s, "myproject", rawKey, model.RoleEditor)
	if w.Code != http.StatusOK {
		t.Fatalf("editor should be allowed for min=editor, got %d", w.Code)
	}
}

func TestRequireRoleEditorAllowsAdmin(t *testing.T) {
	s := newTestStore(t)
	user, rawKey := seedUser(t, s, "admin@test.com", model.RoleViewer)
	if err := s.SetProjectMember(context.Background(), &model.ProjectMember{
		UserID: user.ID, Project: "myproject", Role: model.RoleAdmin,
	}); err != nil {
		t.Fatalf("SetProjectMember: %v", err)
	}

	w := serveWithProject(t, s, "myproject", rawKey, model.RoleEditor)
	if w.Code != http.StatusOK {
		t.Fatalf("admin should be allowed for min=editor, got %d", w.Code)
	}
}

func TestRequireRoleEditorBlocksViewer(t *testing.T) {
	s := newTestStore(t)
	user, rawKey := seedUser(t, s, "viewer@test.com", model.RoleViewer)
	if err := s.SetProjectMember(context.Background(), &model.ProjectMember{
		UserID: user.ID, Project: "myproject", Role: model.RoleViewer,
	}); err != nil {
		t.Fatalf("SetProjectMember: %v", err)
	}

	w := serveWithProject(t, s, "myproject", rawKey, model.RoleEditor)
	if w.Code != http.StatusForbidden {
		t.Fatalf("viewer should be blocked for min=editor, got %d", w.Code)
	}
}

func TestRequireRoleNoMembership(t *testing.T) {
	s := newTestStore(t)
	_, rawKey := seedUser(t, s, "nomember@test.com", model.RoleViewer)
	// User has no ProjectMember entry for "myproject"

	w := serveWithProject(t, s, "myproject", rawKey, model.RoleViewer)
	if w.Code != http.StatusForbidden {
		t.Fatalf("user with no membership should get 403, got %d", w.Code)
	}
}

// ---- RequireRole (system-level — no project param) --------------------------------

func TestRequireRoleSystemAdminAllowed(t *testing.T) {
	s := newTestStore(t)
	_, rawKey := seedUser(t, s, "sysadmin@test.com", model.RoleAdmin)

	rtr := chi.NewRouter()
	rtr.Use(auth.Middleware(s))
	rtr.With(auth.RequireRole(s, model.RoleAdmin)).Get("/v1/users", okHandler)

	req := httptest.NewRequest(http.MethodGet, "/v1/users", nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	w := httptest.NewRecorder()
	rtr.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("system admin should access /v1/users, got %d", w.Code)
	}
}

func TestRequireRoleSystemViewerBlocked(t *testing.T) {
	s := newTestStore(t)
	_, rawKey := seedUser(t, s, "sysviewer@test.com", model.RoleViewer)

	rtr := chi.NewRouter()
	rtr.Use(auth.Middleware(s))
	rtr.With(auth.RequireRole(s, model.RoleAdmin)).Get("/v1/users", okHandler)

	req := httptest.NewRequest(http.MethodGet, "/v1/users", nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	w := httptest.NewRecorder()
	rtr.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("system viewer should be blocked from /v1/users, got %d", w.Code)
	}
}
