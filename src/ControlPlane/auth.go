package main

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/yaffw/yaffw/src/internal/config"
	"github.com/yaffw/yaffw/src/internal/domain"
	"github.com/yaffw/yaffw/src/internal/ports"
)

type AuthMiddleware struct {
	Verifier *oidc.IDTokenVerifier
	UserRepo ports.UserRepository
}

func NewAuthMiddleware(userRepo ports.UserRepository, oidcCfg config.OIDCConfig) *AuthMiddleware {
	if oidcCfg.ProviderURL == "" {
		log.Println("WARNING: OIDC Provider URL not set. Auth will be disabled (or broken if required).")
		return &AuthMiddleware{UserRepo: userRepo}
	}

	ctx := context.Background()
	provider, err := oidc.NewProvider(ctx, oidcCfg.ProviderURL)
	if err != nil {
		// Don't crash, just log error and fail later if auth is used
		log.Printf("Failed to query OIDC provider: %v", err)
		return &AuthMiddleware{UserRepo: userRepo}
	}

	// For Access Tokens, we often need to skip ClientID check as 'aud' might not match client_id
	oidcConfig := &oidc.Config{
		ClientID:          oidcCfg.ClientID,
		SkipClientIDCheck: true,
	}
	verifier := provider.Verifier(oidcConfig)

	return &AuthMiddleware{
		Verifier: verifier,
		UserRepo: userRepo,
	}
}

func (m *AuthMiddleware) RequireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if m.Verifier == nil {
			// If OIDC is not configured, maybe bypass?
			// For now, fail secure.
			http.Error(w, "OIDC not configured on server", http.StatusServiceUnavailable)
			return
		}

		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			http.Error(w, "Missing Authorization header", http.StatusUnauthorized)
			return
		}

		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			http.Error(w, "Invalid Authorization header format", http.StatusUnauthorized)
			return
		}

		tokenStr := parts[1]
		ctx := r.Context()

		// Verify Token
		idToken, err := m.Verifier.Verify(ctx, tokenStr)
		if err != nil {
			log.Printf("Token verification failed: %v", err)
			http.Error(w, "Invalid token", http.StatusUnauthorized)
			return
		}

		// Extract claims
		var claims struct {
			Sub               string `json:"sub"`
			Email             string `json:"email"`
			PreferredUsername string `json:"preferred_username"`
		}
		if err := idToken.Claims(&claims); err != nil {
			http.Error(w, "Invalid token claims", http.StatusUnauthorized)
			return
		}

		// JIT Provisioning (Upsert User)
		// We do this async or on every request?
		// Doing it on every request ensures LastSeen is up to date, but adds DB write.
		// Optimization: Check cache or only update if older than X. For now, simple.

		// Try to find user
		user, err := m.UserRepo.GetByID(ctx, claims.Sub)
		if err != nil {
			// User doesn't exist, create new
			user = &domain.User{
				ID:        claims.Sub,
				Email:     claims.Email,
				CreatedAt: time.Now(),
				LastSeen:  time.Now(),
			}
			if user.Email == "" {
				user.Email = claims.PreferredUsername
			}

			if err := m.UserRepo.Save(ctx, user); err != nil {
				log.Printf("Failed to create user %s: %v", claims.Sub, err)
				http.Error(w, "User provisioning failed", http.StatusInternalServerError)
				return
			}
			log.Printf("Provisioned new user: %s (%s)", user.ID, user.Email)
		} else {
			// Update LastSeen
			user.LastSeen = time.Now()
			// Update email if changed
			if claims.Email != "" && user.Email != claims.Email {
				user.Email = claims.Email
			}
			m.UserRepo.Save(ctx, user)
		}

		// Inject UserID into context
		ctx = context.WithValue(ctx, "userID", user.ID)
		next.ServeHTTP(w, r.WithContext(ctx))
	}
}

func GetUserID(ctx context.Context) string {
	id, ok := ctx.Value("userID").(string)
	if !ok {
		return ""
	}
	return id
}
