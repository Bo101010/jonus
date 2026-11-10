package cloud

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Bo101010/jonus/internal/engine"
)

// songBytes stands in for a downloaded track. It is not real audio: nothing in
// this adapter inspects the bytes, so the tests only need to see them arrive
// intact.
var songBytes = []byte("ID3 pretend this is an mp3")

// vendor serves a stubbed generation API and the audio it points at, and records
// what was asked of it.
type vendor struct {
	*httptest.Server

	// response is the JSON body the generation route answers with, once
	// "%URL%" has been replaced by the address of this server.
	response string

	// path, body, and auth capture the last request the generation route
	// received.
	path string
	body map[string]any
	auth string
}

func newVendor(t *testing.T, response string) *vendor {
	t.Helper()

	v := &vendor{response: response}
	v.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/audio.mp3") {
			w.Header().Set("Content-Type", "audio/mpeg")
			w.Write(songBytes)
			return
		}

		v.path = r.URL.Path
		v.auth = r.Header.Get("Authorization")

		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading request body: %v", err)
			return
		}
		if err := json.Unmarshal(raw, &v.body); err != nil {
			t.Errorf("request body is not JSON: %v (%s)", err, raw)
			return
		}

		fmt.Fprint(w, strings.ReplaceAll(v.response, "%URL%", v.URL+"/audio.mp3"))
	}))
	t.Cleanup(v.Close)

	return v
}

// flag reads a boolean field that omitempty may have left out of the body. An
// absent flag and a false one mean the same thing to the vendor.
func (v *vendor) flag(name string) bool {
	got, _ := v.body[name].(bool)
	return got
}

const miniMaxOK = `{
	"data": {"audio": "%URL%", "status": 2},
	"extra_info": {"music_duration": 25364, "music_sample_rate": 44100},
	"base_resp": {"status_code": 0, "status_msg": "success"},
	"trace_id": "trace-abc"
}`

const murekaOK = `{
	"id": "mureka-task-1",
	"status": "succeeded",
	"choices": [{"url": "%URL%", "duration": 162630}]
}`

func TestNewRejectsAMissingKey(t *testing.T) {
	if _, err := New(Config{APIKey: "   "}); err == nil {
		t.Fatal("New accepted an empty API key; the failure would then only appear on the first job")
	}
}

func TestNewRejectsAnUnknownProvider(t *testing.T) {
	_, err := New(Config{Provider: "suno", APIKey: "k"})
	if err == nil {
		t.Fatal("New accepted an unknown provider")
	}
	if !strings.Contains(err.Error(), ProviderMiniMax) {
		t.Errorf("error should name the providers that exist, got %q", err)
	}
}

