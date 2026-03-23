package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// translateGoogleToAnthropic converts a Google GenAI request to Anthropic Messages API format
func translateGoogleToAnthropic(googleReq map[string]interface{}, model string, streaming bool) ([]byte, error) {
	anthropicReq := map[string]interface{}{
		"model":      model,
		"max_tokens": 8192,
	}

	if streaming {
		anthropicReq["stream"] = true
	}

	// System instruction -> system
	if sysInstr, ok := googleReq["systemInstruction"].(map[string]interface{}); ok {
		if parts, ok := sysInstr["parts"].([]interface{}); ok {
			var texts []string
			for _, p := range parts {
				if part, ok := p.(map[string]interface{}); ok {
					if text, ok := part["text"].(string); ok {
						texts = append(texts, text)
					}
				}
			}
			if len(texts) > 0 {
				system := ""
				for i, t := range texts {
					if i > 0 {
						system += "\n"
					}
					system += t
				}
				anthropicReq["system"] = system
			}
		}
	}

	// Generation config
	if genConfig, ok := googleReq["generationConfig"].(map[string]interface{}); ok {
		if maxTokens, ok := genConfig["maxOutputTokens"].(float64); ok {
			anthropicReq["max_tokens"] = int(maxTokens)
		}
		if temp, ok := genConfig["temperature"].(float64); ok {
			anthropicReq["temperature"] = temp
		}
		if topP, ok := genConfig["topP"].(float64); ok {
			anthropicReq["top_p"] = topP
		}
		if topK, ok := genConfig["topK"].(float64); ok {
			anthropicReq["top_k"] = int(topK)
		}
		if stopSeqs, ok := genConfig["stopSequences"].([]interface{}); ok {
			anthropicReq["stop_sequences"] = stopSeqs
		}
	}

	// Contents -> messages
	if contents, ok := googleReq["contents"].([]interface{}); ok {
		messages, err := translateGoogleContentsToAnthropicMessages(contents)
		if err != nil {
			return nil, err
		}
		anthropicReq["messages"] = messages
	}

	// Tools
	if tools, ok := googleReq["tools"].([]interface{}); ok {
		anthropicTools := translateGoogleToolsToAnthropic(tools)
		if len(anthropicTools) > 0 {
			anthropicReq["tools"] = anthropicTools
		}
	}

	// Tool config -> tool_choice
	if toolConfig, ok := googleReq["toolConfig"].(map[string]interface{}); ok {
		if fcc, ok := toolConfig["functionCallingConfig"].(map[string]interface{}); ok {
			if mode, ok := fcc["mode"].(string); ok {
				switch mode {
				case "AUTO":
					anthropicReq["tool_choice"] = map[string]string{"type": "auto"}
				case "ANY":
					anthropicReq["tool_choice"] = map[string]string{"type": "any"}
				case "NONE":
					// Don't send tools at all
					delete(anthropicReq, "tools")
				}
			}
		}
	}

	return json.Marshal(anthropicReq)
}

