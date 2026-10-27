// Package model is one OpenAI-compatible chat client for every model
// (Meta Model API, Baseten, Modal, Ollama), configured by environment.
package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

// Error is a model or transport failure. Callers surface it as "pengu: ...".
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

// Message is one chat turn.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func baseURL() string {
	return strings.TrimSuffix(os.Getenv("MUSE_BASE_URL"), "/")
}

func apiKey() string {
	if k := os.Getenv("MUSE_API_KEY"); k != "" {
		return k
	}
	return os.Getenv("META_API")
}

func modelName(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if m := os.Getenv("MUSE_MODEL"); m != "" {
		return m
	}
	return "muse-spark-1.3"
}

// Chat sends messages and returns the assistant's text content.
func Chat(messages []Message, model string, timeout time.Duration) (string, error) {
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	base := baseURL()
	if base == "" {
		base = "https://api.meta.ai/v1"
	}
	body, err := json.Marshal(map[string]any{
		"model":    modelName(model),
		"messages": messages,
	})
	if err != nil {
		return "", &Error{err.Error()}
	}
	req, err := http.NewRequest("POST", base+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", &Error{err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey())
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return "", &Error{err.Error()}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return "", &Error{err.Error()}
	}
	if resp.StatusCode >= 400 {
		return "", &Error{fmt.Sprintf("HTTP %d: %.200s", resp.StatusCode, raw)}
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil || len(parsed.Choices) == 0 {
		return "", &Error{fmt.Sprintf("bad chat response: %.200s", raw)}
	}
	return parsed.Choices[0].Message.Content, nil
}

var jsonObj = regexp.MustCompile(`(?s)\{.*\}`)

// AskJSON asks for a JSON object; tolerates prose or code fences around it
// for endpoints that do not enforce a schema.
func AskJSON(system, user, model string) (any, error) {
	text, err := Chat([]Message{{Role: "system", Content: system}, {Role: "user", Content: user}}, model, 0)
	if err != nil {
		return nil, err
	}
	m := jsonObj.FindString(text)
	if m == "" {
		return nil, &Error{fmt.Sprintf("no JSON object in the answer: %.200s", text)}
	}
	var v any
	if err := json.Unmarshal([]byte(m), &v); err != nil {
		return nil, &Error{fmt.Sprintf("bad JSON in the answer: %.200s", text)}
	}
	return v, nil
}
