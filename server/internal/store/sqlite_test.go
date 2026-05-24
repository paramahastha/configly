package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/paramahastha/configly/server/internal/model"
	"github.com/paramahastha/configly/server/internal/store"
)

func newTestStore(t *testing.T) *store.SQLite {
	t.Helper()
	s, err := store.NewSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func seedEnv(t *testing.T, s *store.SQLite, project, env string) {
	t.Helper()
	err := s.CreateEnvironment(context.Background(), &model.Environment{
		Project: project,
		Name:    env,
	})
	if err != nil {
		t.Fatalf("CreateEnvironment: %v", err)
	}
}

// TestUpsertAndGet: upsert a config, get it back, version is 1.
func TestUpsertAndGet(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	seedEnv(t, s, "default", "dev")

	c := &model.Config{
		Project:     "default",
		Environment: "dev",
		Key:         "feature.x",
		Type:        model.ConfigTypeString,
		Value:       "hello",
	}
	if err := s.UpsertConfig(ctx, c, "tester"); err != nil {
		t.Fatalf("UpsertConfig: %v", err)
	}
	if c.Version != 1 {
		t.Fatalf("expected version=1, got %d", c.Version)
	}

	got, err := s.GetConfig(ctx, "default", "dev", "feature.x")
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if got == nil {
		t.Fatal("expected config, got nil")
	}
	if got.Value != "hello" {
		t.Fatalf("expected value='hello', got %q", got.Value)
	}
	if got.Version != 1 {
		t.Fatalf("expected version=1, got %d", got.Version)
	}
}

// TestUpsertBumpsVersion: upsert twice → version is 2, config_versions has 2 rows.
func TestUpsertBumpsVersion(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	seedEnv(t, s, "default", "dev")

	c := &model.Config{
		Project: "default", Environment: "dev", Key: "flag.y",
		Type: model.ConfigTypeFlag, Value: "false",
	}
	if err := s.UpsertConfig(ctx, c, "actor"); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	c.Value = "true"
	if err := s.UpsertConfig(ctx, c, "actor"); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	got, err := s.GetConfig(ctx, "default", "dev", "flag.y")
	if err != nil || got == nil {
		t.Fatalf("GetConfig: %v, %v", got, err)
	}
	if got.Version != 2 {
		t.Fatalf("expected version=2, got %d", got.Version)
	}
	if got.Value != "true" {
		t.Fatalf("expected value='true', got %q", got.Value)
	}

	versions, err := s.ListVersions(ctx, got.ID, 10)
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("expected 2 version rows, got %d", len(versions))
	}
}

// TestRollback: write v1, write v2, rollback to v1 → current value matches v1, version is 3.
func TestRollback(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	seedEnv(t, s, "default", "dev")

	c := &model.Config{
		Project: "default", Environment: "dev", Key: "cfg.z",
		Type: model.ConfigTypeString, Value: "v1-value",
	}
	if err := s.UpsertConfig(ctx, c, "actor"); err != nil {
		t.Fatalf("v1 upsert: %v", err)
	}
	configID := c.ID

	c.Value = "v2-value"
	if err := s.UpsertConfig(ctx, c, "actor"); err != nil {
		t.Fatalf("v2 upsert: %v", err)
	}

	if _, _, err := s.Rollback(ctx, configID, 1, "actor"); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	got, err := s.GetConfig(ctx, "default", "dev", "cfg.z")
	if err != nil || got == nil {
		t.Fatalf("GetConfig after rollback: %v, %v", got, err)
	}
	if got.Value != "v1-value" {
		t.Fatalf("expected 'v1-value' after rollback, got %q", got.Value)
	}
	if got.Version != 3 {
		t.Fatalf("expected version=3 after rollback, got %d", got.Version)
	}
}

