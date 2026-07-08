package daemon

import (
	"testing"

	"github.com/get-vix/vix/internal/daemon/llm"
)

func TestShouldNudgeDeferredToolUse(t *testing.T) {
	tools := []llm.ToolParam{{Name: "bash"}}

	cases := []struct {
		name  string
		msg   *llm.Message
		tools []llm.ToolParam
		want  bool
	}{
		{
			name: "promised action with available tools",
			msg: &llm.Message{
				StopReason:  llm.StopEndTurn,
				TextContent: "I'll inspect the repo and update the file.",
			},
			tools: tools,
			want:  true,
		},
		{
			name: "ordinary answer",
			msg: &llm.Message{
				StopReason:  llm.StopEndTurn,
				TextContent: "The sky is blue because shorter wavelengths scatter more.",
			},
			tools: tools,
			want:  false,
		},
		{
			name: "no tools available",
			msg: &llm.Message{
				StopReason:  llm.StopEndTurn,
				TextContent: "I'll inspect the repo.",
			},
			want: false,
		},
		{
			name: "already emitted a tool call",
			msg: &llm.Message{
				StopReason:  llm.StopEndTurn,
				TextContent: "I'll inspect the repo.",
				ToolCalls: []llm.ToolCall{{
					ID:   "call_1",
					Name: "bash",
				}},
			},
			tools: tools,
			want:  false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldNudgeDeferredToolUse(tc.msg, tc.tools); got != tc.want {
				t.Fatalf("shouldNudgeDeferredToolUse() = %v, want %v", got, tc.want)
			}
		})
	}
}
