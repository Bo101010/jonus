// Package engine defines the seam between jonus and whatever actually
// produces audio.
//
// The interface and the request/result types live here rather than in any
// implementation because both the consumer (the job queue) and every
// implementation need them. Implementations satisfy Engine structurally; they
// never import it to declare conformance.
package engine

import (
	"context"
	"math"
	"slices"
	"time"
)

// Kind selects the operation to perform. The values mirror the task types
// exposed by ACE-Step so that the adapter is a direct mapping.
type Kind string

const (
	// KindText2Music generates a song from a description and optional lyrics.
	KindText2Music Kind = "text2music"

	// KindCover re-renders an existing track in a new style.
	KindCover Kind = "cover"

	// KindRepaint regenerates a time range inside an existing track.
	KindRepaint Kind = "repaint"

	// KindLego regenerates a single instrument stem.
	KindLego Kind = "lego"

	// KindExtract separates an existing track into stems.
	KindExtract Kind = "extract"

	// KindComplete continues an existing track.
	KindComplete Kind = "complete"
)

// Valid reports whether k is a kind the engine layer understands.
func (k Kind) Valid() bool {
	switch k {
	case KindText2Music, KindCover, KindRepaint, KindLego, KindExtract, KindComplete:
		return true
	default:
		return false
	}
}

// Spec is the engine-agnostic description of what to generate. Everything
// needed to reproduce a job deterministically is held here, so a job can be
// replayed from its stored parameters.
type Spec struct {
	// Prompt describes the style or mood, for example "warm acoustic folk".
	Prompt string `json:"prompt"`

	// Lyrics is the song text. Lines may carry structure tags such as
	// [Verse] or [Chorus]. Empty lyrics with Instrumental unset means the
	// engine should write its own.
	Lyrics string `json:"lyrics"`

	// Instrumental asks for a track with no lead vocal.
	Instrumental bool `json:"instrumental"`

	// Duration is the requested length. Zero means the engine's default.
	Duration time.Duration `json:"duration"`

	// Language is a BCP-47-ish hint such as "zh" or "en".
	Language string `json:"language"`

	// Seed makes generation reproducible. Zero means "pick one"; the
	// resolved value is reported back in Result so it can be replayed.
	Seed int64 `json:"seed"`

	// Model selects an engine-specific model. Empty means the default.
	Model string `json:"model"`

	// Extra carries engine-specific knobs that do not warrant a first-class
	// field. Engines ignore keys they do not understand.
	Extra map[string]string `json:"extra,omitempty"`
}

// Input references a local file the engine must read, such as the source audio
// for a cover, a repaint, or a stem extraction.
type Input struct {
	// Path is an absolute path to the input audio.
	Path string `json:"path"`

	// Role says how the file is used: "source" for the track being
	// transformed, "reference" for stylistic guidance.
	Role string `json:"role"`
}

// Request is one unit of work handed to an Engine.
type Request struct {
	Kind Kind `json:"kind"`
	Spec Spec `json:"spec"`

	// OutDir is the directory the engine must write its tracks into. The
	// caller creates it before Generate is called.
	//
	// The layout of the data directory belongs to the storage layer, so the
	// engine is told where to write rather than choosing for itself. This also
	// keeps the paths in Result predictable, which the library depends on.
	OutDir string `json:"out_dir"`

	Inputs []Input `json:"inputs,omitempty"`
}

// Track is one produced audio artifact.
type Track struct {
	// Path is the absolute path of the written audio file.
	Path string `json:"path"`

	// Format is the container format: "wav", "mp3", or "flac".
	Format string `json:"format"`

	// Duration is the length of the produced audio.
	Duration time.Duration `json:"duration"`

	// SampleRate is the rate the audio was rendered at, in Hz. Zero means the
	// engine did not report one.
	SampleRate int `json:"sample_rate,omitempty"`

	// Seed is the seed actually used, which may have been chosen by the
	// engine when Spec.Seed was zero.
	Seed int64 `json:"seed"`

	// Stems maps a stem name to a file path. It is populated by
	// KindExtract and left nil otherwise.
	Stems map[string]string `json:"stems,omitempty"`
}

