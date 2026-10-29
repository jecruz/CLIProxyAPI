package registry

import (
	"encoding/json"
	"math"
	"strings"
)

// ModelCapabilities records optional, verified metadata; nil booleans mean unknown.
// Source is required for operator overrides and describes the route-specific evidence.
type ModelCapabilities struct {
	Source             string   `json:"source,omitempty" yaml:"source,omitempty"`
	ContextLength      int      `json:"context_length,omitempty" yaml:"context-length,omitempty"`
	MaxOutputTokens    int      `json:"max_output_tokens,omitempty" yaml:"max-output-tokens,omitempty"`
	SupportsReasoning  *bool    `json:"supports_reasoning,omitempty" yaml:"supports-reasoning,omitempty"`
	ReasoningLevels    []string `json:"reasoning_levels,omitempty" yaml:"reasoning-levels,omitempty"`
	SupportsToolCalls  *bool    `json:"supports_tool_calls,omitempty" yaml:"supports-tool-calls,omitempty"`
	SupportsImageInput *bool    `json:"supports_image_input,omitempty" yaml:"supports-image-input,omitempty"`
}

// Fields returns only known, valid OpenAI-compatible capability extensions.
func (c ModelCapabilities) Fields() map[string]any {
	out := make(map[string]any)
	if c.ContextLength > 0 {
		out["context_length"] = c.ContextLength
	}
	if c.MaxOutputTokens > 0 {
		out["max_output_tokens"] = c.MaxOutputTokens
	}
	for key, value := range map[string]*bool{"supports_reasoning": c.SupportsReasoning, "supports_tool_calls": c.SupportsToolCalls, "supports_image_input": c.SupportsImageInput} {
		if value != nil {
			out[key] = *value
		}
	}
	levels := metadataStrings(c.ReasoningLevels)
	if len(levels) > 0 && (c.SupportsReasoning == nil || *c.SupportsReasoning) {
		out["reasoning_levels"] = levels
		out["supports_reasoning"] = true
	}
	return out
}

// Clone copies optional booleans and slices so callers cannot mutate registry snapshots.
func (c ModelCapabilities) Clone() *ModelCapabilities {
	copy := c
	copy.ReasoningLevels = append([]string(nil), c.ReasoningLevels...)
	if c.SupportsReasoning != nil {
		value := *c.SupportsReasoning
		copy.SupportsReasoning = &value
	}
	if c.SupportsToolCalls != nil {
		value := *c.SupportsToolCalls
		copy.SupportsToolCalls = &value
	}
	if c.SupportsImageInput != nil {
		value := *c.SupportsImageInput
		copy.SupportsImageInput = &value
	}
	return &copy
}

// CapabilitiesFromMetadata reads declared provider fields without model-name heuristics.
// OpenRouter's top_provider and architecture shapes are accepted alongside engine metadata.
func CapabilitiesFromMetadata(model map[string]any) ModelCapabilities {
	c := ModelCapabilities{ContextLength: metadataLimit(model["context_length"]), MaxOutputTokens: metadataLimit(model["max_output_tokens"])}
	if c.ContextLength == 0 {
		c.ContextLength = metadataLimit(model["inputTokenLimit"])
	}
	if c.MaxOutputTokens == 0 {
		c.MaxOutputTokens = metadataLimit(model["max_completion_tokens"])
	}
	if c.MaxOutputTokens == 0 {
		c.MaxOutputTokens = metadataLimit(model["outputTokenLimit"])
	}
	if top, ok := model["top_provider"].(map[string]any); ok && c.MaxOutputTokens == 0 {
		c.MaxOutputTokens = metadataLimit(top["max_completion_tokens"])
	}
	c.SupportsReasoning = metadataBool(model["supports_reasoning"])
	c.SupportsToolCalls = metadataBool(model["supports_tool_calls"])
	c.SupportsImageInput = metadataBool(model["supports_image_input"])
	c.ReasoningLevels = metadataStrings(model["reasoning_levels"])
	if thinking, ok := model["thinking"].(map[string]any); ok {
		if len(c.ReasoningLevels) == 0 {
			c.ReasoningLevels = metadataStrings(thinking["levels"])
		}
		if c.SupportsReasoning == nil && (len(c.ReasoningLevels) > 0 || metadataLimit(thinking["max"]) > 0) {
			c.SupportsReasoning = metadataBool(true)
		}
	}
	// A listed supported parameter is positive evidence; its absence is not a denial.
	for _, parameter := range metadataStrings(model["supported_parameters"]) {
		switch parameter {
		case "reasoning", "reasoning_effort":
			if c.SupportsReasoning == nil {
				c.SupportsReasoning = metadataBool(true)
			}
		case "tools":
			if c.SupportsToolCalls == nil {
				c.SupportsToolCalls = metadataBool(true)
			}
		}
	}
	modalities := metadataStrings(model["supportedInputModalities"])
	if architecture, ok := model["architecture"].(map[string]any); ok {
		modalities = append(modalities, metadataStrings(architecture["input_modalities"])...)
	}
	for _, modality := range modalities {
		if strings.EqualFold(modality, "image") && c.SupportsImageInput == nil {
			c.SupportsImageInput = metadataBool(true)
		}
	}
	return c
}

