// Package cloud is an Engine backed by a hosted music generation API.
//
// It is the counterpart to the local engines: rather than loading a model, it
// posts a request to a vendor and downloads what comes back. That makes it
// usable on a machine that cannot run a diffusion model at all, at the cost of
// a network round trip and a charge per song.
//
// Both supported vendors are reached through Tencent Cloud's TokenHub gateway,
// so one account and one key cover them both.
package cloud

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/shaolianbo/jonus/internal/engine"
)

// Provider names accepted by Config.Provider.
const (
	// ProviderMiniMax is MiniMax Music. It writes its own lyrics when the
	// request carries none, which is what makes "describe a song, get a song"
	// possible.
	ProviderMiniMax = "minimax"

	// ProviderMureka is Kunlun's Mureka Music.
	ProviderMureka = "mureka"
)

// DefaultBaseURL is the gateway the vendors are reached through when the
// configuration names none.
const DefaultBaseURL = "https://tokenhub.tencentmaas.com"

// defaultTimeout is applied when the configuration names none.
const defaultTimeout = 10 * time.Minute

// maxResponseBytes bounds a response body. The vendors answer with a JSON
// envelope holding a download link, so anything approaching this is not the
// response we asked for.
const maxResponseBytes = 4 << 20

// maxSongDuration is the ceiling reported to the UI. It is a hint for display,
// not a limit the vendor enforces: the vendors choose a length themselves, and
// no request of ours changes it.
const maxSongDuration = 10 * time.Minute

// submitCeiling is the highest fraction the submit phase may report. The
// download that follows needs the rest of the bar.
const submitCeiling = 0.80

// progressInterval is how often elapsed time is reported while waiting on a
// vendor. The vendors publish no per-step progress, so elapsed time is the only
// signal there is.
const progressInterval = time.Second

// Config describes a cloud engine. Every field is optional except APIKey.
type Config struct {
	// Provider selects the vendor. Empty means ProviderMiniMax.
	Provider string

	// BaseURL is the API root. Empty means the vendor gateway's default.
	BaseURL string

	// APIKey authenticates the request. There is no default: without it the
	// engine cannot be built.
	APIKey string

	// Model overrides the provider's default model.
	Model string

	// Timeout bounds one generation request, including the time the vendor
	// spends writing and rendering. Zero means defaultTimeout.
	Timeout time.Duration
}

// Client implements engine.Engine against a hosted API.
type Client struct {
	provider provider
	model    string
	apiKey   string
	baseURL  string
	http     *http.Client
}

var _ engine.Engine = (*Client)(nil)