// Result is what an Engine returns for a successful Request.
type Result struct {
	Tracks []Track `json:"tracks"`

	// Meta carries engine-specific annotations worth surfacing in the UI,
	// such as the resolved model or a lyrics draft.
	Meta map[string]string `json:"meta,omitempty"`

	// TaskID is the engine-side identifier, useful for correlating logs.
	TaskID string `json:"task_id,omitempty"`
}

// Phase is a coarse lifecycle marker.
type Phase string

const (
	PhaseQueued   Phase = "queued"
	PhaseLoading  Phase = "loading_model"
	PhaseRunning  Phase = "generating"
	PhaseDecoding Phase = "decoding"
	PhaseDone     Phase = "done"
)

// Progress is a coarse progress report.
//
// Most engines, ACE-Step included, expose no per-step progress, so Fraction is
// an estimate derived from elapsed time. Callers must present it as an
// estimate rather than a measurement.
type Progress struct {
	Phase    Phase
	Fraction float64 // 0..1, estimated
	Message  string
	At       time.Time
}

// ProgressFunc receives progress reports.
//
// Contract:
//   - It must not block; the engine calls it synchronously from the goroutine
//     running Generate.
//   - It may be nil.
//   - It is never called after Generate returns.
//   - Cancellation is signalled through the context, never through the callback.
type ProgressFunc func(Progress)

// Report sends one update.
//
// It exists so an engine never has to guard against a nil callback or remember
// to clamp the fraction: both are properties of the contract, and both are
// easier to get wrong at each call site than in one place. Calling Report on a
// nil ProgressFunc is safe.
func (f ProgressFunc) Report(phase Phase, fraction float64, message string) {
	if f == nil {
		return
	}
	f(Progress{
		Phase:    phase,
		Fraction: math.Max(0, math.Min(1, fraction)),
		Message:  message,
		At:       time.Now(),
	})
}

// Capabilities describes what an Engine can do, so that the CLI and web UI can
// gate features without engine-specific code.
type Capabilities struct {
	Kinds         []Kind        `json:"kinds"`
	Instrumental  bool          `json:"instrumental"`
	Lyrics        bool          `json:"lyrics"`
	MaxDuration   time.Duration `json:"max_duration"`
	MaxConcurrent int           `json:"max_concurrent"`

	// ControlsDuration reports whether Spec.Duration has any effect. The hosted
	// vendors derive a song's length from its lyrics and expose no length
	// parameter, so a request for ninety seconds is not one they can honour.
	//
	// The zero value is false, which is the safe direction: an engine that
	// forgets to set it loses a control, where one that wrongly claims it would
	// show the user a slider that does nothing. MaxDuration is meaningless when
	// this is false.
	ControlsDuration bool `json:"controls_duration"`
}

// Supports reports whether the engine handles the given kind.
func (c Capabilities) Supports(k Kind) bool {
	return slices.Contains(c.Kinds, k)
}

// Engine produces audio. Implementations need not be safe for concurrent use:
// the queue serialises calls, which also matches the single-device constraint
// of local accelerator hardware.
type Engine interface {
	// Name identifies the engine in configuration and in the UI.
	Name() string

	// Capabilities reports what this engine supports.
	Capabilities() Capabilities

	// Generate runs one request to completion. It returns ctx.Err() when the
	// context is cancelled, and must stop doing work when that happens.
	Generate(ctx context.Context, req Request, progress ProgressFunc) (Result, error)
}

// ModelLister is an optional capability for engines that can enumerate their
// models. Callers discover it with a type assertion.
type ModelLister interface {
	ListModels(ctx context.Context) ([]string, error)
}

// Cleaner is an optional capability for engines holding resources that must be
// released.
type Cleaner interface {
	Close() error
}
