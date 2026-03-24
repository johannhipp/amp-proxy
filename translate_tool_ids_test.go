package main

import (
	"encoding/json"
	"testing"
)

func TestTranslateGoogleToAnthropic_RepeatedToolNameGetsDistinctResults(t *testing.T) {
	googleReq := map[string]interface{}{
		"contents": []interface{}{
			map[string]interface{}{
				"role": "model",
				"parts": []interface{}{
					map[string]interface{}{"functionCall": map[string]interface{}{"name": "shell_command", "args": map[string]interface{}{"command": "echo one"}}},
					map[string]interface{}{"functionCall": map[string]interface{}{"name": "shell_command", "args": map[string]interface{}{"command": "echo two"}}},
				},
			},
			map[string]interface{}{
				"role": "user",
				"parts": []interface{}{
					map[string]interface{}{"functionResponse": map[string]interface{}{"name": "shell_command", "response": map[string]interface{}{"ok": true, "n": 1}}},
					map[string]interface{}{"functionResponse": map[string]interface{}{"name": "shell_command", "response": map[string]interface{}{"ok": true, "n": 2}}},
				},
			},
		},
	}

	body, err := translateGoogleToAnthropic(googleReq, "claude-sonnet-4-6", false)
	if err != nil {
		t.Fatalf("translateGoogleToAnthropic failed: %v", err)
	}

	var translated map[string]interface{}
	if err := json.Unmarshal(body, &translated); err != nil {
		t.Fatalf("failed to unmarshal translated body: %v", err)
	}

	messages := mustMessages(t, translated)
	toolUseIDs := anthropicToolUseIDs(t, messages[0])
	toolResultIDs := anthropicToolResultIDs(t, messages[1])

	if len(toolUseIDs) != 2 || len(toolResultIDs) != 2 {
		t.Fatalf("expected two tool uses and two tool results, got uses=%d results=%d", len(toolUseIDs), len(toolResultIDs))
	}
	if toolResultIDs[0] != toolUseIDs[0] || toolResultIDs[1] != toolUseIDs[1] {
		t.Fatalf("tool_result IDs should follow tool call order, got uses=%v results=%v", toolUseIDs, toolResultIDs)
	}
	if toolResultIDs[0] == toolResultIDs[1] {
		t.Fatalf("tool_result IDs must be distinct, got %v", toolResultIDs)
	}
}

func TestTranslateGoogleToOpenAI_RepeatedToolNameGetsDistinctResults(t *testing.T) {
	googleReq := map[string]interface{}{
		"contents": []interface{}{
			map[string]interface{}{
				"role": "model",
				"parts": []interface{}{
					map[string]interface{}{"functionCall": map[string]interface{}{"name": "shell_command", "args": map[string]interface{}{"command": "echo one"}}},
					map[string]interface{}{"functionCall": map[string]interface{}{"name": "shell_command", "args": map[string]interface{}{"command": "echo two"}}},
				},
			},
			map[string]interface{}{
				"role": "user",
				"parts": []interface{}{
					map[string]interface{}{"functionResponse": map[string]interface{}{"name": "shell_command", "response": map[string]interface{}{"ok": true, "n": 1}}},
					map[string]interface{}{"functionResponse": map[string]interface{}{"name": "shell_command", "response": map[string]interface{}{"ok": true, "n": 2}}},
				},
			},
		},
	}

	body, err := translateGoogleToOpenAI(googleReq, "gpt-5.4", false)
	if err != nil {
		t.Fatalf("translateGoogleToOpenAI failed: %v", err)
	}

	var translated map[string]interface{}
	if err := json.Unmarshal(body, &translated); err != nil {
		t.Fatalf("failed to unmarshal translated body: %v", err)
	}

	messages := mustMessages(t, translated)
	toolCallIDs := openAIToolCallIDs(t, messages[0])
	toolResultIDs := openAIToolResultIDs(t, messages[1:])

	if len(toolCallIDs) != 2 || len(toolResultIDs) != 2 {
		t.Fatalf("expected two tool calls and two tool results, got calls=%d results=%d", len(toolCallIDs), len(toolResultIDs))
	}
	if toolResultIDs[0] != toolCallIDs[0] || toolResultIDs[1] != toolCallIDs[1] {
		t.Fatalf("tool results should follow tool call order, got calls=%v results=%v", toolCallIDs, toolResultIDs)
	}
	if toolResultIDs[0] == toolResultIDs[1] {
		t.Fatalf("tool result IDs must be distinct, got %v", toolResultIDs)
	}
}