// New builds a cloud engine.
//
// It fails on a missing key rather than at the first job: a configuration error
// should surface where it was made, not as a queue entry that fails a minute
// later.
func New(cfg Config) (*Client, error) {
	key := strings.TrimSpace(cfg.APIKey)
	if key == "" {
		return nil, errors.New("cloud engine: no API key; set JONUS_CLOUD_API_KEY to the key from your TokenHub console")
	}

	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if base == "" {
		base = DefaultBaseURL
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	// The switch is the one place that knows which vendors exist, so it is also
	// where each one's default model is named.
	var (
		vendor provider
		model  string
	)
	switch name := strings.TrimSpace(cfg.Provider); name {
	case "", ProviderMiniMax:
		vendor, model = minimax{}, miniMaxDefaultModel
	case ProviderMureka:
		vendor, model = mureka{}, murekaDefaultModel
	default:
		return nil, fmt.Errorf("cloud engine: unknown provider %q (want %s or %s)",
			name, ProviderMiniMax, ProviderMureka)
	}
	if override := strings.TrimSpace(cfg.Model); override != "" {
		model = override
	}

	return &Client{
		provider: vendor,
		model:    model,
		apiKey:   key,
		baseURL:  base,
		http:     &http.Client{Timeout: timeout},
	}, nil
}

// Name implements engine.Engine.
func (c *Client) Name() string { return c.provider.name() }

// Capabilities implements engine.Engine.
//
// Only text-to-music is offered: the vendors do expose more (MiniMax has a
// cover model, Mureka an extend route), but advertising a kind this adapter
// cannot build a request for would put a button in the UI that fails on click.
func (c *Client) Capabilities() engine.Capabilities {
	return engine.Capabilities{
		Kinds:        []engine.Kind{engine.KindText2Music},
		Instrumental: true,
		Lyrics:       true,
		MaxDuration:  maxSongDuration,
		// Both vendors decide the length from the lyrics.
		ControlsDuration: false,
		// The vendors are hosted services and take concurrent requests, though
		// the queue currently serialises work regardless.
		MaxConcurrent: 2,
	}
}

// Generate implements engine.Engine.
//
// The vendors are synchronous: one POST returns a finished song. There is no
// per-step progress to relay, so the fraction is derived from elapsed time and
// is capped below completion — a bar that reaches the end before the audio
// exists would be a lie.
func (c *Client) Generate(
	ctx context.Context,
	req engine.Request,
	progress engine.ProgressFunc,
) (engine.Result, error) {
	if req.Kind != engine.KindText2Music {
		return engine.Result{}, fmt.Errorf("cloud engine: %s does not support %q", c.Name(), req.Kind)
	}
	if req.OutDir == "" {
		return engine.Result{}, errors.New("cloud engine: request has no output directory")
	}
	if err := os.MkdirAll(req.OutDir, 0o755); err != nil {
		return engine.Result{}, fmt.Errorf("cloud engine: create output directory: %w", err)
	}

	progress.Report(engine.PhaseRunning, 0.02, "submitting to "+c.Name())

	httpReq, err := c.provider.request(ctx, submission{
		baseURL: c.baseURL,
		apiKey:  c.apiKey,
		model:   c.model,
		request: req,
	})
	if err != nil {
		return engine.Result{}, err
	}

	body, err := c.post(ctx, httpReq, progress)
	if err != nil {
		return engine.Result{}, err
	}

	song, err := c.provider.decode(body)
	if err != nil {
		return engine.Result{}, err
	}

	progress.Report(engine.PhaseDecoding, 0.85, "downloading audio")

	track, err := c.download(ctx, song, req, progress)
	if err != nil {
		return engine.Result{}, err
	}

	progress.Report(engine.PhaseDone, 1, "done")

	meta := map[string]string{
		"engine":   c.Name(),
		"provider": c.provider.name(),
		"model":    c.model,
	}
	if req.Spec.Duration > 0 {
		meta["duration_note"] = "the provider chooses the length from the lyrics; the requested duration was ignored"
	}

	return engine.Result{
		Tracks: []engine.Track{track},
		TaskID: song.taskID,
		Meta:   meta,
	}, nil
}

// post sends the request and waits for the answer, reporting elapsed time while
// it waits.
//
// The request runs on its own goroutine so that the wait can be observed. The
// channel is buffered, so that goroutine always has somewhere to put its result
// and therefore always exits — including when the context is cancelled first.
func (c *Client) post(
	ctx context.Context,
	req *http.Request,
	progress engine.ProgressFunc,
) ([]byte, error) {
	type outcome struct {
		body []byte
		err  error
	}
	done := make(chan outcome, 1)

	go func() {
		body, err := c.exchange(req)
		done <- outcome{body: body, err: err}
	}()

	start := time.Now()
	ticker := time.NewTicker(progressInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()

		case out := <-done:
			return out.body, out.err

		case <-ticker.C:
			elapsed := time.Since(start)
			progress.Report(engine.PhaseRunning, submitFraction(elapsed),
				fmt.Sprintf("generating, %.0fs elapsed", elapsed.Seconds()))
		}
	}
}

// exchange performs one HTTP round trip and turns a non-2xx into an error.
func (c *Client) exchange(req *http.Request) ([]byte, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cloud engine: %s request: %w", c.provider.name(), err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("cloud engine: read %s response: %w", c.provider.name(), err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cloud engine: %s returned %s: %s",
			c.provider.name(), resp.Status, snippet(body))
	}
	return body, nil
}

