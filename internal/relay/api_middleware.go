package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/Muster-dev/muster-fleet-cloud/internal/auth"
)

type contextKey string

const adminTokenKey contextKey = "admin_token"

// adminAuth wraps an HTTP handler with admin token authentication.
func (s *Server) adminAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if header == "" || !strings.HasPrefix(header, "Bearer ") {
			writeJSON(w, http.StatusUnauthorized, map[string]string{
				"error": "missing or invalid Authorization header",
			})
			return
		}

		raw := strings.TrimPrefix(header, "Bearer ")
		tok, err := s.tokenStore.ValidateToken(raw)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{
				"error": "invalid token",
			})
			return
		}

		if tok.TokenType != auth.TypeAdmin {
			writeJSON(w, http.StatusUnauthorized, map[string]string{
				"error": "admin token required",
			})
			return
		}

		ctx := context.WithValue(r.Context(), adminTokenKey, tok)
		next(w, r.WithContext(ctx))
	}
}

// adminTokenFromContext extracts the admin token from the request context.
func adminTokenFromContext(r *http.Request) *auth.Token {
	tok, _ := r.Context().Value(adminTokenKey).(*auth.Token)
	return tok
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
