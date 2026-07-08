package daemon

import (
	"strings"

	"github.com/get-vix/vix/internal/daemon/llm"
)

const maxDeferredToolNudges = 2

func shouldNudgeDeferredToolUse(msg *llm.Message, tools []llm.ToolParam) bool {
	if len(tools) == 0 || msg == nil {
		return false
	}
	if msg.StopReason != llm.StopEndTurn || len(msg.ToolCalls) > 0 {
		return false
	}

	text := strings.ToLower(strings.TrimSpace(extractTextFromMessage(msg)))
	text = strings.ReplaceAll(text, "\u2019", "'")
	text = strings.ReplaceAll(text, "\u2018", "'")
	if text == "" {
		return false
	}

	if !containsAny(text, []string{
		"i'll ",
		"i will ",
		"i'm going to ",
		"i am going to ",
		"let me ",
		"i need to ",
	}) {
		return false
	}

	return containsAny(text, []string{
		"inspect",
		"check",
		"look",
		"read",
		"open",
		"search",
		"grep",
		"list",
		"find",
		"examine",
		"explore",
		"scan",
		"modify",
		"edit",
		"update",
		"create",
		"write",
		"run",
		"test",
		"import",
		"copy",
		"compare",
		"fetch",
		"load",
		"review",
		"investigate",
		"patch",
		"fix",
		"change",
		"apply",
		"install",
	})
}

func deferredToolUseNudge() llm.MessageParam {
	return llm.NewUserMessage(llm.NewTextBlock("You ended your turn after saying you would take an action, but you did not emit any tool call. Continue now by calling the appropriate tool(s). Do not restate the plan unless you are blocked."))
}

func containsAny(s string, needles []string) bool {
	for _, needle := range needles {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}