// TestGenerateWritesTheDownloadedSong covers the whole exchange: build a
// request, post it, read the link out of the answer, fetch the audio, and
// publish it where the library expects it.
func TestGenerateWritesTheDownloadedSong(t *testing.T) {
	v := newVendor(t, miniMaxOK)

	c, err := New(Config{APIKey: "test-key", BaseURL: v.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	dir := t.TempDir()
	var seen []engine.Progress

	result, err := c.Generate(context.Background(), engine.Request{
		Kind:   engine.KindText2Music,
		Spec:   engine.Spec{Prompt: "warm acoustic folk"},
		OutDir: dir,
	}, func(p engine.Progress) { seen = append(seen, p) })
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if len(result.Tracks) != 1 {
		t.Fatalf("got %d tracks, want 1", len(result.Tracks))
	}
	track := result.Tracks[0]

	got, err := os.ReadFile(track.Path)
	if err != nil {
		t.Fatalf("reading the published track: %v", err)
	}
	if string(got) != string(songBytes) {
		t.Errorf("track contents = %q, want %q", got, songBytes)
	}
	if track.Format != "mp3" {
		t.Errorf("format = %q, want mp3 (from the download URL)", track.Format)
	}
	if track.Duration != 25364*time.Millisecond {
		t.Errorf("duration = %v, want 25.364s from the vendor's extra_info", track.Duration)
	}
	if track.SampleRate != 44100 {
		t.Errorf("sampleRate = %d, want 44100", track.SampleRate)
	}
	if track.Seed != 0 {
		t.Errorf("seed = %d; the vendors take no seed, so reporting one would imply a reproducibility this engine lacks", track.Seed)
	}
	if result.TaskID != "trace-abc" {
		t.Errorf("taskID = %q, want the vendor's trace id", result.TaskID)
	}
	if v.auth != "Bearer test-key" {
		t.Errorf("Authorization = %q, want the configured key", v.auth)
	}

	// A leftover .part file would mean the publish step did not happen.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading the output directory: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(track.Path) {
		t.Errorf("output directory holds %v, want just the finished track", entries)
	}

	// The bar must advance and must not claim completion before the audio is
	// on disk.
	if len(seen) < 2 {
		t.Fatalf("got %d progress reports, want the phase changes to be visible", len(seen))
	}
	last := 0.0
	for _, p := range seen {
		if p.Fraction < last {
			t.Errorf("progress went backwards: %v after %v", p.Fraction, last)
		}
		last = p.Fraction
	}
	if seen[len(seen)-1].Phase != engine.PhaseDone || last != 1 {
		t.Errorf("last report = %+v, want a done phase at 1.0", seen[len(seen)-1])
	}
}

// TestGenerateAsksMiniMaxToWriteLyrics pins the behaviour the whole product
// rests on: a description with no words must come back as a sung song.
func TestGenerateAsksMiniMaxToWriteLyrics(t *testing.T) {
	tests := []struct {
		name             string
		spec             engine.Spec
		wantOptimizer    bool
		wantInstrumental bool
	}{
		{
			name:          "a prompt alone asks the model to write words",
			spec:          engine.Spec{Prompt: "a rainy night in the city"},
			wantOptimizer: true,
		},
		{
			name: "supplied lyrics are used as they are",
			spec: engine.Spec{Prompt: "folk", Lyrics: "[Verse]\nhold on"},
		},
		{
			name:             "an instrumental request asks for no vocal",
			spec:             engine.Spec{Prompt: "solo piano", Instrumental: true},
			wantInstrumental: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			v := newVendor(t, miniMaxOK)

			c, err := New(Config{APIKey: "test-key", BaseURL: v.URL})
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			if _, err := c.Generate(context.Background(), engine.Request{
				Kind:   engine.KindText2Music,
				Spec:   test.spec,
				OutDir: t.TempDir(),
			}, nil); err != nil {
				t.Fatalf("Generate: %v", err)
			}

			if v.path != miniMaxPath {
				t.Errorf("posted to %q, want %q", v.path, miniMaxPath)
			}
			if got := v.flag("lyrics_optimizer"); got != test.wantOptimizer {
				t.Errorf("lyrics_optimizer = %v, want %v", got, test.wantOptimizer)
			}
			if got := v.flag("is_instrumental"); got != test.wantInstrumental {
				t.Errorf("is_instrumental = %v, want %v", got, test.wantInstrumental)
			}
			if got := v.body["output_format"]; got != "url" {
				t.Errorf("output_format = %v, want url", got)
			}
			if got := v.body["model"]; got != miniMaxDefaultModel {
				t.Errorf("model = %v, want %v", got, miniMaxDefaultModel)
			}
		})
	}
}

