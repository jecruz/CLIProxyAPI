package management

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestLocalDiscoverySurvivesManagementRoundTripAndPatch(t *testing.T) {
	h := &Handler{cfg: &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{Name: "bay", BaseURL: "http://127.0.0.1:8318/v1", DiscoverLocalModels: true}}}, configFilePath: writeTestConfigFile(t)}
	rr := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rr)
	c.Request = httptest.NewRequest("GET", "/v0/management/openai-compatibility", nil)
	h.GetOpenAICompat(c)
	var response map[string]json.RawMessage
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(response["openai-compatibility"], []byte(`"discover-local-models":true`)) {
		t.Error("management GET dropped discovery flag")
	}
	rr = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(rr)
	c.Request = httptest.NewRequest("PUT", "/v0/management/openai-compatibility", bytes.NewReader(response["openai-compatibility"]))
	h.PutOpenAICompat(c)
	if rr.Code != 200 || !h.cfg.OpenAICompatibility[0].DiscoverLocalModels {
		t.Errorf("GET-to-PUT disabled discovery, status=%d", rr.Code)
	}
	for _, value := range []bool{false, true} {
		body, _ := json.Marshal(map[string]any{"name": "bay", "value": map[string]any{"discover-local-models": value}})
		rr = httptest.NewRecorder()
		c, _ = gin.CreateTestContext(rr)
		c.Request = httptest.NewRequest("PATCH", "/v0/management/openai-compatibility", bytes.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		h.PatchOpenAICompat(c)
		if rr.Code != 200 || h.cfg.OpenAICompatibility[0].DiscoverLocalModels != value {
			t.Errorf("PATCH discovery=%v not preserved, status=%d", value, rr.Code)
		}
	}
	saved, err := config.LoadConfig(h.configFilePath)
	if err != nil {
		t.Fatal(err)
	}
	if !saved.OpenAICompatibility[0].DiscoverLocalModels {
		t.Fatal("discovery flag not persisted")
	}
}
