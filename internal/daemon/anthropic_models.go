package daemon

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/get-vix/vix/internal/config"
)

// Anthropic model discovery: when the user points ANTHROPIC_BASE_URL (or a
// stored custom endpoint) at a proxy instead of api.anthropic.com, the Models
// tab live-lists whatever the proxy's /v1/models advertises instead of the
// static providers.json catalogue. Unlike the local-provider probe in
// local_providers.go, this reuses the vendored Anthropic SDK's ModelService
// so headers (x-api-key/anthropic-version) and pagination match the real
// chat-request path exactly (internal/daemon/llm/anthropic.go).

// anthropicOverride resolves the anthropic credential and reports the
// resolved base URL when it differs from the default (i.e. ANTHROPIC_BASE_URL
// or a stored custom endpoint is set). ok is false when the user is on the
// default api.anthropic.com endpoint — callers should skip probing.
func anthropicOverride() (cred config.Credential, ok bool) {
	cred = config.ResolveProviderCredential("anthropic")
	return cred, cred.BaseURL != ""
}

// probeAnthropicModels lists models from a proxy's /v1/models using the same
// SDK client construction as real chat requests (NewAnthropic in
// internal/daemon/llm/anthropic.go), minus the chat-specific wrapper.
func probeAnthropicModels(ctx context.Context, cred config.Credential) LocalProviderState {
	st := LocalProviderState{Provider: "anthropic", BaseURL: cred.BaseURL}

	opts := []option.RequestOption{option.WithMaxRetries(0), option.WithBaseURL(cred.BaseURL)}
	opts = append(opts, cred.RequestOptions()...)
	sdk := anthropic.NewClient(opts...)

	pctx, cancel := context.WithTimeout(ctx, localProbeTimeout)
	defer cancel()

	pager := sdk.Models.ListAutoPaging(pctx, anthropic.ModelListParams{})
	var models []LocalModel
	for pager.Next() {
		m := pager.Current()
		if m.ID == "" {
			continue
		}
		models = append(models, LocalModel{Spec: "anthropic/" + m.ID, DisplayName: m.DisplayName})
	}
	if pager.Err() != nil {
		return st // unreachable or non-conforming: Reachable stays false
	}
	st.Reachable = true
	st.Models = models
	return st
}

// RegisterAnthropicModelHandlers wires the Anthropic model-discovery RPC.
func RegisterAnthropicModelHandlers(s *Server) {
	// providers.anthropic_models — when the user has overridden the Anthropic
	// base URL, probe it for its live model list; otherwise report
	// "overridden: false" so the UI keeps showing the static catalogue.
	s.RegisterHandler("providers.anthropic_models", func(data map[string]any) (map[string]any, error) {
		cred, ok := anthropicOverride()
		if !ok {
			return map[string]any{"status": "ok", "overridden": false}, nil
		}
		return map[string]any{
			"status":     "ok",
			"overridden": true,
			"state":      probeAnthropicModels(context.Background(), cred),
		}, nil
	})
}

// --- Client side (connection-level RPC) ---

// AnthropicModelStatus asks the daemon whether the Anthropic base URL is
// overridden and, if so, returns the live probe result for it.
func (c *Client) AnthropicModelStatus() (overridden bool, state LocalProviderState, err error) {
	resp, err := c.sendRequest(map[string]any{"command": "providers.anthropic_models"})
	if err != nil {
		return false, LocalProviderState{}, err
	}
	if resp["status"] != "ok" {
		msg, _ := resp["message"].(string)
		if msg == "" {
			msg = "anthropic model status request failed"
		}
		return false, LocalProviderState{}, fmt.Errorf("%s", msg)
	}
	overridden, _ = resp["overridden"].(bool)
	if !overridden {
		return false, LocalProviderState{}, nil
	}
	raw, err := json.Marshal(resp["state"])
	if err != nil {
		return false, LocalProviderState{}, err
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return false, LocalProviderState{}, err
	}
	return true, state, nil
}