func TestTranslateGoogleToAnthropic_ExplicitIDsOverrideQueueOrder(t *testing.T) {
	googleReq := map[string]interface{}{
		"contents": []interface{}{
			map[string]interface{}{
				"role": "model",
				"parts": []interface{}{
					map[string]interface{}{"functionCall": map[string]interface{}{"id": "call-a", "name": "shell_command", "args": map[string]interface{}{"command": "echo one"}}},
					map[string]interface{}{"functionCall": map[string]interface{}{"id": "call-b", "name": "shell_command", "args": map[string]interface{}{"command": "echo two"}}},
				},
			},
			map[string]interface{}{
				"role": "user",
				"parts": []interface{}{
					map[string]interface{}{"functionResponse": map[string]interface{}{"id": "call-b", "name": "shell_command", "response": map[string]interface{}{"ok": true, "n": 2}}},
					map[string]interface{}{"functionResponse": map[string]interface{}{"id": "call-a", "name": "shell_command", "response": map[string]interface{}{"ok": true, "n": 1}}},
				},
			},
		},
	}

	body, err := translateGoogleToAnthropic(googleReq, "claude-sonnet-4-6", false)
	if err != nil {
		t.Fatalf("translateGoogleToAnthropic failed: %v", err)
	}

	var translated map[string]interface{}
	if err := json.Unmarshal(body, &translated); err != nil {
		t.Fatalf("failed to unmarshal translated body: %v", err)
	}

	messages := mustMessages(t, translated)
	toolUseIDs := anthropicToolUseIDs(t, messages[0])
	toolResultIDs := anthropicToolResultIDs(t, messages[1])

	if len(toolUseIDs) != 2 || len(toolResultIDs) != 2 {
		t.Fatalf("expected two tool uses and two tool results, got uses=%d results=%d", len(toolUseIDs), len(toolResultIDs))
	}
	if toolResultIDs[0] != toolUseIDs[1] || toolResultIDs[1] != toolUseIDs[0] {
		t.Fatalf("explicit IDs should allow out-of-order mapping, got uses=%v results=%v", toolUseIDs, toolResultIDs)
	}
}

func TestValidateTranslatedToolResults_CatchesDuplicateToolResultIDs(t *testing.T) {
	body := []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_000001","name":"shell_command","input":{}},{"type":"tool_use","id":"toolu_000002","name":"shell_command","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_000001","content":"{}"},{"type":"tool_result","tool_use_id":"toolu_000001","content":"{}"}]}]}`)

	err := validateTranslatedToolResults("anthropic", body)
	if err == nil {
		t.Fatal("expected duplicate tool_result validation error, got nil")
	}
}

func mustMessages(t *testing.T, translated map[string]interface{}) []interface{} {
	t.Helper()
	messages, ok := translated["messages"].([]interface{})
	if !ok {
		t.Fatalf("translated request missing messages: %v", translated)
	}
	return messages
}

func anthropicToolUseIDs(t *testing.T, rawMsg interface{}) []string {
	t.Helper()
	msg, ok := rawMsg.(map[string]interface{})
	if !ok {
		t.Fatalf("invalid message type: %T", rawMsg)
	}
	content, ok := msg["content"].([]interface{})
	if !ok {
		t.Fatalf("message missing content blocks: %v", msg)
	}

	var ids []string
	for _, rawBlock := range content {
		block, ok := rawBlock.(map[string]interface{})
		if !ok {
			continue
		}
		if blockType, _ := block["type"].(string); blockType != "tool_use" {
			continue
		}
		if id, _ := block["id"].(string); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func anthropicToolResultIDs(t *testing.T, rawMsg interface{}) []string {
	t.Helper()
	msg, ok := rawMsg.(map[string]interface{})
	if !ok {
		t.Fatalf("invalid message type: %T", rawMsg)
	}
	content, ok := msg["content"].([]interface{})
	if !ok {
		t.Fatalf("message missing content blocks: %v", msg)
	}

	var ids []string
	for _, rawBlock := range content {
		block, ok := rawBlock.(map[string]interface{})
		if !ok {
			continue
		}
		if blockType, _ := block["type"].(string); blockType != "tool_result" {
			continue
		}
		if id, _ := block["tool_use_id"].(string); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func openAIToolCallIDs(t *testing.T, rawMsg interface{}) []string {
	t.Helper()
	msg, ok := rawMsg.(map[string]interface{})
	if !ok {
		t.Fatalf("invalid message type: %T", rawMsg)
	}
	toolCalls, ok := msg["tool_calls"].([]interface{})
	if !ok {
		t.Fatalf("assistant message missing tool_calls: %v", msg)
	}

	ids := make([]string, 0, len(toolCalls))
	for _, rawCall := range toolCalls {
		call, ok := rawCall.(map[string]interface{})
		if !ok {
			continue
		}
		if id, _ := call["id"].(string); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func openAIToolResultIDs(t *testing.T, messages []interface{}) []string {
	t.Helper()

	var ids []string
	for _, rawMsg := range messages {
		msg, ok := rawMsg.(map[string]interface{})
		if !ok {
			continue
		}
		if role, _ := msg["role"].(string); role != "tool" {
			continue
		}
		if id, _ := msg["tool_call_id"].(string); id != "" {
			ids = append(ids, id)
		}
	}

	return ids
}
