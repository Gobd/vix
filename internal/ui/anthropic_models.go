package ui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/get-vix/vix/internal/daemon"
)

// anthropicModelsMsg carries the daemon's live probe of the Anthropic base
// URL: whether it's overridden from api.anthropic.com, and if so, the probe
// result (reachability + live model list).
type anthropicModelsMsg struct {
	overridden bool
	state      daemon.LocalProviderState
}

// fetchAnthropicModels asks the daemon whether the Anthropic base URL is
// overridden (e.g. pointed at a proxy via ANTHROPIC_BASE_URL) and, if so,
// probes it for its live model list. Triggered on entering the Models tab
// and when the provider cursor lands on anthropic — same points
// fetchLocalProviders is triggered from. A no-op RPC when not overridden.
func fetchAnthropicModels(socketPath, authToken string) tea.Cmd {
	return func() tea.Msg {
		client := daemon.NewClient(socketPath)
		client.SetAuthToken(authToken)
		overridden, state, err := client.AnthropicModelStatus()
		if err != nil {
			return anthropicModelsMsg{}
		}
		return anthropicModelsMsg{overridden: overridden, state: state}
	}
}
