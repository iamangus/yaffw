package domain

import (
	"fmt"
	"sync"
)

// DynamicMediaType represents a dynamically discovered media type
type DynamicMediaType struct {
	Name           string   `json:"name"`
	DisplayName    string   `json:"display_name"`
	Icon           string   `json:"icon"`
	Description    string   `json:"description"`
	IsLeaf         bool     `json:"is_leaf"`
	HasFiles       bool     `json:"has_files"`
	ParentTypes    []string `json:"parent_types"`
	ChildTypes     []string `json:"child_types"`
	Variants       []string `json:"variants"`
	QualityProfile string   `json:"default_quality_profile"`
	BackendID      string   `json:"backend_id"`
	Source         string   `json:"source"` // "discovered" or "configured"
}

// EntityTypeInfo provides complete information about an entity type
type EntityTypeInfo struct {
	Type           string            `json:"type"`
	Hierarchy      map[string]string `json:"hierarchy"` // parent->child relationships
	MaxDepth       int               `json:"max_depth"`
	LeafTypes      []string          `json:"leaf_types"`
	ContainerTypes []string          `json:"container_types"`
	DefaultFields  []string          `json:"default_fields"`
}

// HierarchyPath represents a complete hierarchical path from root to leaf
type HierarchyPath struct {
	Path       []HierarchyItem `json:"path"`
	EntityType string          `json:"entity_type"`
	MaxDepth   int             `json:"max_depth"`
}

// HierarchyItem represents a single level in a hierarchy
type HierarchyItem struct {
	Type     string   `json:"type"`
	Name     string   `json:"name"`
	Parent   *string  `json:"parent,omitempty"`
	Children []string `json:"children,omitempty"`
}

// DiscoveryBackend represents a dynamically discovered backend with its capabilities
type DiscoveryBackend struct {
	ID            string                      `json:"id"`
	URL           string                      `json:"url"`
	Name          string                      `json:"name"`
	Version       string                      `json:"version"`
	Status        string                      `json:"status"`
	MediaTypes    map[string]DynamicMediaType `json:"media_types"`
	Architectures ArchitectureInfo            `json:"architecture"`
	Providers     []ProviderInfo              `json:"providers"`
	Capabilities  CapabilitiesInfo            `json:"capabilities"`
	DiscoveryTime string                      `json:"discovery_time"`
	LastRefresh   string                      `json:"last_refresh"`
}

// ArchitectureInfo represents system architecture information from omniarr
type ArchitectureInfo struct {
	Version  string       `json:"version"`
	Database DatabaseInfo `json:"database"`
	Storage  StorageInfo  `json:"storage"`
	Features []string     `json:"features"`
}

// DatabaseInfo represents database configuration
type DatabaseInfo struct {
	Type        string `json:"type"`
	Version     string `json:"version"`
	MaxEntities int    `json:"max_entities"`
}

// StorageInfo represents storage configuration
type StorageInfo struct {
	Type      string `json:"type"`
	Path      string `json:"path"`
	Available bool   `json:"available"`
}

// ProviderInfo represents metadata provider information
type ProviderInfo struct {
	Name           string   `json:"name"`
	Type           string   `json:"type"`
	Available      bool     `json:"available"`
	SupportedTypes []string `json:"supported_types"`
}

// CapabilitiesInfo represents system capabilities
type CapabilitiesInfo struct {
	Streaming        bool `json:"streaming"`
	Transcoding      bool `json:"transcoding"`
	MetadataFetching bool `json:"metadata_fetching"`
	Search           bool `json:"search"`
	Download         bool `json:"download"`
	Import           bool `json:"import"`
}

// EntityTypeRegistry manages dynamically discovered entity types
type EntityTypeRegistry struct {
	mu       sync.RWMutex
	backends map[string]DiscoveryBackend
	types    map[string]DynamicMediaType
}

// NewEntityTypeRegistry creates a new registry for dynamic entity types
func NewEntityTypeRegistry() *EntityTypeRegistry {
	return &EntityTypeRegistry{
		backends: make(map[string]DiscoveryBackend),
		types:    make(map[string]DynamicMediaType),
	}
}

// RegisterBackend adds a new discovered backend to the registry
func (r *EntityTypeRegistry) RegisterBackend(backend DiscoveryBackend) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.backends[backend.ID] = backend

	// Register all media types from this backend
	for _, mediaType := range backend.MediaTypes {
		// Create a globally unique key combining backend ID and type
		globalType := fmt.Sprintf("%s:%s", backend.ID, mediaType.Name)
		mediaType.BackendID = backend.ID
		mediaType.Source = "discovered"
		r.types[globalType] = mediaType
	}

	return nil
}

// GetBackendTypes returns all media types for a specific backend
func (r *EntityTypeRegistry) GetBackendTypes(backendID string) []DynamicMediaType {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var types []DynamicMediaType
	for _, mediaType := range r.types {
		if mediaType.BackendID == backendID {
			types = append(types, mediaType)
		}
	}
	return types
}

// GetMediaType returns information about a specific media type
func (r *EntityTypeRegistry) GetMediaType(typeName, backendID string) (DynamicMediaType, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	globalType := fmt.Sprintf("%s:%s", backendID, typeName)
	typeInfo, exists := r.types[globalType]
	return typeInfo, exists
}

// GetAllTypes returns all registered media types from all backends
func (r *EntityTypeRegistry) GetAllTypes() []DynamicMediaType {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var allTypes []DynamicMediaType
	for _, mediaType := range r.types {
		allTypes = append(allTypes, mediaType)
	}
	return allTypes
}

// GetHierarchicalTypes returns entity types that form a hierarchy
func (r *EntityTypeRegistry) GetHierarchicalTypes(backendID string) [][]string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	hierarchies := [][]string{}

	backend, exists := r.backends[backendID]
	if !exists {
		return hierarchies
	}

	// Build all possible hierarchies based on parent-child relationships
	rootTypes := []string{}
	for _, mediaType := range backend.MediaTypes {
		if len(mediaType.ParentTypes) == 0 {
			rootTypes = append(rootTypes, mediaType.Name)
		}
	}

	for _, rootType := range rootTypes {
		hierarchy := []string{rootType}
		r.buildHierarchyPath(backendID, rootType, &hierarchy)
		if len(hierarchy) > 1 {
			hierarchies = append(hierarchies, hierarchy)
		}
	}

	return hierarchies
}

// buildHierarchyPath recursively builds hierarchy chains (helper method)
func (r *EntityTypeRegistry) buildHierarchyPath(backendID string, currentType string, path *[]string) {
	// Find children of current type
	for _, mediaType := range r.types {
		if mediaType.BackendID == backendID && contains(mediaType.ParentTypes, currentType) {
			*path = append(*path, mediaType.Name)
			r.buildHierarchyPath(backendID, mediaType.Name, path)
			return
		}
	}
}

// Helper function
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
