package management

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"gopkg.in/yaml.v3"
)

type configCompareResponse struct {
	StaticAssetVersion string                    `json:"staticAssetVersion,omitempty"`
	Sources            []configCompareSource     `json:"sources"`
	Differences        []configCompareDifference `json:"differences"`
}

type configCompareSource struct {
	Key     string                 `json:"key"`
	Label   string                 `json:"label"`
	Path    string                 `json:"path,omitempty"`
	Exists  bool                   `json:"exists"`
	Active  bool                   `json:"active,omitempty"`
	Error   string                 `json:"error,omitempty"`
	Summary *redactedConfigSummary `json:"summary,omitempty"`
}

type redactedConfigSummary struct {
	StorageHome             string         `json:"storageHome,omitempty"`
	Host                    string         `json:"host,omitempty"`
	Port                    int            `json:"port,omitempty"`
	AuthDir                 string         `json:"authDir,omitempty"`
	ProviderGroups          map[string]int `json:"providerGroups"`
	ClientAPIKeyCount       int            `json:"clientApiKeyCount"`
	ManagementKeyConfigured bool           `json:"managementKeyConfigured"`
	StaticAssetVersion      string         `json:"staticAssetVersion,omitempty"`
	FileLogging             bool           `json:"fileLogging"`
	RequestLogging          bool           `json:"requestLogging"`
}

type configCompareDifference struct {
	Source string `json:"source"`
	Field  string `json:"field"`
	Active any    `json:"active"`
	Value  any    `json:"value"`
}

// GetConfigCompare returns redacted config summaries for runtime drift diagnostics.
func (h *Handler) GetConfigCompare(c *gin.Context) {
	staticVersion := managementStaticAssetVersion()
	sources := h.configCompareSources(staticVersion)
	c.JSON(http.StatusOK, configCompareResponse{
		StaticAssetVersion: staticVersion,
		Sources:            sources,
		Differences:        compareConfigSources(sources),
	})
}

func (h *Handler) configCompareSources(staticVersion string) []configCompareSource {
	activePath := ""
	if h != nil {
		activePath = strings.TrimSpace(h.configFilePath)
	}

	homeDir, _ := os.UserHomeDir()
	prodPath := ""
	devPath := ""
	if homeDir != "" {
		prodPath = filepath.Join(homeDir, ".llmdynamix", "config.yaml")
		devPath = filepath.Join(homeDir, ".llmdynamix-dev", "config.yaml")
	}

	candidates := []configCompareSource{
		{
			Key:    "effective",
			Label:  "Effective runtime",
			Path:   activePath,
			Active: true,
		},
		{
			Key:   "production",
			Label: "Production config",
			Path:  prodPath,
		},
		{
			Key:   "dev",
			Label: "Dev config",
			Path:  devPath,
		},
		{
			Key:   "bundled-default",
			Label: "Bundled default",
			Path:  bundledDefaultConfigPath(),
		},
	}

	for i := range candidates {
		if candidates[i].Key == "effective" && h != nil && h.cfg != nil {
			candidates[i].Exists = true
			candidates[i].Summary = summarizeEffectiveConfig(h.cfg, activePath, staticVersion)
			continue
		}
		candidates[i] = summarizeConfigFile(candidates[i], staticVersion)
	}

	return candidates
}

func summarizeEffectiveConfig(cfg *config.Config, path string, staticVersion string) *redactedConfigSummary {
	if cfg == nil {
		return nil
	}
	return &redactedConfigSummary{
		StorageHome:             storageHomeForConfigPath(path),
		Host:                    cfg.Host,
		Port:                    cfg.Port,
		AuthDir:                 cfg.AuthDir,
		ProviderGroups:          providerGroupsFromConfig(cfg),
		ClientAPIKeyCount:       len(cfg.SDKConfig.APIKeys),
		ManagementKeyConfigured: strings.TrimSpace(cfg.RemoteManagement.SecretKey) != "",
		StaticAssetVersion:      staticVersion,
		FileLogging:             cfg.LoggingToFile,
		RequestLogging:          cfg.RequestLog,
	}
}

func summarizeConfigFile(source configCompareSource, staticVersion string) configCompareSource {
	path := strings.TrimSpace(source.Path)
	if path == "" {
		source.Error = "path unavailable"
		return source
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			source.Error = "missing"
			return source
		}
		source.Error = err.Error()
		return source
	}
	source.Exists = true

	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		source.Error = err.Error()
		return source
	}

	source.Summary = summarizeRawConfig(raw, path, staticVersion)
	return source
}

