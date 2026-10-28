package omniarr

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

type OmniarrClient struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

func NewOmniarrClient(baseURL, apiKey string) *OmniarrClient {
	return &OmniarrClient{
		baseURL: baseURL,
		apiKey:  apiKey,
		client:  &http.Client{Timeout: 10 * time.Second},
	}
}

type Entity struct {
	UUID       string                 `json:"uuid"`
	EntityType string                 `json:"entity_type"`
	Status     string                 `json:"status"`
	LocalPath  string                 `json:"local_path"`
	ImagePath  string                 `json:"image_path"`
	Metadata   map[string]interface{} `json:"metadata"`
	ParentUUID string                 `json:"parent_uuid,omitempty"`
}

func (c *OmniarrClient) ListEntities(entityType, parentUUID, status string) ([]Entity, error) {
	u, err := url.Parse(c.baseURL + "/entities")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	if entityType != "" {
		q.Set("type", entityType)
	}
	if parentUUID != "" {
		q.Set("parent", parentUUID)
	}
	if status != "" {
		q.Set("status", status)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequest("GET", u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-Key", c.apiKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("omniarr API error: %s", resp.Status)
	}

	var entities []Entity
	if err := json.NewDecoder(resp.Body).Decode(&entities); err != nil {
		return nil, err
	}

	return entities, nil
}

func (c *OmniarrClient) GetEntityDetails(uuid string) (*Entity, error) {
	u := fmt.Sprintf("%s/entities/%s", c.baseURL, uuid)
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-Key", c.apiKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("omniarr API error: %s", resp.Status)
	}

	var response struct {
		Entity Entity `json:"entity"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, err
	}

	return &response.Entity, nil
}

func (c *OmniarrClient) SearchEntities(title, entityType string) ([]Entity, error) {
	u, err := url.Parse(c.baseURL + "/entities/search")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("title", title)
	if entityType != "" {
		q.Set("type", entityType)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequest("GET", u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-Key", c.apiKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("omniarr API error: %s", resp.Status)
	}

	var entities []Entity
	if err := json.NewDecoder(resp.Body).Decode(&entities); err != nil {
		return nil, err
	}

	return entities, nil
}
