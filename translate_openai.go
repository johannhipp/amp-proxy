package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// translateGoogleToOpenAI converts a Google GenAI request to OpenAI Chat Completions format
func translateGoogleToOpenAI(googleReq map[string]interface{}, model string, streaming bool) ([]byte, error) {
	openaiReq := map[string]interface{}{
		"model": model,
	}

	if streaming {
		openaiReq["stream"] = true
	}

	var messages []interface{}

	// System instruction -> system message
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
				messages = append(messages, map[string]interface{}{
					"role":    "system",
					"content": strings.Join(texts, "\n"),
				})
			}
		}
	}

	// Generation config
	if genConfig, ok := googleReq["generationConfig"].(map[string]interface{}); ok {
		if maxTokens, ok := genConfig["maxOutputTokens"].(float64); ok {
			openaiReq["max_tokens"] = int(maxTokens)
		}
		if temp, ok := genConfig["temperature"].(float64); ok {
			openaiReq["temperature"] = temp
		}
		if topP, ok := genConfig["topP"].(float64); ok {
			openaiReq["top_p"] = topP
		}
		if stopSeqs, ok := genConfig["stopSequences"].([]interface{}); ok {
			openaiReq["stop"] = stopSeqs
		}
	}

	// Contents -> messages
	if contents, ok := googleReq["contents"].([]interface{}); ok {
		contentMsgs, err := translateGoogleContentsToOpenAIMessages(contents)
		if err != nil {
			return nil, err
		}
		messages = append(messages, contentMsgs...)
	}

	openaiReq["messages"] = messages

	// Tools
	if tools, ok := googleReq["tools"].([]interface{}); ok {
		openaiTools := translateGoogleToolsToOpenAI(tools)
		if len(openaiTools) > 0 {
			openaiReq["tools"] = openaiTools
		}
	}

	// Tool config -> tool_choice
	if toolConfig, ok := googleReq["toolConfig"].(map[string]interface{}); ok {
		if fcc, ok := toolConfig["functionCallingConfig"].(map[string]interface{}); ok {
			if mode, ok := fcc["mode"].(string); ok {
				switch mode {
				case "AUTO":
					openaiReq["tool_choice"] = "auto"
				case "ANY":
					openaiReq["tool_choice"] = "required"
				case "NONE":
					openaiReq["tool_choice"] = "none"
				}
			}
		}
	}

	return json.Marshal(openaiReq)
}

func translateGoogleContentsToOpenAIMessages(contents []interface{}) ([]interface{}, error) {
	var messages []interface{}
	toolCallCounter := 0

	for _, c := range contents {
		content, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		role, _ := content["role"].(string)
		parts, _ := content["parts"].([]interface{})

		// Map role
		openaiRole := role
		if role == "model" {
			openaiRole = "assistant"
		}

		// Check what kinds of parts we have
		var textParts []string
		var toolCalls []interface{}
		var toolResults []interface{}

		for _, p := range parts {
			part, ok := p.(map[string]interface{})
			if !ok {
				continue
			}

			if text, ok := part["text"].(string); ok {
				textParts = append(textParts, text)
			} else if fc, ok := part["functionCall"].(map[string]interface{}); ok {
				toolCallCounter++
				argsJSON, _ := json.Marshal(fc["args"])
				toolCalls = append(toolCalls, map[string]interface{}{
					"id":   fmt.Sprintf("call_%06d", toolCallCounter),
					"type": "function",
					"function": map[string]interface{}{
						"name":      fc["name"],
						"arguments": string(argsJSON),
					},
				})
			} else if fr, ok := part["functionResponse"].(map[string]interface{}); ok {
				responseJSON, _ := json.Marshal(fr["response"])
				name, _ := fr["name"].(string)
				toolResults = append(toolResults, map[string]interface{}{
					"role":         "tool",
					"tool_call_id": findOpenAIToolCallID(messages, name),
					"content":      string(responseJSON),
				})
			}
		}

		// Emit messages based on content types
		if len(toolCalls) > 0 {
			msg := map[string]interface{}{
				"role":       "assistant",
				"tool_calls": toolCalls,
			}
			if len(textParts) > 0 {
				msg["content"] = strings.Join(textParts, "\n")
			}
			messages = append(messages, msg)
		} else if len(toolResults) > 0 {
			for _, tr := range toolResults {
				messages = append(messages, tr)
			}
		} else if len(textParts) > 0 {
			messages = append(messages, map[string]interface{}{
				"role":    openaiRole,
				"content": strings.Join(textParts, "\n"),
			})
		}
	}

	return messages, nil
}