// TestSnapshotCache: snapshot returns same pointer on second call;
// after upsert it returns a new pointer with a different ETag.
func TestSnapshotCache(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	seedEnv(t, s, "default", "dev")

	snap1, err := s.Snapshot(ctx, "default", "dev")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	snap2, err := s.Snapshot(ctx, "default", "dev")
	if err != nil {
		t.Fatalf("Snapshot 2nd call: %v", err)
	}
	if snap1 != snap2 {
		t.Fatal("expected same pointer on cache hit")
	}

	c := &model.Config{
		Project: "default", Environment: "dev", Key: "new.key",
		Type: model.ConfigTypeString, Value: "val",
	}
	if err := s.UpsertConfig(ctx, c, "actor"); err != nil {
		t.Fatalf("UpsertConfig: %v", err)
	}

	snap3, err := s.Snapshot(ctx, "default", "dev")
	if err != nil {
		t.Fatalf("Snapshot after upsert: %v", err)
	}
	if snap3 == snap1 {
		t.Fatal("expected new pointer after cache invalidation")
	}
	if snap3.ETag == snap1.ETag {
		t.Fatalf("expected different ETag after upsert; both are %q", snap1.ETag)
	}

	// ETag must be stable on the same data.
	snap4, _ := s.Snapshot(ctx, "default", "dev")
	if snap3.ETag != snap4.ETag {
		t.Fatalf("ETag unstable: %q vs %q", snap3.ETag, snap4.ETag)
	}
}

// TestListAudit: append 3 events, list returns them newest-first.
func TestListAudit(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	events := []model.AuditEvent{
		{Actor: "alice", Action: "create", Project: "proj1"},
		{Actor: "bob", Action: "update", Project: "proj1"},
		{Actor: "carol", Action: "delete", Project: "proj1"},
	}
	for i := range events {
		// Small sleep so created_at timestamps differ.
		time.Sleep(2 * time.Millisecond)
		if err := s.AppendAudit(ctx, &events[i]); err != nil {
			t.Fatalf("AppendAudit[%d]: %v", i, err)
		}
	}

	got, err := s.ListAudit(ctx, "proj1", 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 events, got %d", len(got))
	}
	// Newest-first: carol, bob, alice
	if got[0].Actor != "carol" {
		t.Fatalf("expected newest event first (carol), got %q", got[0].Actor)
	}
	if got[2].Actor != "alice" {
		t.Fatalf("expected oldest event last (alice), got %q", got[2].Actor)
	}
}

// TestConfigCreatedAtPopulated: created_at and created_by are set on insert and preserved on update.
func TestConfigCreatedAtPopulated(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	seedEnv(t, s, "p", "e")

	c := &model.Config{Project: "p", Environment: "e", Key: "k", Type: model.ConfigTypeString, Value: "v1"}
	if err := s.UpsertConfig(ctx, c, "alice"); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if c.CreatedAt.IsZero() {
		t.Fatal("CreatedAt must be set after insert")
	}
	if c.CreatedBy != "alice" {
		t.Fatalf("CreatedBy: want alice, got %q", c.CreatedBy)
	}
	createdAt := c.CreatedAt

	c.Value = "v2"
	if err := s.UpsertConfig(ctx, c, "bob"); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	got, _ := s.GetConfig(ctx, "p", "e", "k")
	if got.CreatedBy != "alice" {
		t.Fatalf("CreatedBy must not change on update: got %q", got.CreatedBy)
	}
	if !got.CreatedAt.Equal(createdAt) {
		t.Fatalf("CreatedAt must not change on update")
	}
	if got.UpdatedBy != "bob" {
		t.Fatalf("UpdatedBy: want bob, got %q", got.UpdatedBy)
	}
}

