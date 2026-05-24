package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/paramahastha/configly/server/internal/model"
	"github.com/paramahastha/configly/server/internal/store"
)

// roleRank maps each role to a numeric rank for minimum-role comparisons.
var roleRank = map[model.Role]int{
	model.RoleViewer: 1,
	model.RoleEditor: 2,
	model.RoleAdmin:  3,
}

type ctxKey int

const ctxUser ctxKey = 0

// HashPassword returns a bcrypt hash of pw at DefaultCost.
func HashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(b), nil
}

// CheckPassword reports whether pw matches hash.
func CheckPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// NewAPIKey generates a unique key: "cfly_" + 48 random hex chars (24 bytes).
// The "cfly_" prefix makes leaked keys greppable in logs.
func NewAPIKey() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate api key: %w", err)
	}
	return "cfly_" + hex.EncodeToString(b), nil
}

// Middleware authenticates every request via Bearer token, X-API-Key header,
// or ?key= query param (the last form exists for EventSource which can't set headers).
// On success the authenticated *model.User is attached to the request context.
func Middleware(st store.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw := extractKey(r)
			if raw == "" {
				writeErr(w, http.StatusUnauthorized, "missing api key")
				return
			}
			user, _, err := st.GetUserByAPIKey(r.Context(), raw)
			if err != nil || user == nil {
				writeErr(w, http.StatusUnauthorized, "invalid api key")
				return
			}
			ctx := context.WithValue(r.Context(), ctxUser, user)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireRole gates access by minimum role.
// When the request URL contains a "project" chi param it resolves the caller's
// role from project_members (per-project RBAC). Otherwise it falls back to the
// user's global system Role field (set at account creation).
func RequireRole(st store.Store, min model.Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user := UserFrom(r.Context())
			if user == nil {
				writeErr(w, http.StatusUnauthorized, "unauthorized")
				return
			}

			role := user.Role // system-level fallback

			// System admins bypass per-project membership checks.
			if user.Role != model.RoleAdmin {
				if project := chi.URLParam(r, "project"); project != "" {
					m, err := st.GetProjectMember(r.Context(), user.ID, project)
					if err != nil || m == nil {
						writeErr(w, http.StatusForbidden, "forbidden")
						return
					}
					role = m.Role
				}
			}

			if roleRank[role] < roleRank[min] {
				writeErr(w, http.StatusForbidden, "forbidden")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// UserFrom extracts the authenticated user from ctx. Returns nil if not set.
func UserFrom(ctx context.Context) *model.User {
	u, _ := ctx.Value(ctxUser).(*model.User)
	return u
}

func extractKey(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); auth != "" {
		if rest, ok := strings.CutPrefix(auth, "Bearer "); ok {
			return rest
		}
	}
	if key := r.Header.Get("X-API-Key"); key != "" {
		return key
	}
	return r.URL.Query().Get("key")
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"error":%q}`, msg)
}
