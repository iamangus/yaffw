package main

import (
	"net/http"
	"strings"
)

type SimpleAuthMiddleware struct {
	apiKey string
}

func NewSimpleAuthMiddleware(apiKey string) *SimpleAuthMiddleware {
	return &SimpleAuthMiddleware{apiKey: apiKey}
}

func (m *SimpleAuthMiddleware) RequireAPIKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m.apiKey == "" {
			next.ServeHTTP(w, r)
			return
		}

		auth := r.Header.Get("Authorization")
		if auth == "" {
			http.Error(w, "missing authorization header", http.StatusUnauthorized)
			return
		}

		if !strings.HasPrefix(auth, "Bearer ") {
			http.Error(w, "invalid authorization format", http.StatusUnauthorized)
			return
		}

		token := strings.TrimPrefix(auth, "Bearer ")
		if token != m.apiKey {
			http.Error(w, "invalid api key", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}