// ModelCapabilityFields projects verified registry metadata and ignores internal reasoning defaults.
func ModelCapabilityFields(model *ModelInfo) map[string]any {
	if model == nil {
		return map[string]any{}
	}
	c := CapabilitiesFromMetadata(map[string]any{"context_length": model.ContextLength, "max_completion_tokens": model.MaxCompletionTokens, "inputTokenLimit": model.InputTokenLimit, "outputTokenLimit": model.OutputTokenLimit, "supported_parameters": model.SupportedParameters, "supportedInputModalities": model.SupportedInputModalities})
	if model.Thinking != nil && !model.ThinkingDefault && (len(model.Thinking.Levels) > 0 || model.Thinking.Max > 0 || model.Thinking.DynamicAllowed) {
		c.SupportsReasoning = metadataBool(true)
		c.ReasoningLevels = append([]string(nil), model.Thinking.Levels...)
	}
	out := c.Fields()
	if model.Capabilities != nil {
		for k, v := range model.Capabilities.Fields() {
			out[k] = v
		}
	}
	if out["supports_reasoning"] == false {
		delete(out, "reasoning_levels")
	}
	return out
}

// ApplyModelCapabilities adds verified provider metadata and sourced per-alias overrides.
// Overrides match the exact published ID, so one provider route cannot imply another's limits.
func ApplyModelCapabilities(model map[string]any, overrides map[string]ModelCapabilities) {
	for k, v := range CapabilitiesFromMetadata(model).Fields() {
		model[k] = v
	}
	id, _ := model["id"].(string)
	if override, ok := overrides[id]; ok && strings.TrimSpace(override.Source) != "" {
		for k, v := range override.Fields() {
			model[k] = v
		}
		model["capability_source"] = strings.TrimSpace(override.Source)
	}
	if model["supports_reasoning"] == false {
		delete(model, "reasoning_levels")
	}
}

func metadataBool(value any) *bool {
	if b, ok := value.(bool); ok {
		return &b
	}
	return nil
}

func metadataLimit(value any) int {
	var n float64
	switch v := value.(type) {
	case int:
		if v > 0 {
			return v
		}
		return 0
	case int64:
		if v > 0 && uint64(v) <= uint64(^uint(0)>>1) {
			return int(v)
		}
		return 0
	case float64:
		n = v
	case json.Number:
		parsed, err := v.Float64()
		if err != nil {
			return 0
		}
		n = parsed
	default:
		return 0
	}
	// JSON's portable exact-integer range also avoids implementation-specific float-to-int overflow.
	if n > 0 && n <= 9007199254740991 && n < float64(^uint(0)>>1) && math.Trunc(n) == n {
		return int(n)
	}
	return 0
}

func metadataStrings(value any) []string {
	var raw []string
	switch v := value.(type) {
	case []string:
		raw = v
	case []any:
		for _, entry := range v {
			s, ok := entry.(string)
			if !ok {
				return nil
			}
			raw = append(raw, s)
		}
	}
	var out []string
	for _, s := range raw {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
