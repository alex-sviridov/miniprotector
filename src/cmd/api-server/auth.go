// src/cmd/api-server/auth.go
package main

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// requireBearerToken guards next with a single shared bearer token --
// api-server's only auth layer in v1 (see
// docs/superpowers/specs/2026-07-14-api-server-design.md, "no RBAC yet").
func requireBearerToken(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "Bearer "
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, prefix) {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		presented := strings.TrimPrefix(auth, prefix)
		if subtle.ConstantTimeCompare([]byte(presented), []byte(token)) != 1 {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}