func translateGoogleContentsToAnthropicMessages(contents []interface{}) ([]interface{}, error) {
	var messages []interface{}
	toolCallCounter := 0

	for _, c := range contents {
		content, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		role, _ := content["role"].(string)
		parts, _ := content["parts"].([]interface{})

		// Map Google role to Anthropic role
		anthropicRole := role
		if role == "model" {
			anthropicRole = "assistant"
		}

		var contentBlocks []interface{}
		for _, p := range parts {
			part, ok := p.(map[string]interface{})
			if !ok {
				continue
			}

			if text, ok := part["text"].(string); ok {
				contentBlocks = append(contentBlocks, map[string]interface{}{
					"type": "text",
					"text": text,
				})
			} else if fc, ok := part["functionCall"].(map[string]interface{}); ok {
				toolCallCounter++
				contentBlocks = append(contentBlocks, map[string]interface{}{
					"type":  "tool_use",
					"id":    fmt.Sprintf("toolu_%06d", toolCallCounter),
					"name":  fc["name"],
					"input": fc["args"],
				})
			} else if fr, ok := part["functionResponse"].(map[string]interface{}); ok {
				// Function responses become tool_result blocks
				// Find the matching tool_use ID by name
				name, _ := fr["name"].(string)
				response := fr["response"]
				responseJSON, _ := json.Marshal(response)

				contentBlocks = append(contentBlocks, map[string]interface{}{
					"type":        "tool_result",
					"tool_use_id": findToolUseID(messages, name),
					"content":     string(responseJSON),
				})
			} else if inlineData, ok := part["inlineData"].(map[string]interface{}); ok {
				contentBlocks = append(contentBlocks, map[string]interface{}{
					"type": "image",
					"source": map[string]interface{}{
						"type":       "base64",
						"media_type": inlineData["mimeType"],
						"data":       inlineData["data"],
					},
				})
			}
		}

		if len(contentBlocks) > 0 {
			messages = append(messages, map[string]interface{}{
				"role":    anthropicRole,
				"content": contentBlocks,
			})
		}
	}

	return messages, nil
}

// findToolUseID walks previous messages to find the tool_use block with the given name
func findToolUseID(messages []interface{}, name string) string {
	// Walk backwards through messages to find matching tool_use
	for i := len(messages) - 1; i >= 0; i-- {
		msg, ok := messages[i].(map[string]interface{})
		if !ok {
			continue
		}
		content, ok := msg["content"].([]interface{})
		if !ok {
			continue
		}
		for _, block := range content {
			b, ok := block.(map[string]interface{})
			if !ok {
				continue
			}
			if b["type"] == "tool_use" && b["name"] == name {
				if id, ok := b["id"].(string); ok {
					return id
				}
			}
		}
	}
	return "toolu_unknown"
}

func translateGoogleToolsToAnthropic(tools []interface{}) []interface{} {
	var result []interface{}
	for _, t := range tools {
		tool, ok := t.(map[string]interface{})
		if !ok {
			continue
		}
		funcDecls, ok := tool["functionDeclarations"].([]interface{})
		if !ok {
			continue
		}
		for _, fd := range funcDecls {
			funcDecl, ok := fd.(map[string]interface{})
			if !ok {
				continue
			}
			anthropicTool := map[string]interface{}{
				"name": funcDecl["name"],
			}
			if desc, ok := funcDecl["description"].(string); ok {
				anthropicTool["description"] = desc
			}
			if params, ok := funcDecl["parameters"]; ok {
				// Google uses uppercase type names (OBJECT, STRING, etc.)
				// Anthropic requires lowercase (object, string, etc.)
				anthropicTool["input_schema"] = lowercaseSchemaTypes(params)
			} else {
				anthropicTool["input_schema"] = map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
			}
			result = append(result, anthropicTool)
		}
	}
	return result
}

// lowercaseSchemaTypes recursively lowercases all "type" values in a JSON Schema.
// Google GenAI uses uppercase ("OBJECT", "STRING", "ARRAY", "NUMBER", "BOOLEAN", "INTEGER")
// while Anthropic/OpenAI require lowercase ("object", "string", "array", "number", "boolean", "integer").
func lowercaseSchemaTypes(v interface{}) interface{} {
	switch val := v.(type) {
	case map[string]interface{}:
		result := make(map[string]interface{}, len(val))
		for k, v := range val {
			if k == "type" {
				if s, ok := v.(string); ok {
					result[k] = strings.ToLower(s)
				} else {
					result[k] = v
				}
			} else {
				result[k] = lowercaseSchemaTypes(v)
			}
		}
		return result
	case []interface{}:
		result := make([]interface{}, len(val))
		for i, item := range val {
			result[i] = lowercaseSchemaTypes(item)
		}
		return result
	default:
		return v
	}
}

