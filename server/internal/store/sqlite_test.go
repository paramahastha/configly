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

	if err := s.Rollback(ctx, configID, 1, "actor"); err != nil {
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