func summarizeRawConfig(raw map[string]any, path string, staticVersion string) *redactedConfigSummary {
	return &redactedConfigSummary{
		StorageHome:             storageHomeForConfigPath(path),
		Host:                    stringField(raw, "host"),
		Port:                    intField(raw, "port"),
		AuthDir:                 stringField(raw, "auth-dir"),
		ProviderGroups:          providerGroupsFromRaw(raw),
		ClientAPIKeyCount:       listCount(raw["api-keys"]),
		ManagementKeyConfigured: nestedStringConfigured(raw, "remote-management", "secret-key"),
		StaticAssetVersion:      staticVersion,
		FileLogging:             boolField(raw, "logging-to-file"),
		RequestLogging:          boolField(raw, "request-log"),
	}
}

func providerGroupsFromConfig(cfg *config.Config) map[string]int {
	groups := map[string]int{
		"claude":              len(cfg.ClaudeKey),
		"codex":               len(cfg.CodexKey),
		"gemini":              len(cfg.GeminiKey),
		"openaiCompatibility": len(cfg.OpenAICompatibility),
		"vertex":              len(cfg.VertexCompatAPIKey),
	}
	return groups
}

func providerGroupsFromRaw(raw map[string]any) map[string]int {
	return map[string]int{
		"claude":              listCount(raw["claude-api-key"]),
		"codex":               listCount(raw["codex-api-key"]),
		"gemini":              listCount(raw["gemini-api-key"]),
		"openaiCompatibility": listCount(raw["openai-compatibility"]),
		"vertex":              listCount(raw["vertex-api-key"]),
	}
}

func compareConfigSources(sources []configCompareSource) []configCompareDifference {
	var active *redactedConfigSummary
	for i := range sources {
		if sources[i].Key == "effective" {
			active = sources[i].Summary
			break
		}
	}
	if active == nil {
		return nil
	}

	var diffs []configCompareDifference
	for i := range sources {
		source := sources[i]
		if source.Key == "effective" || source.Summary == nil {
			continue
		}
		addDiff := func(field string, activeValue any, sourceValue any) {
			if !reflect.DeepEqual(activeValue, sourceValue) {
				diffs = append(diffs, configCompareDifference{
					Source: source.Key,
					Field:  field,
					Active: activeValue,
					Value:  sourceValue,
				})
			}
		}
		addDiff("storageHome", active.StorageHome, source.Summary.StorageHome)
		addDiff("port", active.Port, source.Summary.Port)
		addDiff("authDir", active.AuthDir, source.Summary.AuthDir)
		addDiff("providerGroups", active.ProviderGroups, source.Summary.ProviderGroups)
		addDiff("clientApiKeyCount", active.ClientAPIKeyCount, source.Summary.ClientAPIKeyCount)
		addDiff("managementKeyConfigured", active.ManagementKeyConfigured, source.Summary.ManagementKeyConfigured)
		addDiff("staticAssetVersion", active.StaticAssetVersion, source.Summary.StaticAssetVersion)
		addDiff("fileLogging", active.FileLogging, source.Summary.FileLogging)
	}

	sort.SliceStable(diffs, func(i, j int) bool {
		if diffs[i].Source == diffs[j].Source {
			return diffs[i].Field < diffs[j].Field
		}
		return diffs[i].Source < diffs[j].Source
	})
	return diffs
}

func storageHomeForConfigPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return filepath.Dir(path)
}

func bundledDefaultConfigPath() string {
	staticPath := strings.TrimSpace(os.Getenv("MANAGEMENT_STATIC_PATH"))
	if staticPath == "" {
		return ""
	}
	resourceDir := filepath.Dir(filepath.Dir(staticPath))
	return filepath.Join(resourceDir, "config.yaml")
}

func managementStaticAssetVersion() string {
	staticPath := strings.TrimSpace(os.Getenv("MANAGEMENT_STATIC_PATH"))
	if staticPath == "" {
		return ""
	}
	return filepath.Base(staticPath)
}

func stringField(raw map[string]any, key string) string {
	value, ok := raw[key]
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(toString(value))
}

func intField(raw map[string]any, key string) int {
	switch value := raw[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case string:
		value = strings.TrimSpace(value)
		parsed, _ := strconv.Atoi(value)
		return parsed
	default:
		return 0
	}
}

func boolField(raw map[string]any, key string) bool {
	switch value := raw[key].(type) {
	case bool:
		return value
	case string:
		return strings.EqualFold(strings.TrimSpace(value), "true")
	default:
		return false
	}
}

func nestedStringConfigured(raw map[string]any, parent string, child string) bool {
	nested, ok := raw[parent].(map[string]any)
	if !ok {
		return false
	}
	return stringField(nested, child) != ""
}

func listCount(value any) int {
	items, ok := value.([]any)
	if !ok {
		return 0
	}
	return len(items)
}

func toString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	default:
		return fmt.Sprint(typed)
	}
}
