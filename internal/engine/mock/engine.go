// Package mock is an Engine that synthesises audio locally, with no model and
// no external process.
//
// It exists so the whole product — queue, persistence, progress, library,
// playback — can be built and demonstrated before a multi-gigabyte model is
// downloaded. Its output is a simple arrangement, not a song; what matters is
// that it is a real, decodable, deterministic audio file.
package mock

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/shaolianbo/jonus/internal/audio"
	"github.com/shaolianbo/jonus/internal/engine"
)

// Name is the engine's identifier in configuration and in the UI.
const Name = "mock"

// DefaultWorkTime is how long a default run pretends to work. The mock has no
// real model, so without a simulated delay the progress bar would jump from
// nothing to finished and the streaming path would go untested.
const DefaultWorkTime = 3 * time.Second

// Engine implements engine.Engine by synthesising audio in-process.
type Engine struct {
	sampleRate int
	workTime   time.Duration
}

var _ engine.Engine = (*Engine)(nil)

// Option configures an Engine.
type Option func(*Engine)

// WithSampleRate overrides the output sample rate. Mostly useful in tests,
// where a low rate keeps fixtures small.
func WithSampleRate(hz int) Option {
	return func(e *Engine) { e.sampleRate = hz }
}

// WithWorkTime sets how long a run simulates work. Zero makes Generate return
// as fast as it can, which is what tests want.
func WithWorkTime(d time.Duration) Option {
	return func(e *Engine) { e.workTime = d }
}

// New builds a mock engine.
func New(opts ...Option) *Engine {
	e := &Engine{sampleRate: sampleRate, workTime: DefaultWorkTime}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Name implements engine.Engine.
func (e *Engine) Name() string { return Name }

// Capabilities implements engine.Engine. The mock accepts every kind the
// engine layer defines, so the UI can exercise each code path.
func (e *Engine) Capabilities() engine.Capabilities {
	return engine.Capabilities{
		Kinds: []engine.Kind{
			engine.KindText2Music,
			engine.KindCover,
			engine.KindRepaint,
			engine.KindLego,
			engine.KindExtract,
			engine.KindComplete,
		},
		Instrumental:     true,
		Lyrics:           true,
		MaxDuration:      maxDuration,
		MaxConcurrent:    1,
		ControlsDuration: true,
	}
}

// Generate implements engine.Engine.
func (e *Engine) Generate(
	ctx context.Context,
	req engine.Request,
	progress engine.ProgressFunc,
) (engine.Result, error) {
	if !req.Kind.Valid() {
		return engine.Result{}, fmt.Errorf("mock engine: unknown kind %q", req.Kind)
	}
	if req.OutDir == "" {
		return engine.Result{}, errors.New("mock engine: request has no output directory")
	}
	if err := os.MkdirAll(req.OutDir, 0o755); err != nil {
		return engine.Result{}, fmt.Errorf("mock engine: create output directory: %w", err)
	}

	// A zero seed means "pick one", and the chosen value is reported back so
	// the same track can be regenerated later.
	seed := req.Spec.Seed
	if seed == 0 {
		seed = randomSeed()
	}

	progress.Report(engine.PhaseLoading, 0.02, "preparing mock engine")

	if err := e.simulateWork(ctx, progress); err != nil {
		return engine.Result{}, err
	}

	progress.Report(engine.PhaseDecoding, 0.92, "encoding audio")

	track, err := e.writeTrack(req, seed, "track-01.wav")
	if err != nil {
		return engine.Result{}, err
	}

	result := engine.Result{
		Tracks: []engine.Track{track},
		TaskID: fmt.Sprintf("mock-%d", seed),
		Meta: map[string]string{
			"engine": Name,
			"seed":   fmt.Sprintf("%d", seed),
			"note":   "synthesised locally; not a language model",
		},
	}

	if req.Kind == engine.KindExtract {
		stems, err := e.writeStems(ctx, req, seed)
		if err != nil {
			return engine.Result{}, err
		}
		result.Tracks[0].Stems = stems
		result.Meta["stems"] = fmt.Sprintf("%d", len(stems))
	}

	progress.Report(engine.PhaseDone, 1, "done")
	return result, nil
}

// writeTrack renders one file and describes it.
func (e *Engine) writeTrack(req engine.Request, seed int64, name string) (engine.Track, error) {
	samples := compose(req.Spec, seed, e.sampleRate)
	path := filepath.Join(req.OutDir, name)

	if err := writeWAV(path, samples, e.sampleRate); err != nil {
		return engine.Track{}, err
	}

	return engine.Track{
		Path:       path,
		Format:     "wav",
		Duration:   time.Duration(float64(len(samples)) / float64(e.sampleRate) * float64(time.Second)),
		SampleRate: e.sampleRate,
		Seed:       seed,
	}, nil
}

// writeStems splits the arrangement back into the layers it was built from.
//
// This is the honest mock equivalent of source separation: the layers are still
// available, so they are rendered separately rather than estimated. The mock
// has no percussion voice, so there is no drum stem.
func (e *Engine) writeStems(ctx context.Context, req engine.Request, seed int64) (map[string]string, error) {
	layers := []struct {
		name   string
		voices voiceSet
	}{
		{"vocals", voiceVocal},
		{"bass", voiceBass},
		{"other", voicePad | voiceLead},
	}

	// The stems must partition exactly the layers the mix contains, or they
	// would not sum back to it. An instrumental request has no vocal layer, so
	// it gets no vocal stem.
	base := voicesFor(req.Spec)

	// Mastering every stem with the full mix's gain keeps the sum faithful.
	gain := gainFor(renderVoices(req.Spec, seed, e.sampleRate, base))

	stems := make(map[string]string, len(layers))
	for _, layer := range layers {
		if base&layer.voices == 0 {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		mix := renderVoices(req.Spec, seed, e.sampleRate, layer.voices)
		master(mix, e.sampleRate, gain)

		path := filepath.Join(req.OutDir, "stem-"+layer.name+".wav")
		if err := writeWAV(path, mix, e.sampleRate); err != nil {
			return nil, err
		}
		stems[layer.name] = path
	}
	return stems, nil
}

// simulateWork reports plausible progress until workTime has elapsed.
//
// The fraction is derived from elapsed time, not measured from the engine —
// exactly the honest compromise the real ACE-Step adapter has to make, so the
// UI is built against an estimate from the start.
func (e *Engine) simulateWork(ctx context.Context, progress engine.ProgressFunc) error {
	if e.workTime <= 0 {
		return nil
	}

	const tick = 100 * time.Millisecond

	start := time.Now()
	timer := time.NewTimer(tick)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			frac := math.Min(1, float64(time.Since(start))/float64(e.workTime))
			progress.Report(phaseFor(frac), fractionFor(frac), messageFor(frac))
			if frac >= 1 {
				return nil
			}
			timer.Reset(tick)
		}
	}
}

