package ui

import (
	"testing"

	"github.com/get-vix/vix/internal/protocol"
)

func TestMarkCancelledReadyForInput(t *testing.T) {
	sess := &SessionState{
		agentState:   StateToolExecuting,
		input:        newInput(),
		focus:        FocusChat,
		pendingInput: &pendingMsg{text: "queued behind cancelled turn"},
	}
	sess.input.Blur()

	markCancelledReadyForInput(sess)

	if sess.agentState != StateWaitingForInput {
		t.Fatalf("agentState = %v, want %v", sess.agentState, StateWaitingForInput)
	}
	if sess.pendingInput != nil {
		t.Fatalf("pendingInput = %+v, want nil", sess.pendingInput)
	}
	if !sess.cancelAckPending {
		t.Fatal("cancelAckPending = false, want true")
	}
	if sess.focus != FocusEditor {
		t.Fatalf("focus = %v, want %v", sess.focus, FocusEditor)
	}
	if !sess.input.Focused() {
		t.Fatal("input should be focused after cancellation")
	}
}

func TestHandleEnterWhileBusyStillQueuesSteeringPrompt(t *testing.T) {
	s := NewStyles(true)
	m := Model{
		styles:     s,
		mdRenderer: NewMarkdownRenderer(80, true, s.CodeBoxBorderStyle),
	}
	sess := &SessionState{
		agentState: StateStreaming,
		input:      newInput(),
		history:    NewHistory(t.TempDir()),
	}
	sess.input.SetValue("actually inspect ~/.vix first")

	m.handleEnter(sess)

	if sess.agentState != StateStreaming {
		t.Fatalf("agentState = %v, want %v", sess.agentState, StateStreaming)
	}
	if sess.pendingInput == nil {
		t.Fatal("pendingInput = nil, want queued steering prompt")
	}
	if sess.pendingInput.text != "actually inspect ~/.vix first" {
		t.Fatalf("pendingInput.text = %q", sess.pendingInput.text)
	}
	if sess.input.Value() != "" {
		t.Fatalf("input.Value() = %q, want empty", sess.input.Value())
	}
}

func TestCancelledTurnDoneDoesNotIdleNewPrompt(t *testing.T) {
	s := NewStyles(true)
	sess := &SessionState{
		agentState:       StateStreaming,
		input:            newInput(),
		focus:            FocusEditor,
		cancelAckPending: true,
	}
	m := Model{
		activeTab:       TabKindChat,
		selectedSession: 0,
		sessions:        []*SessionState{sess},
		styles:          s,
		mdRenderer:      NewMarkdownRenderer(80, true, s.CodeBoxBorderStyle),
	}

	m.applyEventToSession(0, protocol.SessionEvent{Type: "event.agent_done"})

	if sess.cancelAckPending {
		t.Fatal("cancelAckPending = true, want consumed")
	}
	if sess.agentState != StateStreaming {
		t.Fatalf("agentState = %v, want %v", sess.agentState, StateStreaming)
	}
}
