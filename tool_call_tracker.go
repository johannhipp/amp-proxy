package main

// toolCallTracker keeps one-to-one pairing between translated tool calls and tool results.
// It supports either explicit correlation IDs (when provided by upstream payloads) or
// fallback FIFO matching by tool name.
type toolCallTracker struct {
	byName             map[string][]string
	sourceToTranslated map[string]string
	translatedToSource map[string]string
}

func newToolCallTracker() *toolCallTracker {
	return &toolCallTracker{
		byName:             make(map[string][]string),
		sourceToTranslated: make(map[string]string),
		translatedToSource: make(map[string]string),
	}
}

func (t *toolCallTracker) record(name, translatedID, sourceID string) {
	if name != "" {
		t.byName[name] = append(t.byName[name], translatedID)
	}
	if sourceID != "" {
		t.sourceToTranslated[sourceID] = translatedID
		t.translatedToSource[translatedID] = sourceID
	}
}

func (t *toolCallTracker) consume(name, sourceID, unknownID string) string {
	if sourceID != "" {
		if translatedID, ok := t.sourceToTranslated[sourceID]; ok {
			t.removeFromQueue(name, translatedID)
			delete(t.sourceToTranslated, sourceID)
			delete(t.translatedToSource, translatedID)
			return translatedID
		}
	}

	if name != "" {
		queue := t.byName[name]
		if len(queue) > 0 {
			translatedID := queue[0]
			t.byName[name] = queue[1:]
			if sourceID, ok := t.translatedToSource[translatedID]; ok {
				delete(t.translatedToSource, translatedID)
				delete(t.sourceToTranslated, sourceID)
			}
			return translatedID
		}
	}

	return unknownID
}

func (t *toolCallTracker) removeFromQueue(name, translatedID string) {
	if name != "" {
		if t.removeFromNamedQueue(name, translatedID) {
			return
		}
	}

	for toolName := range t.byName {
		if t.removeFromNamedQueue(toolName, translatedID) {
			return
		}
	}
}

func (t *toolCallTracker) removeFromNamedQueue(name, translatedID string) bool {
	queue := t.byName[name]
	for i, id := range queue {
		if id == translatedID {
			t.byName[name] = append(queue[:i], queue[i+1:]...)
			return true
		}
	}
	return false
}

func extractToolCorrelationID(payload map[string]interface{}) string {
	for _, key := range []string{"id", "callId", "toolCallId", "tool_call_id", "toolUseId", "tool_use_id"} {
		if value, ok := payload[key].(string); ok && value != "" {
			return value
		}
	}
	return ""
}
