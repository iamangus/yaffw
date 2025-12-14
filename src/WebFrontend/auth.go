package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

type AuthService struct {
	Provider *oidc.Provider
	Config   oauth2.Config
	Enabled  bool
}

func NewAuthService() *AuthService {
	providerURL := os.Getenv("OIDC_PROVIDER")
	clientID := os.Getenv("OIDC_CLIENT_ID")
	clientSecret := os.Getenv("OIDC_CLIENT_SECRET")
	redirectURL := os.Getenv("OIDC_REDIRECT_URL")

	if providerURL == "" {
		log.Println("OIDC_PROVIDER not set. Frontend Auth disabled.")
		return &AuthService{Enabled: false}
	}

	provider, err := oidc.NewProvider(context.Background(), providerURL)
	if err != nil {
		log.Printf("Failed to init OIDC provider: %v", err)
		return &AuthService{Enabled: false}
	}

	conf := oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
		Endpoint:     provider.Endpoint(),
		Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
	}

	return &AuthService{
		Provider: provider,
		Config:   conf,
		Enabled:  true,
	}
}

func (s *AuthService) HandleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.Enabled {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	// TODO: Generate proper random state and store in session/cookie for CSRF protection
	state := "random-state-xyz"
	http.Redirect(w, r, s.Config.AuthCodeURL(state), http.StatusFound)
}

func (s *AuthService) HandleCallback(w http.ResponseWriter, r *http.Request) {
	if !s.Enabled {
		http.Error(w, "Auth disabled", http.StatusBadRequest)
		return
	}

	if r.URL.Query().Get("state") != "random-state-xyz" {
		http.Error(w, "State mismatch", http.StatusBadRequest)
		return
	}

	oauth2Token, err := s.Config.Exchange(r.Context(), r.URL.Query().Get("code"))
	if err != nil {
		http.Error(w, "Failed to exchange token: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Extract ID Token to get user info (optional, for UI)
	rawIDToken, ok := oauth2Token.Extra("id_token").(string)
	if ok {
		// Verify ID Token (optional here since we trust the provider exchange, but good practice)
		verifier := s.Provider.Verifier(&oidc.Config{ClientID: s.Config.ClientID})
		_, err := verifier.Verify(r.Context(), rawIDToken)
		if err != nil {
			log.Printf("ID Token verification failed: %v", err)
		} else {
			// Store ID token or Claims if needed for UI
			// For now, we just care about Access Token for the backend
		}
	}

	// Store Access Token in cookie
	// Note: Access Tokens can be large. If too large for cookie, use server-side session.
	// Keycloak tokens are usually fine (~1-2KB).
	http.SetCookie(w, &http.Cookie{
		Name:     "yaffw_token",
		Value:    oauth2Token.AccessToken,
		HttpOnly: true,
		Path:     "/",
		SameSite: http.SameSiteLaxMode,
		// Secure: true, // TODO: Enable in production
	})

	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *AuthService) HandleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     "yaffw_token",
		Value:    "",
		HttpOnly: true,
		Path:     "/",
		MaxAge:   -1,
	})
	http.Redirect(w, r, "/", http.StatusFound)
}

// TokenMiddleware injects the Authorization header if cookie is present
func (s *AuthService) TokenMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("yaffw_token")
		if err == nil && cookie.Value != "" {
			// Inject Authorization header for proxy
			r.Header.Set("Authorization", "Bearer "+cookie.Value)
		}
		next.ServeHTTP(w, r)
	})
}
