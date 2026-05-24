package api

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/paramahastha/configly/server/internal/auth"
	"github.com/paramahastha/configly/server/internal/model"
	"github.com/paramahastha/configly/server/internal/sse"
	"github.com/paramahastha/configly/server/internal/store"
)

//go:embed ui/*
var uiFS embed.FS

// Handler wires Store and Broker into an HTTP service.
type Handler struct {
	Store  store.Store
	Broker *sse.Broker
}

// Routes builds and returns the fully-configured chi router.
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(corsMiddleware)

	r.Get("/healthz", h.healthz)

	r.Group(func(r chi.Router) {
		r.Use(auth.Middleware(h.Store))

		// Viewer-level
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireRole(h.Store, model.RoleViewer))
			r.Get("/v1/snapshot/{project}/{env}", h.getSnapshot)
			r.Get("/v1/stream/{project}/{env}", h.streamSSE)
		})

		// Editor-level — register static-suffix routes first so chi prefers them
		// over the two-wildcard /v1/configs/{project}/{env} pattern.
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireRole(h.Store, model.RoleEditor))
			r.Get("/v1/configs/{id}/versions", h.listVersions)
			r.Post("/v1/configs/{id}/rollback/{version}", h.rollbackConfig)
			r.Get("/v1/configs/{project}/{env}", h.listConfigs)
			r.Put("/v1/configs/{project}/{env}", h.upsertConfig)
			r.Delete("/v1/configs/{project}/{env}/{key}", h.deleteConfig)
		})

		// Admin-level
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireRole(h.Store, model.RoleAdmin))
			r.Post("/v1/environments", h.createEnvironment)
			r.Get("/v1/projects", h.listProjects)
			r.Get("/v1/projects/{project}/environments", h.listEnvironments)
			r.Post("/v1/users", h.createUser)
			r.Get("/v1/users", h.listUsers)
			r.Get("/v1/audit", h.listAudit)
		})
	})

	sub, _ := fs.Sub(uiFS, "ui")
	r.Handle("/*", http.FileServer(http.FS(sub)))

	return r
}

// ---------------------------------------------------------------------------
// Public
// ---------------------------------------------------------------------------

func (h *Handler) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ---------------------------------------------------------------------------
// Viewer
// ---------------------------------------------------------------------------

func (h *Handler) getSnapshot(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	env := chi.URLParam(r, "env")

	snap, err := h.Store.Snapshot(r.Context(), project, env)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	clientETag := r.Header.Get("If-None-Match")
	wait, _ := strconv.Atoi(r.URL.Query().Get("wait"))

	if wait > 0 && clientETag != "" && clientETag == snap.ETag {
		if wait > 60 {
			wait = 60
		}
		ch, cancel := h.Broker.Subscribe(sse.Topic(project, env))
		defer cancel()

		ctx, cleanup := context.WithTimeout(r.Context(), time.Duration(wait)*time.Second)
		defer cleanup()

		select {
		case <-ch:
			snap, err = h.Store.Snapshot(r.Context(), project, env)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, err.Error())
				return
			}
		case <-ctx.Done():
			w.Header().Set("ETag", snap.ETag)
			w.WriteHeader(http.StatusNotModified)
			return
		}
	} else if clientETag == snap.ETag {
		w.Header().Set("ETag", snap.ETag)
		w.WriteHeader(http.StatusNotModified)
		return
	}

	w.Header().Set("ETag", snap.ETag)
	w.Header().Set("Cache-Control", "no-cache")
	writeJSON(w, http.StatusOK, snap)
}

func (h *Handler) streamSSE(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	env := chi.URLParam(r, "env")

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	snap, err := h.Store.Snapshot(r.Context(), project, env)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	fmt.Fprintf(w, "event: init\ndata: {\"etag\":%q}\n\n", snap.ETag)
	flusher.Flush()

	ch, cancel := h.Broker.Subscribe(sse.Topic(project, env))
	defer cancel()

	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()

	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return
			}
			data, _ := json.Marshal(ev)
			fmt.Fprintf(w, "event: change\ndata: %s\n\n", data)
			flusher.Flush()
		case <-ping.C:
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// ---------------------------------------------------------------------------
// Editor
// ---------------------------------------------------------------------------

func (h *Handler) listConfigs(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	env := chi.URLParam(r, "env")

	configs, err := h.Store.ListConfigs(r.Context(), project, env)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if configs == nil {
		configs = []model.Config{}
	}
	writeJSON(w, http.StatusOK, configs)
}

type upsertBody struct {
	Key         string           `json:"key"`
	Type        model.ConfigType `json:"type"`
	Value       string           `json:"value"`
	Rollout     model.Rollout    `json:"rollout"`
	Rules       json.RawMessage  `json:"rules,omitempty"`
	Description string           `json:"description,omitempty"`
}

func (h *Handler) upsertConfig(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	env := chi.URLParam(r, "env")
	user := auth.UserFrom(r.Context())

	var req upsertBody
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Key == "" {
		writeErr(w, http.StatusBadRequest, "key is required")
		return
	}
	if err := validateValue(req.Type, req.Value); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	c := &model.Config{
		Project:     project,
		Environment: env,
		Key:         req.Key,
		Type:        req.Type,
		Value:       req.Value,
		Rollout:     req.Rollout,
		Rules:       req.Rules,
		Description: req.Description,
	}
	if err := h.Store.UpsertConfig(r.Context(), c, user.Email); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.publishChange(r.Context(), project, env)
	writeJSON(w, http.StatusOK, c)
}

