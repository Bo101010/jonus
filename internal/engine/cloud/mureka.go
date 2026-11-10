package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// murekaDefaultModel is Mureka's current model.
const murekaDefaultModel = "mureka-music-v9"

// The gateway routes Mureka by what the caller supplied, rather than by a mode
// flag: words to sing, only a subject to write about, or a request for no
// vocal at all.
const (
	murekaLyricsPath       = "/v1/wand/mureka-music/generation"
	murekaPromptPath       = "/v1/wand/mureka-music/easy-generation"
	murekaInstrumentalPath = "/v1/wand/mureka-music/instrumental-generation"
)

// murekaStatusSucceeded is the only status that carries finished audio.
const murekaStatusSucceeded = "succeeded"

// mureka speaks Mureka's dialect of the gateway API.
//
// Unlike MiniMax it cannot write lyrics, so a request with no words has to go
// to a different route — one that asks for a song about a subject instead of a
// song with given words.
type mureka struct{}

func (mureka) name() string { return ProviderMureka }

// murekaLyricsRequest asks for a song sung to supplied words.
type murekaLyricsRequest struct {
	Model  string `json:"model"`
	Lyrics string `json:"lyrics"`
	Prompt string `json:"prompt,omitempty"`
	Count  int    `json:"n"`
}

// murekaPromptRequest asks for a song about a subject, words included.
type murekaPromptRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Count  int    `json:"n"`
}

// murekaInstrumentalRequest asks for a track with no vocal.
type murekaInstrumentalRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

// murekaResponse is the part of Mureka's answer this adapter reads.
type murekaResponse struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Reason  string `json:"failed_reason"`
	Choices []struct {
		URL string `json:"url"`
		// DurationMS is the song length in milliseconds.
		DurationMS int64 `json:"duration"`
	} `json:"choices"`
}

func (mureka) request(ctx context.Context, sub submission) (*http.Request, error) {
	spec := sub.request.Spec

	prompt := strings.TrimSpace(spec.Prompt)
	lyrics := strings.TrimSpace(spec.Lyrics)

	var (
		route   string
		payload any
	)

	switch {
	case spec.Instrumental:
		if prompt == "" {
			return nil, errors.New("cloud engine: an instrumental request needs a prompt describing the music")
		}
		route = murekaInstrumentalPath
		payload = murekaInstrumentalRequest{Model: sub.model, Prompt: prompt}

	case lyrics == "":
		if prompt == "" {
			return nil, errors.New("cloud engine: mureka cannot write lyrics; supply lyrics or a prompt")
		}
		route = murekaPromptPath
		payload = murekaPromptRequest{Model: sub.model, Prompt: prompt, Count: 1}

	default:
		// The prompt is a style hint on this route, not the subject.
		route = murekaLyricsPath
		payload = murekaLyricsRequest{Model: sub.model, Lyrics: lyrics, Prompt: prompt, Count: 1}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("cloud engine: encode request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		sub.baseURL+route, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("cloud engine: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+sub.apiKey)
	return req, nil
}

func (mureka) decode(body []byte) (generated, error) {
	var resp murekaResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return generated{}, fmt.Errorf("cloud engine: decode mureka response: %w (body: %s)",
			err, snippet(body))
	}

	if resp.Status != murekaStatusSucceeded {
		reason := strings.TrimSpace(resp.Reason)
		if reason == "" {
			reason = "no reason given"
		}
		return generated{}, fmt.Errorf(
			"cloud engine: mureka returned %q: %s (this route is documented as synchronous)",
			resp.Status, reason)
	}
	if len(resp.Choices) == 0 || resp.Choices[0].URL == "" {
		return generated{}, errors.New("cloud engine: mureka returned no audio")
	}

	return generated{
		url:      resp.Choices[0].URL,
		taskID:   resp.ID,
		duration: time.Duration(resp.Choices[0].DurationMS) * time.Millisecond,
	}, nil
}

var _ provider = mureka{}
