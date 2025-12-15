package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ControlPlaneConfig holds configuration for the Control Plane service
type ControlPlaneConfig struct {
	DatabaseURL string     `json:"database_url" yaml:"database_url"`
	DataDir     string     `json:"data_dir" yaml:"data_dir"`
	Port        string     `json:"port" yaml:"port"`
	TMDBAPIKey  string     `json:"tmdb_api_key" yaml:"tmdb_api_key"`
	ArtworkDir  string     `json:"artwork_dir" yaml:"artwork_dir"`
	OIDC        OIDCConfig `json:"oidc" yaml:"oidc"`
}

// ComputePlaneConfig holds configuration for the Compute Plane service
type ComputePlaneConfig struct {
	ControlURL string `json:"control_url" yaml:"control_url"`
	WorkerPort string `json:"worker_port" yaml:"worker_port"`
}

// WebFrontendConfig holds configuration for the Web Frontend service
type WebFrontendConfig struct {
	ControlPlaneURL string     `json:"control_plane_url" yaml:"control_plane_url"`
	Port            string     `json:"port" yaml:"port"`
	OIDC            OIDCConfig `json:"oidc" yaml:"oidc"`
}

type OIDCConfig struct {
	ProviderURL  string `json:"provider_url" yaml:"provider_url"`
	ClientID     string `json:"client_id" yaml:"client_id"`
	ClientSecret string `json:"client_secret" yaml:"client_secret"`
	RedirectURL  string `json:"redirect_url" yaml:"redirect_url"`
}

// Load loads the configuration from a file (YAML or JSON)
func Load(path string, cfg interface{}) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("failed to open config file %s: %w", path, err)
	}
	defer file.Close()

	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".yaml" || ext == ".yml" {
		decoder := yaml.NewDecoder(file)
		if err := decoder.Decode(cfg); err != nil {
			return fmt.Errorf("failed to decode YAML config file %s: %w", path, err)
		}
	} else {
		// Default to JSON for compatibility or other extensions
		decoder := json.NewDecoder(file)
		if err := decoder.Decode(cfg); err != nil {
			return fmt.Errorf("failed to decode JSON config file %s: %w", path, err)
		}
	}

	return nil
}