// TestETagCoversAllFields: changes to type, rollout, and rules must change the ETag.
func TestETagCoversAllFields(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	seedEnv(t, s, "p", "e")

	c := &model.Config{Project: "p", Environment: "e", Key: "k", Type: model.ConfigTypeString, Value: "v", Rollout: 0}
	if err := s.UpsertConfig(ctx, c, "actor"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	snap1, _ := s.Snapshot(ctx, "p", "e")

	// Change rollout only — value unchanged.
	c.Rollout = 50
	if err := s.UpsertConfig(ctx, c, "actor"); err != nil {
		t.Fatalf("rollout update: %v", err)
	}
	snap2, _ := s.Snapshot(ctx, "p", "e")
	if snap1.ETag == snap2.ETag {
		t.Fatal("ETag must change when rollout changes")
	}

	// Change type only.
	c.Type = model.ConfigTypeBool
	if err := s.UpsertConfig(ctx, c, "actor"); err != nil {
		t.Fatalf("type update: %v", err)
	}
	snap3, _ := s.Snapshot(ctx, "p", "e")
	if snap2.ETag == snap3.ETag {
		t.Fatal("ETag must change when type changes")
	}

	// Change rules only.
	c.Rules = []byte(`{"pct":10}`)
	if err := s.UpsertConfig(ctx, c, "actor"); err != nil {
		t.Fatalf("rules update: %v", err)
	}
	snap4, _ := s.Snapshot(ctx, "p", "e")
	if snap3.ETag == snap4.ETag {
		t.Fatal("ETag must change when rules change")
	}
}

// TestSnapshotUpdatedAt: UpdatedAt reflects the latest config update, not server time.
func TestSnapshotUpdatedAt(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	seedEnv(t, s, "p", "e")

	before := time.Now()
	c := &model.Config{Project: "p", Environment: "e", Key: "k", Type: model.ConfigTypeString, Value: "v"}
	if err := s.UpsertConfig(ctx, c, "actor"); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	snap, _ := s.Snapshot(ctx, "p", "e")
	if snap.UpdatedAt.Before(before) || snap.UpdatedAt.After(time.Now()) {
		t.Fatalf("Snapshot.UpdatedAt out of range: %v", snap.UpdatedAt)
	}
	// Must equal the config's UpdatedAt, not an arbitrary server timestamp.
	got, _ := s.GetConfig(ctx, "p", "e", "k")
	if !snap.UpdatedAt.Equal(got.UpdatedAt) {
		t.Fatalf("Snapshot.UpdatedAt=%v does not match config.UpdatedAt=%v", snap.UpdatedAt, got.UpdatedAt)
	}
}

// TestDeleteConfig: soft-deleted config is invisible to Get and List.
func TestDeleteConfig(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	seedEnv(t, s, "p", "e")
	c := &model.Config{Project: "p", Environment: "e", Key: "k", Type: model.ConfigTypeString, Value: "v"}
	if err := s.UpsertConfig(ctx, c, "actor"); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.DeleteConfig(ctx, "p", "e", "k", "actor"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	got, err := s.GetConfig(ctx, "p", "e", "k")
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if got != nil {
		t.Fatal("deleted config must not be returned by GetConfig")
	}

	list, _ := s.ListConfigs(ctx, "p", "e")
	for _, lc := range list {
		if lc.Key == "k" {
			t.Fatal("deleted config must not appear in ListConfigs")
		}
	}
}

// TestDeleteEnvironment: soft-deleted environment disappears from ListEnvironments.
func TestDeleteEnvironment(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	seedEnv(t, s, "p", "staging")
	seedEnv(t, s, "p", "prod")

	if err := s.DeleteEnvironment(ctx, "p", "staging"); err != nil {
		t.Fatalf("DeleteEnvironment: %v", err)
	}

	envs, err := s.ListEnvironments(ctx, "p")
	if err != nil {
		t.Fatalf("ListEnvironments: %v", err)
	}
	for _, e := range envs {
		if e.Name == "staging" {
			t.Fatal("deleted environment must not appear in ListEnvironments")
		}
	}
	if len(envs) != 1 || envs[0].Name != "prod" {
		t.Fatalf("expected only prod, got %v", envs)
	}
}

// TestListProjects: projects are derived from their environments.
func TestListProjects(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	seedEnv(t, s, "alpha", "dev")
	seedEnv(t, s, "beta", "dev")
	seedEnv(t, s, "gamma", "dev")

	projects, err := s.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	want := []string{"alpha", "beta", "gamma"}
	if len(projects) != len(want) {
		t.Fatalf("expected %v, got %v", want, projects)
	}
	for i, p := range projects {
		if p != want[i] {
			t.Fatalf("projects[%d]: want %q, got %q", i, want[i], p)
		}
	}
}

// TestCreateAndGetUser: create a user, retrieve by email, verify fields round-trip.
func TestCreateAndGetUser(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	u := &model.User{Email: "alice@example.com", PasswordHash: "hash", Role: model.RoleAdmin}
	if err := s.CreateUser(ctx, u); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if u.ID == "" {
		t.Fatal("ID must be set after CreateUser")
	}

	got, err := s.GetUserByEmail(ctx, "alice@example.com")
	if err != nil || got == nil {
		t.Fatalf("GetUserByEmail: %v, %v", got, err)
	}
	if got.Role != model.RoleAdmin {
		t.Fatalf("Role: want admin, got %q", got.Role)
	}
	if got.PasswordHash != "hash" {
		t.Fatal("PasswordHash must round-trip")
	}

	users, err := s.ListUsers(ctx)
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(users) != 1 || users[0].Email != "alice@example.com" {
		t.Fatalf("ListUsers returned unexpected results: %v", users)
	}
}

// TestDeleteUser: soft-deleted user is invisible to GetUserByEmail, ListUsers,
// and GetUserByAPIKey; cascades to api_keys and project_members.
func TestDeleteUser(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	u := &model.User{Email: "bob@example.com", PasswordHash: "h"}
	if err := s.CreateUser(ctx, u); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	rawKey := "deleteduserkey1"
	k := &model.APIKey{UserID: u.ID, Name: "k", Prefix: rawKey[:8]}
	if err := s.CreateAPIKey(ctx, k, rawKey, u.ID); err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	if err := s.SetProjectMember(ctx, &model.ProjectMember{UserID: u.ID, Project: "p", Role: model.RoleViewer}); err != nil {
		t.Fatalf("SetProjectMember: %v", err)
	}

	if err := s.DeleteUser(ctx, u.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}

	if got, _ := s.GetUserByEmail(ctx, "bob@example.com"); got != nil {
		t.Fatal("deleted user must not be returned by GetUserByEmail")
	}
	users, _ := s.ListUsers(ctx)
	for _, lu := range users {
		if lu.ID == u.ID {
			t.Fatal("deleted user must not appear in ListUsers")
		}
	}
	// API key and project membership must be gone.
	if u2, _, _ := s.GetUserByAPIKey(ctx, rawKey); u2 != nil {
		t.Fatal("deleted user's API key must not authenticate")
	}
	if members, _ := s.ListProjectMembers(ctx, "p"); len(members) != 0 {
		t.Fatalf("deleted user's project_members must be removed, got %d", len(members))
	}
}

// TestCreateAndRevokeAPIKey: create key → lookup works; revoke → lookup returns nil.
func TestCreateAndRevokeAPIKey(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	u := &model.User{Email: "carol@example.com", PasswordHash: "h"}
	if err := s.CreateUser(ctx, u); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	rawKey := "supersecretkey1234"
	k := &model.APIKey{
		UserID: u.ID,
		Name:   "test-key",
		Prefix: rawKey[:8],
	}
	if err := s.CreateAPIKey(ctx, k, rawKey, u.ID); err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}

	foundUser, foundKey, err := s.GetUserByAPIKey(ctx, rawKey)
	if err != nil || foundUser == nil || foundKey == nil {
		t.Fatalf("GetUserByAPIKey: user=%v key=%v err=%v", foundUser, foundKey, err)
	}
	if foundUser.Email != "carol@example.com" {
		t.Fatalf("unexpected user email: %q", foundUser.Email)
	}

	if err := s.RevokeAPIKey(ctx, k.ID); err != nil {
		t.Fatalf("RevokeAPIKey: %v", err)
	}

	u2, k2, err := s.GetUserByAPIKey(ctx, rawKey)
	if err != nil {
		t.Fatalf("GetUserByAPIKey after revoke: %v", err)
	}
	if u2 != nil || k2 != nil {
		t.Fatal("revoked key must not be found")
	}
}

// TestExpiredAPIKey: a key whose expires_at is in the past must not be returned.
func TestExpiredAPIKey(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	u := &model.User{Email: "expired@example.com", PasswordHash: "h"}
	if err := s.CreateUser(ctx, u); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	past := time.Now().UTC().Add(-time.Hour)
	rawKey := "expiredkey9999"
	k := &model.APIKey{
		UserID:    u.ID,
		Name:      "old-key",
		Prefix:    rawKey[:8],
		ExpiresAt: &past,
	}
	if err := s.CreateAPIKey(ctx, k, rawKey, u.ID); err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}

	foundUser, foundKey, err := s.GetUserByAPIKey(ctx, rawKey)
	if err != nil {
		t.Fatalf("GetUserByAPIKey: %v", err)
	}
	if foundUser != nil || foundKey != nil {
		t.Fatal("expired key must not authenticate")
	}
}