func (h *Handler) deleteConfig(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	env := chi.URLParam(r, "env")
	key := chi.URLParam(r, "key")
	user := auth.UserFrom(r.Context())

	if err := h.Store.DeleteConfig(r.Context(), project, env, key, user.Email); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.publishChange(r.Context(), project, env)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listVersions(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	limit := 50
	if l, _ := strconv.Atoi(r.URL.Query().Get("limit")); l > 0 {
		limit = l
	}

	versions, err := h.Store.ListVersions(r.Context(), id, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if versions == nil {
		versions = []model.ConfigVersion{}
	}
	writeJSON(w, http.StatusOK, versions)
}

func (h *Handler) rollbackConfig(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	version, err := strconv.Atoi(chi.URLParam(r, "version"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid version number")
		return
	}
	user := auth.UserFrom(r.Context())

	if err := h.Store.Rollback(r.Context(), id, version, user.Email); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Admin
// ---------------------------------------------------------------------------

type createEnvBody struct {
	Project string `json:"project"`
	Name    string `json:"name"`
}

func (h *Handler) createEnvironment(w http.ResponseWriter, r *http.Request) {
	var req createEnvBody
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Project == "" || req.Name == "" {
		writeErr(w, http.StatusBadRequest, "project and name are required")
		return
	}

	env := &model.Environment{ID: uuid.NewString(), Project: req.Project, Name: req.Name}
	if err := h.Store.CreateEnvironment(r.Context(), env); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, env)
}

func (h *Handler) listProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := h.Store.ListProjects(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if projects == nil {
		projects = []string{}
	}
	writeJSON(w, http.StatusOK, projects)
}

func (h *Handler) listEnvironments(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	envs, err := h.Store.ListEnvironments(r.Context(), project)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if envs == nil {
		envs = []model.Environment{}
	}
	writeJSON(w, http.StatusOK, envs)
}

type createUserBody struct {
	Email    string     `json:"email"`
	Password string     `json:"password"`
	Role     model.Role `json:"role"`
	KeyName  string     `json:"key_name,omitempty"`
}

type createUserResp struct {
	User   model.User `json:"user"`
	APIKey string     `json:"api_key"`
}

func (h *Handler) createUser(w http.ResponseWriter, r *http.Request) {
	var req createUserBody
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Email == "" || req.Password == "" {
		writeErr(w, http.StatusBadRequest, "email and password are required")
		return
	}
	if req.Role == "" {
		req.Role = model.RoleViewer
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "hash password: "+err.Error())
		return
	}

	u := &model.User{Email: req.Email, PasswordHash: hash, Role: req.Role}
	if err := h.Store.CreateUser(r.Context(), u); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	rawKey, err := auth.NewAPIKey()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "generate api key: "+err.Error())
		return
	}

	caller := auth.UserFrom(r.Context())
	keyName := req.KeyName
	if keyName == "" {
		keyName = "default"
	}
	k := &model.APIKey{
		UserID:  u.ID,
		Name:    keyName,
		Prefix:  rawKey[:8],
		KeyHash: store.HashAPIKey(rawKey),
	}
	if err := h.Store.CreateAPIKey(r.Context(), k, caller.Email); err != nil {
		writeErr(w, http.StatusInternalServerError, "create api key: "+err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, createUserResp{User: *u, APIKey: rawKey})
}

func (h *Handler) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.Store.ListUsers(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if users == nil {
		users = []model.User{}
	}
	writeJSON(w, http.StatusOK, users)
}

func (h *Handler) listAudit(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	limit := 100
	if l, _ := strconv.Atoi(r.URL.Query().Get("limit")); l > 0 {
		limit = l
	}

	events, err := h.Store.ListAudit(r.Context(), project, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if events == nil {
		events = []model.AuditEvent{}
	}
	writeJSON(w, http.StatusOK, events)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// publishChange fetches the current snapshot (already rebuilt by the store)
// and broadcasts a change event so long-poll and SSE subscribers wake up.
func (h *Handler) publishChange(ctx context.Context, project, env string) {
	snap, _ := h.Store.Snapshot(ctx, project, env)
	etag := ""
	if snap != nil {
		etag = snap.ETag
	}
	h.Broker.Publish(sse.Topic(project, env), sse.Event{
		Project:     project,
		Environment: env,
		ETag:        etag,
	})
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-API-Key, If-None-Match")
		w.Header().Set("Access-Control-Expose-Headers", "ETag")
		w.Header().Set("Access-Control-Allow-Methods", "GET, PUT, POST, DELETE, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"error":%q}`, msg)
}

func validateValue(ct model.ConfigType, value string) error {
	switch ct {
	case model.ConfigTypeString, model.ConfigTypeFlag:
		return nil
	case model.ConfigTypeInt:
		if _, err := strconv.ParseInt(value, 10, 64); err != nil {
			return fmt.Errorf("invalid int value %q", value)
		}
	case model.ConfigTypeFloat:
		if _, err := strconv.ParseFloat(value, 64); err != nil {
			return fmt.Errorf("invalid float value %q", value)
		}
	case model.ConfigTypeBool:
		if value != "true" && value != "false" {
			return fmt.Errorf(`bool value must be "true" or "false"`)
		}
	case model.ConfigTypeJSON:
		var v any
		if err := json.Unmarshal([]byte(value), &v); err != nil {
			return fmt.Errorf("invalid JSON value: %w", err)
		}
	default:
		return fmt.Errorf("unknown config type %q", ct)
	}
	return nil
}