// phaseFor maps overall progress onto a lifecycle phase.
func phaseFor(frac float64) engine.Phase {
	switch {
	case frac < 0.08:
		return engine.PhaseLoading
	case frac < 0.92:
		return engine.PhaseRunning
	default:
		return engine.PhaseDecoding
	}
}

// fractionFor maps simulated progress onto the 0..0.9 band, leaving room for
// the encode that follows.
func fractionFor(frac float64) float64 {
	switch {
	case frac < 0.08:
		return 0.02 + frac/0.08*0.06
	case frac < 0.92:
		return 0.08 + (frac-0.08)/0.84*0.82
	default:
		return 0.90 + (frac-0.92)/0.08*0.02
	}
}

func messageFor(frac float64) string {
	switch {
	case frac < 0.08:
		return "preparing mock engine"
	case frac < 0.92:
		return "synthesising"
	default:
		return "finishing"
	}
}

// randomSeed returns a non-zero seed from the system CSPRNG.
//
// Zero is reserved for "pick one", so a drawn zero would be reinterpreted as
// "unset" by anything reading the value back.
func randomSeed() int64 {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// A seed is not a security decision; falling back keeps generation
		// working rather than failing a job over entropy.
		return time.Now().UnixNano() | 1
	}
	seed := int64(binary.LittleEndian.Uint64(buf[:]))
	if seed == 0 {
		return 1
	}
	return seed
}

// writeWAV encodes samples to path, creating the file only once the encoding
// succeeds so that a failure cannot leave a half-written track in the library.
func writeWAV(path string, samples []float32, sampleRate int) error {
	tmp := path + ".part"

	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("mock engine: create %s: %w", tmp, err)
	}

	if err := audio.EncodeWAV(f, samples, sampleRate); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("mock engine: encode %s: %w", path, err)
	}
	// Encoding a track is a durability point: the file is what the library
	// serves, so a close error has to be reported rather than deferred away.
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("mock engine: close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("mock engine: publish %s: %w", path, err)
	}
	return nil
}