// TestUpdateLastUsedAt: last_used_at is persisted and returned on next lookup.
func TestUpdateLastUsedAt(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	u := &model.User{Email: "dave@example.com", PasswordHash: "h"}
	if err := s.CreateUser(ctx, u); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	rawKey := "anotherkey5678"
	k := &model.APIKey{UserID: u.ID, Name: "k", Prefix: rawKey[:8]}
	if err := s.CreateAPIKey(ctx, k, rawKey, u.ID); err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}

	ts := time.Now().UTC().Truncate(time.Second)
	if err := s.UpdateLastUsedAt(ctx, k.ID, ts); err != nil {
		t.Fatalf("UpdateLastUsedAt: %v", err)
	}

	_, foundKey, _ := s.GetUserByAPIKey(ctx, rawKey)
	if foundKey == nil || foundKey.LastUsedAt == nil {
		t.Fatal("LastUsedAt must be set after UpdateLastUsedAt")
	}
	if !foundKey.LastUsedAt.Truncate(time.Second).Equal(ts) {
		t.Fatalf("LastUsedAt: want %v, got %v", ts, foundKey.LastUsedAt)
	}
}

// TestSetAndGetProjectMember: upsert role, re-upsert to change it, verify.
func TestSetAndGetProjectMember(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	u := &model.User{Email: "eve@example.com", PasswordHash: "h"}
	if err := s.CreateUser(ctx, u); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	m := &model.ProjectMember{UserID: u.ID, Project: "proj", Role: model.RoleViewer}
	if err := s.SetProjectMember(ctx, m); err != nil {
		t.Fatalf("SetProjectMember: %v", err)
	}

	got, err := s.GetProjectMember(ctx, u.ID, "proj")
	if err != nil || got == nil {
		t.Fatalf("GetProjectMember: %v, %v", got, err)
	}
	if got.Role != model.RoleViewer {
		t.Fatalf("Role: want viewer, got %q", got.Role)
	}

	// Promote to editor.
	m.Role = model.RoleEditor
	if err := s.SetProjectMember(ctx, m); err != nil {
		t.Fatalf("SetProjectMember (update): %v", err)
	}
	got2, _ := s.GetProjectMember(ctx, u.ID, "proj")
	if got2.Role != model.RoleEditor {
		t.Fatalf("Role after update: want editor, got %q", got2.Role)
	}

	members, err := s.ListProjectMembers(ctx, "proj")
	if err != nil {
		t.Fatalf("ListProjectMembers: %v", err)
	}
	if len(members) != 1 {
		t.Fatalf("expected 1 member, got %d", len(members))
	}
}