// download fetches the finished audio and publishes it in the job's directory.
func (c *Client) download(
	ctx context.Context,
	song generated,
	req engine.Request,
	progress engine.ProgressFunc,
) (engine.Track, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, song.url, nil)
	if err != nil {
		return engine.Track{}, fmt.Errorf("cloud engine: build download request: %w", err)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return engine.Track{}, fmt.Errorf("cloud engine: download audio: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return engine.Track{}, fmt.Errorf("cloud engine: download audio: %s", resp.Status)
	}

	format := formatFromURL(song.url, resp.Header.Get("Content-Type"))
	path, size, err := writeSong(resp.Body, req.OutDir, "track-01."+format)
	if err != nil {
		return engine.Track{}, err
	}
	if size == 0 {
		return engine.Track{}, errors.New("cloud engine: downloaded audio is empty")
	}

	return engine.Track{
		Path:       path,
		Format:     format,
		Duration:   song.duration,
		SampleRate: song.sampleRate,
		// The vendors take no seed, so reporting the requested one back would
		// imply a reproducibility this engine does not have.
		Seed: 0,
	}, nil
}

// submission is everything a provider needs to spell one request in its own
// dialect: where to send it, who is asking, and what was asked for.
type submission struct {
	baseURL string
	apiKey  string
	model   string
	request engine.Request
}

// generated is what a vendor reports about the song it produced.
type generated struct {
	// url is where the finished audio can be downloaded.
	url string

	// taskID is the vendor's identifier for the request, for log correlation.
	taskID string

	// duration and sampleRate are zero when the vendor did not report them.
	duration   time.Duration
	sampleRate int
}

// provider adapts one vendor's HTTP shape onto the flow Client implements once.
//
// The vendors disagree about more than a URL: they differ in how a request is
// spelled and in where the finished audio is named in the response. What they
// share is the shape of the exchange — one POST, then download a link — and
// that is the part Client owns.
type provider interface {
	// name identifies the vendor in the UI and in error messages.
	name() string

	// request builds the POST that starts a generation.
	request(ctx context.Context, sub submission) (*http.Request, error)

	// decode reads a successful response body.
	decode(body []byte) (generated, error)
}

// submitFraction maps elapsed time onto a curve that rises quickly and then
// flattens towards submitCeiling, which it never passes.
//
// The time constant is a guess at a typical song. Both vendors usually answer
// inside a minute, so a bar that is most of the way there after a minute reads
// as accurate, and it stays defensible when a call runs long instead of
// sticking at 99%.
func submitFraction(elapsed time.Duration) float64 {
	const tau = 45 * time.Second
	return 0.02 + (submitCeiling-0.02)*(1-math.Exp(-float64(elapsed)/float64(tau)))
}

// formatFromURL picks the file extension for a downloaded song.
//
// The vendors serve from paths that end in the extension, but a signed CDN URL
// sometimes carries none, so the content type is the fallback.
func formatFromURL(rawURL, contentType string) string {
	if u, err := url.Parse(rawURL); err == nil {
		switch ext := strings.ToLower(path.Ext(u.Path)); ext {
		case ".mp3", ".wav", ".flac", ".m4a", ".ogg":
			return strings.TrimPrefix(ext, ".")
		}
	}

	switch {
	case strings.Contains(contentType, "wav"):
		return "wav"
	case strings.Contains(contentType, "flac"):
		return "flac"
	default:
		return "mp3"
	}
}

// writeSong streams the audio into dir and publishes it under its final name.
//
// It is written as a .part file and renamed on success so that a download cut
// short cannot leave a truncated file where the library would serve it.
func writeSong(body io.Reader, dir, name string) (string, int64, error) {
	path := filepath.Join(dir, name)
	tmp := path + ".part"

	f, err := os.Create(tmp)
	if err != nil {
		return "", 0, fmt.Errorf("cloud engine: create %s: %w", tmp, err)
	}

	size, err := io.Copy(f, body)
	if err != nil {
		f.Close()
		os.Remove(tmp)
		return "", 0, fmt.Errorf("cloud engine: write %s: %w", tmp, err)
	}
	// The file is what the library serves, so a close error is a real failure
	// rather than something to defer away.
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return "", 0, fmt.Errorf("cloud engine: close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return "", 0, fmt.Errorf("cloud engine: publish %s: %w", path, err)
	}
	return path, size, nil
}

// snippet trims a response body for inclusion in an error message.
func snippet(body []byte) string {
	const limit = 200

	runes := []rune(strings.TrimSpace(string(body)))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + "…"
}
