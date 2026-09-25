package nvidia

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// This file talks to NVIDIA's OpenAI-compatible chat completions API
// (build.nvidia.com) to generate a custom HTML/CSS/JS rendering ("design")
// for a CRM+ entity — see handlers_crm.go's design endpoints. The API key
// never reaches the browser: every call is made server-side.
const invokeURL = "https://integrate.api.nvidia.com/v1/chat/completions"

var ErrNotConfigured = errors.New("NVIDIA_API_KEY n'est pas configurée")

func apiKey() string { return os.Getenv("NVIDIA_API_KEY") }

func Model() string {
	if m := os.Getenv("NVIDIA_MODEL"); m != "" {
		return m
	}
	return "nvidia/nemotron-3-super-120b-a12b"
}

func VisionModel() string {
	if m := os.Getenv("NVIDIA_VISION_MODEL"); m != "" {
		return m
	}
	return "meta/llama-3.2-11b-vision-instruct"
}

// ContentPart is either plain text or an image (data URL), matching
// the OpenAI-compatible multimodal message format NVIDIA's API expects.
type ContentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

type ImageURL struct {
	URL string `json:"url"`
}

type Message struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // string, or []ContentPart when an image is attached
}

type chatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	MaxTokens   int       `json:"max_tokens"`
	Temperature float64   `json:"temperature"`
	Stream      bool      `json:"stream"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

var httpClient = &http.Client{Timeout: 90 * time.Second}

// CallChat sends a single (non-streaming) chat completion request and
// returns the model's reply text.
func CallChat(ctx context.Context, model string, messages []Message, maxTokens int) (string, error) {
	apiKey := apiKey()
	if apiKey == "" {
		return "", ErrNotConfigured
	}

	reqBody, err := json.Marshal(chatRequest{
		Model:       model,
		Messages:    messages,
		MaxTokens:   maxTokens,
		Temperature: 0.4,
		Stream:      false,
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, invokeURL, bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}

	var parsed chatResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("réponse NVIDIA illisible (statut %d)", resp.StatusCode)
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return "", fmt.Errorf("erreur NVIDIA: %s", parsed.Error.Message)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("erreur NVIDIA (statut %d)", resp.StatusCode)
	}
	if len(parsed.Choices) == 0 {
		return "", errors.New("réponse NVIDIA vide")
	}
	return parsed.Choices[0].Message.Content, nil
}

// StripCodeFence removes a leading/trailing ``` (optionally ```html)
// markdown code fence some models wrap their output in, despite being
// told to return raw HTML only.
func StripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	lines := strings.SplitN(s, "\n", 2)
	if len(lines) < 2 {
		return s
	}
	s = lines[1]
	s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	return strings.TrimSpace(s)
}
