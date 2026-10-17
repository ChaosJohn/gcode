package gemini

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Model discovery (ported from refresh_available_models).

// PersistedCatalog is the on-disk model cache.
type PersistedCatalog struct {
	Models           []string `json:"models"`
	FetchedAtRFC3339 string   `json:"fetched_at_rfc3339"`
}

// PersistedCatalogPath is the cache file under the gcode config dir.
func PersistedCatalogPath() string { return filepath.Join(gcodeDir(), "gemini_models_cache.json") }

func loadPersistedCatalog() []string {
	content, err := os.ReadFile(PersistedCatalogPath())
	if err != nil {
		return nil
	}
	var catalog PersistedCatalog
	if err := json.Unmarshal(content, &catalog); err != nil || len(catalog.Models) == 0 {
		return nil
	}
	return catalog.Models
}

func persistCatalog(models []string) {
	if len(models) == 0 {
		return
	}
	payload, err := json.Marshal(PersistedCatalog{
		Models:           models,
		FetchedAtRFC3339: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(PersistedCatalogPath()), 0o755)
	_ = os.WriteFile(PersistedCatalogPath(), payload, 0o644)
}

// SeedCachedCatalog primes the client's model list from the persisted cache.
func (c *Client) SeedCachedCatalog() {
	if models := loadPersistedCatalog(); len(models) > 0 {
		c.setFetchedModels(models)
	}
}

// RefreshAvailableModels discovers models for the active auth route and
// updates the fetched list + persisted cache.
func (c *Client) RefreshAvailableModels() ([]string, error) {
	if mode := ResolveAuthMode(); mode.isAPIKey() {
		return c.refreshAvailableModelsAPIKey(*mode.APIKey)
	}
	projectEnv := CloudProject()
	var projectPtr *string
	if projectEnv != "" {
		projectPtr = &projectEnv
	}
	var response map[string]any
	result, err := postJSON[map[string]any](c, "loadCodeAssist",
		LoadCodeAssistRequestFor(projectPtr, ClientMetadataFor(projectEnv)))
	if err != nil {
		if isVPCSCError(err.Error()) {
			result = nil
		} else {
			return nil, fmt.Errorf("Gemini model discovery failed during loadCodeAssist: %w", err)
		}
	}
	if result != nil {
		response = result
	}
	models := ExtractGeminiModelIDs(anyValue(response))
	return c.recordDiscoveredModels(models), nil
}

// refreshAvailableModelsAPIKey discovers via ListModels, stripping the
// `models/` name prefix before normalizing through the shared catalog merge.
func (c *Client) refreshAvailableModelsAPIKey(apiKey string) ([]string, error) {
	endpoint := DeveloperAPIBaseURL() + "/models"
	resp, err := c.doWithRetry(func() (*http.Request, error) {
		req, err := http.NewRequest("GET", endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("x-goog-api-key", apiKey)
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	})
	if err != nil {
		return c.recordDiscoveredModels(nil), nil
	}
	raw, readErr := readAll(resp.Body)
	resp.Body.Close()
	if readErr != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return c.recordDiscoveredModels(nil), nil
	}
	var payload struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return c.recordDiscoveredModels(nil), nil
	}
	var rawNames []string
	for _, model := range payload.Models {
		rawNames = append(rawNames, trimPrefix(model.Name, "models/"))
	}
	models := MergeGeminiModelLists(rawNames)
	return c.recordDiscoveredModels(models), nil
}

func (c *Client) recordDiscoveredModels(models []string) []string {
	if len(models) > 0 {
		c.setFetchedModels(models)
		persistCatalog(models)
	}
	return models
}

func anyValue(m map[string]any) any {
	if m == nil {
		return nil
	}
	return m
}

func trimPrefix(s, prefix string) string {
	return strings.TrimPrefix(s, prefix)
}