// TestGenerateRejectsARequestWithNothingToGoOn guards the case where the UI
// would otherwise spend a request to learn what it already knew.
func TestGenerateRejectsARequestWithNothingToGoOn(t *testing.T) {
	c, err := New(Config{APIKey: "k", BaseURL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = c.Generate(context.Background(), engine.Request{
		Kind:   engine.KindText2Music,
		Spec:   engine.Spec{},
		OutDir: t.TempDir(),
	}, nil)
	if err == nil {
		t.Fatal("Generate accepted a request with neither a prompt nor lyrics")
	}
}

// TestGenerateRejectsAKindTheAdapterCannotBuild checks that the advertised
// capabilities and the implemented ones agree.
func TestGenerateRejectsAKindTheAdapterCannotBuild(t *testing.T) {
	c, err := New(Config{APIKey: "k", BaseURL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = c.Generate(context.Background(), engine.Request{
		Kind:   engine.KindExtract,
		Spec:   engine.Spec{Prompt: "anything"},
		OutDir: t.TempDir(),
	}, nil)
	if err == nil {
		t.Fatal("Generate accepted a kind the engine does not advertise")
	}
}

// TestMurekaRoutesByWhatWasSupplied covers the three gateway routes. Picking
// the wrong one is not a degraded result — it is a rejected request.
func TestMurekaRoutesByWhatWasSupplied(t *testing.T) {
	tests := []struct {
		name      string
		spec      engine.Spec
		wantRoute string
	}{
		{
			name:      "lyrics go to the route that sings them",
			spec:      engine.Spec{Prompt: "pop", Lyrics: "[Verse]\nhold on"},
			wantRoute: murekaLyricsPath,
		},
		{
			name:      "a prompt alone goes to the route that writes its own",
			spec:      engine.Spec{Prompt: "a song about the sea"},
			wantRoute: murekaPromptPath,
		},
		{
			name:      "instrumental goes to the route with no vocal",
			spec:      engine.Spec{Prompt: "ambient", Instrumental: true},
			wantRoute: murekaInstrumentalPath,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			v := newVendor(t, murekaOK)

			c, err := New(Config{Provider: ProviderMureka, APIKey: "test-key", BaseURL: v.URL})
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			result, err := c.Generate(context.Background(), engine.Request{
				Kind:   engine.KindText2Music,
				Spec:   test.spec,
				OutDir: t.TempDir(),
			}, nil)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}

			if v.path != test.wantRoute {
				t.Errorf("posted to %q, want %q", v.path, test.wantRoute)
			}
			if got := v.body["model"]; got != murekaDefaultModel {
				t.Errorf("model = %v, want %v", got, murekaDefaultModel)
			}
			if result.Tracks[0].Duration != 162630*time.Millisecond {
				t.Errorf("duration = %v, want 162.63s from the vendor's choice",
					result.Tracks[0].Duration)
			}
		})
	}
}

// TestDecodeSurfacesWhatTheVendorSaid checks that a refusal reaches the user as
// the vendor's own words, not as a JSON decode error.
func TestDecodeSurfacesWhatTheVendorSaid(t *testing.T) {
	tests := []struct {
		name     string
		provider provider
		body     string
		want     string
	}{
		{
			name:     "minimax reports a status code",
			provider: minimax{},
			body:     `{"base_resp":{"status_code":1004,"status_msg":"invalid api key"}}`,
			want:     "invalid api key",
		},
		{
			name:     "minimax reports an unfinished song",
			provider: minimax{},
			body:     `{"data":{"status":1},"base_resp":{"status_code":0}}`,
			want:     "unfinished",
		},
		{
			name:     "mureka reports a failure reason",
			provider: mureka{},
			body:     `{"status":"failed","failed_reason":"lyrics too long"}`,
			want:     "lyrics too long",
		},
		{
			name:     "mureka reports a pending song",
			provider: mureka{},
			body:     `{"status":"queued"}`,
			want:     "queued",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.provider.decode([]byte(test.body))
			if err == nil {
				t.Fatal("decode accepted a response that carries no audio")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Errorf("error = %q, want it to mention %q", err, test.want)
			}
		})
	}
}

// TestDecodeRejectsANonJSONBody keeps a gateway error page from being reported
// as a successful generation.
func TestDecodeRejectsANonJSONBody(t *testing.T) {
	_, err := minimax{}.decode([]byte("<html>502 Bad Gateway</html>"))
	if err == nil {
		t.Fatal("decode accepted an HTML body")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Errorf("error = %q, want the body quoted so the real cause is visible", err)
	}
}

func TestFormatFromURL(t *testing.T) {
	tests := []struct {
		name        string
		url         string
		contentType string
		want        string
	}{
		{name: "extension wins", url: "https://cdn/x/track.wav?sig=abc", want: "wav"},
		{name: "signed url without an extension", url: "https://cdn/x/9f3a2b?sig=abc", contentType: "audio/mpeg", want: "mp3"},
		{name: "content type is the fallback", url: "https://cdn/x/9f3a2b", contentType: "audio/wav", want: "wav"},
		{name: "neither says anything useful", url: "https://cdn/x/9f3a2b", want: "mp3"},
		{name: "an unparseable url still lands on mp3", url: "://nonsense", want: "mp3"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := formatFromURL(test.url, test.contentType); got != test.want {
				t.Errorf("formatFromURL(%q, %q) = %q, want %q",
					test.url, test.contentType, got, test.want)
			}
		})
	}
}

func TestSubmitFractionLeavesRoomForTheDownload(t *testing.T) {
	// The download needs the rest of the bar, so the submit phase must never
	// take it however long the vendor takes — and must never report done.
	for _, elapsed := range []time.Duration{0, time.Second, time.Minute, time.Hour, 24 * time.Hour} {
		got := submitFraction(elapsed)
		if got < 0 || got > submitCeiling {
			t.Errorf("submitFraction(%v) = %v, want it within [0, %v]", elapsed, got, submitCeiling)
		}
	}
}

func TestCapabilitiesMatchWhatGenerateAccepts(t *testing.T) {
	c, err := New(Config{APIKey: "k"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	caps := c.Capabilities()
	if caps.ControlsDuration {
		t.Error("the vendors choose the song length, so ControlsDuration must be false")
	}
	if !caps.Supports(engine.KindText2Music) {
		t.Error("text2music must be advertised: it is the only kind Generate implements")
	}
	if caps.Supports(engine.KindExtract) {
		t.Error("extract is not implemented, so it must not be advertised")
	}
}