func findOpenAIToolCallID(messages []interface{}, name string) string {
	for i := len(messages) - 1; i >= 0; i-- {
		msg, ok := messages[i].(map[string]interface{})
		if !ok {
			continue
		}
		toolCalls, ok := msg["tool_calls"].([]interface{})
		if !ok {
			continue
		}
		for _, tc := range toolCalls {
			call, ok := tc.(map[string]interface{})
			if !ok {
				continue
			}
			fn, ok := call["function"].(map[string]interface{})
			if !ok {
				continue
			}
			if fn["name"] == name {
				if id, ok := call["id"].(string); ok {
					return id
				}
			}
		}
	}
	return "call_unknown"
}

func translateGoogleToolsToOpenAI(tools []interface{}) []interface{} {
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
			fn := map[string]interface{}{
				"name": funcDecl["name"],
			}
			if desc, ok := funcDecl["description"].(string); ok {
				fn["description"] = desc
			}
			if params, ok := funcDecl["parameters"]; ok {
				fn["parameters"] = lowercaseSchemaTypes(params)
			}
			result = append(result, map[string]interface{}{
				"type":     "function",
				"function": fn,
			})
		}
	}
	return result
}

// translateOpenAIToGoogle converts an OpenAI Chat Completions response to Google GenAI format
func translateOpenAIToGoogle(resp map[string]interface{}) ([]byte, error) {
	choices, ok := resp["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		return nil, fmt.Errorf("no choices in OpenAI response")
	}

	choice, ok := choices[0].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid choice format")
	}

	message, ok := choice["message"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid message format")
	}

	var parts []interface{}

	if content, ok := message["content"].(string); ok && content != "" {
		parts = append(parts, map[string]interface{}{"text": content})
	}

	if toolCalls, ok := message["tool_calls"].([]interface{}); ok {
		for _, tc := range toolCalls {
			call, ok := tc.(map[string]interface{})
			if !ok {
				continue
			}
			fn, ok := call["function"].(map[string]interface{})
			if !ok {
				continue
			}
			var args interface{}
			argsStr, _ := fn["arguments"].(string)
			if err := json.Unmarshal([]byte(argsStr), &args); err != nil {
				args = map[string]interface{}{}
			}
			parts = append(parts, map[string]interface{}{
				"functionCall": map[string]interface{}{
					"name": fn["name"],
					"args": args,
				},
			})
		}
	}

	finishReason := "STOP"
	if fr, ok := choice["finish_reason"].(string); ok {
		switch fr {
		case "length":
			finishReason = "MAX_TOKENS"
		case "tool_calls":
			finishReason = "STOP"
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
		promptTokens, _ := usage["prompt_tokens"].(float64)
		completionTokens, _ := usage["completion_tokens"].(float64)
		googleResp["usageMetadata"] = map[string]interface{}{
			"promptTokenCount":     int(promptTokens),
			"candidatesTokenCount": int(completionTokens),
			"totalTokenCount":      int(promptTokens + completionTokens),
		}
	}

	return json.Marshal(googleResp)
}

// translateOpenAIStreamChunk translates a single OpenAI SSE chunk to Google format
func translateOpenAIStreamChunk(chunk map[string]interface{}) ([]byte, error) {
	choices, ok := chunk["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		return nil, nil
	}

	choice, ok := choices[0].(map[string]interface{})
	if !ok {
		return nil, nil
	}

	delta, ok := choice["delta"].(map[string]interface{})
	if !ok {
		return nil, nil
	}

	var parts []interface{}

	if content, ok := delta["content"].(string); ok && content != "" {
		parts = append(parts, map[string]interface{}{"text": content})
	}

	if toolCalls, ok := delta["tool_calls"].([]interface{}); ok {
		for _, tc := range toolCalls {
			call, ok := tc.(map[string]interface{})
			if !ok {
				continue
			}
			fn, ok := call["function"].(map[string]interface{})
			if !ok {
				continue
			}
			if name, ok := fn["name"].(string); ok {
				parts = append(parts, map[string]interface{}{
					"functionCall": map[string]interface{}{
						"name": name,
						"args": map[string]interface{}{},
					},
				})
			}
		}
	}

	if len(parts) == 0 {
		// Check for finish_reason
		if fr, ok := choice["finish_reason"].(string); ok && fr != "" {
			finishReason := "STOP"
			if fr == "length" {
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
				promptTokens, _ := usage["prompt_tokens"].(float64)
				completionTokens, _ := usage["completion_tokens"].(float64)
				result["usageMetadata"] = map[string]interface{}{
					"promptTokenCount":     int(promptTokens),
					"candidatesTokenCount": int(completionTokens),
					"totalTokenCount":      int(promptTokens + completionTokens),
				}
			}
			return json.Marshal(result)
		}
		return nil, nil
	}

	return json.Marshal(map[string]interface{}{
		"candidates": []interface{}{
			map[string]interface{}{
				"content": map[string]interface{}{
					"role":  "model",
					"parts": parts,
				},
			},
		},
	})
}
