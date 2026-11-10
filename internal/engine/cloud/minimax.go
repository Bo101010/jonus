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

// miniMaxDefaultModel is MiniMax's current text-to-music model.
const miniMaxDefaultModel = "minimax-music-v3.0"

// miniMaxPath is the gateway route for MiniMax music generation.
const miniMaxPath = "/v1/wand/minimax-music/generation"

// miniMaxStatusComplete is the only status that carries finished audio.
const miniMaxStatusComplete = 2

// minimax speaks MiniMax Music's dialect of the gateway API.
//
// Its useful property is lyrics_optimizer: handed only a description, the model
// writes words and sings them. That is what makes "type a line, get a song"
// work, and it is the reason this is the default provider — Mureka needs the
// words supplied.
type minimax struct{}

func (minimax) name() string { return ProviderMiniMax }

// miniMaxRequest is the body MiniMax expects.
type miniMaxRequest struct {
	Model           string           `json:"model"`
	Prompt          string           `json:"prompt,omitempty"`
	Lyrics          string           `json:"lyrics,omitempty"`
	LyricsOptimizer bool             `json:"lyrics_optimizer,omitempty"`
	Instrumental    bool             `json:"is_instrumental,omitempty"`
	OutputFormat    string           `json:"output_format"`
	AudioSetting    *miniMaxSettings `json:"audio_setting,omitempty"`
	Watermark       bool             `json:"aigc_watermark"`
}

type miniMaxSettings struct {
	SampleRate int    `json:"sample_rate"`
	Bitrate    int    `json:"bitrate"`
	Format     string `json:"format"`
}

// miniMaxResponse is the part of MiniMax's answer this adapter reads.
type miniMaxResponse struct {
	Data struct {
		Audio  string `json:"audio"`
		Status int    `json:"status"`
	} `json:"data"`
	ExtraInfo struct {
		DurationMS int64 `json:"music_duration"`
		SampleRate int   `json:"music_sample_rate"`
	} `json:"extra_info"`
	BaseResp struct {
		StatusCode int    `json:"status_code"`
		StatusMsg  string `json:"status_msg"`
	} `json:"base_resp"`
	TraceID string `json:"trace_id"`
}

func (minimax) request(ctx context.Context, sub submission) (*http.Request, error) {
	spec := sub.request.Spec

	prompt := strings.TrimSpace(spec.Prompt)
	lyrics := strings.TrimSpace(spec.Lyrics)

	if spec.Instrumental && prompt == "" {
		return nil, errors.New("cloud engine: an instrumental request needs a prompt describing the music")
	}
	if !spec.Instrumental && prompt == "" && lyrics == "" {
		return nil, errors.New("cloud engine: provide a prompt or lyrics")
	}

	payload := miniMaxRequest{
		Model:        sub.model,
		Prompt:       prompt,
		Lyrics:       lyrics,
		Instrumental: spec.Instrumental,
		// Ask for a link rather than inline hex: the response stays small, and
		// the download is a separate step that can report its own failure.
		OutputFormat: "url",
		AudioSetting: &miniMaxSettings{SampleRate: 44100, Bitrate: 256000, Format: "mp3"},
	}

	if !spec.Instrumental && lyrics == "" {
		// No words were supplied, so ask the model to write them. Without this
		// the request is rejected: lyrics are mandatory for a sung song.
		payload.LyricsOptimizer = true
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("cloud engine: encode request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		sub.baseURL+miniMaxPath, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("cloud engine: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+sub.apiKey)
	return req, nil
}

func (minimax) decode(body []byte) (generated, error) {
	var resp miniMaxResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return generated{}, fmt.Errorf("cloud engine: decode minimax response: %w (body: %s)",
			err, snippet(body))
	}

	if resp.BaseResp.StatusCode != 0 {
		return generated{}, fmt.Errorf("cloud engine: minimax refused the request (%d): %s",
			resp.BaseResp.StatusCode, resp.BaseResp.StatusMsg)
	}
	// The gateway documents this route as synchronous, so a song still being
	// generated means the answer is not the one the contract promised.
	if resp.Data.Status != miniMaxStatusComplete {
		return generated{}, fmt.Errorf(
			"cloud engine: minimax returned an unfinished song (status %d); this route is documented as synchronous",
			resp.Data.Status)
	}
	if resp.Data.Audio == "" {
		return generated{}, errors.New("cloud engine: minimax returned no audio")
	}

	return generated{
		url:        resp.Data.Audio,
		taskID:     resp.TraceID,
		duration:   time.Duration(resp.ExtraInfo.DurationMS) * time.Millisecond,
		sampleRate: resp.ExtraInfo.SampleRate,
	}, nil
}

var _ provider = minimax{}