// translateAnthropicToGoogle converts an Anthropic Messages API response to Google GenAI format
func translateAnthropicToGoogle(resp map[string]interface{}) ([]byte, error) {
	var parts []interface{}

	if content, ok := resp["content"].([]interface{}); ok {
		for _, c := range content {
			block, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			blockType, _ := block["type"].(string)
			switch blockType {
			case "text":
				parts = append(parts, map[string]interface{}{"text": block["text"]})
			case "tool_use":
				parts = append(parts, map[string]interface{}{
					"functionCall": map[string]interface{}{
						"name": block["name"],
						"args": block["input"],
					},
				})
			}
		}
	}

	finishReason := "STOP"
	if stopReason, ok := resp["stop_reason"].(string); ok {
		switch stopReason {
		case "max_tokens":
			finishReason = "MAX_TOKENS"
		default:
			finishReason = "STOP"
		}
	}

	googleResp := map[string]interface{}{
		"candidates": []interface{}{
			map[string]interface{}{
				"content": map[string]interface{}{
					"role":  "model",
					"parts": parts,
				},
				"finishReason": finishReason,
			},
		},
	}

	if usage, ok := resp["usage"].(map[string]interface{}); ok {
		inputTokens, _ := usage["input_tokens"].(float64)
		outputTokens, _ := usage["output_tokens"].(float64)
		googleResp["usageMetadata"] = map[string]interface{}{
			"promptTokenCount":     int(inputTokens),
			"candidatesTokenCount": int(outputTokens),
			"totalTokenCount":      int(inputTokens + outputTokens),
		}
	}

	return json.Marshal(googleResp)
}

// translateAnthropicStreamChunk translates a single Anthropic SSE event to Google format
func translateAnthropicStreamChunk(chunk map[string]interface{}, eventType string) ([]byte, error) {
	switch eventType {
	case "content_block_delta":
		delta, ok := chunk["delta"].(map[string]interface{})
		if !ok {
			return nil, nil
		}
		deltaType, _ := delta["type"].(string)
		switch deltaType {
		case "text_delta":
			text, _ := delta["text"].(string)
			return json.Marshal(map[string]interface{}{
				"candidates": []interface{}{
					map[string]interface{}{
						"content": map[string]interface{}{
							"role":  "model",
							"parts": []interface{}{map[string]interface{}{"text": text}},
						},
					},
				},
			})
		case "input_json_delta":
			// Tool call argument streaming — accumulate but don't emit until block_stop
			return nil, nil
		}

	case "content_block_start":
		block, ok := chunk["content_block"].(map[string]interface{})
		if !ok {
			return nil, nil
		}
		if block["type"] == "tool_use" {
			return json.Marshal(map[string]interface{}{
				"candidates": []interface{}{
					map[string]interface{}{
						"content": map[string]interface{}{
							"role": "model",
							"parts": []interface{}{
								map[string]interface{}{
									"functionCall": map[string]interface{}{
										"name": block["name"],
										"args": map[string]interface{}{},
									},
								},
							},
						},
					},
				},
			})
		}
		return nil, nil

	case "message_delta":
		delta, ok := chunk["delta"].(map[string]interface{})
		if !ok {
			return nil, nil
		}
		finishReason := "STOP"
		if sr, ok := delta["stop_reason"].(string); ok && sr == "max_tokens" {
			finishReason = "MAX_TOKENS"
		}

		result := map[string]interface{}{
			"candidates": []interface{}{
				map[string]interface{}{
					"content": map[string]interface{}{
						"role":  "model",
						"parts": []interface{}{},
					},
					"finishReason": finishReason,
				},
			},
		}

		if usage, ok := chunk["usage"].(map[string]interface{}); ok {
			outputTokens, _ := usage["output_tokens"].(float64)
			result["usageMetadata"] = map[string]interface{}{
				"candidatesTokenCount": int(outputTokens),
			}
		}
		return json.Marshal(result)

	case "message_stop":
		return nil, nil
	case "message_start":
		return nil, nil
	case "content_block_stop":
		return nil, nil
	case "ping":
		return nil, nil
	}

	return nil, nil
}
