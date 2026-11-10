// Package app is the service layer shared by every transport.
//
// The CLI and the HTTP API are two front ends onto the same operations. Any
// behaviour that both need — creating a job, listing the library, deciding what
// a cancellation means — lives here, so the two front ends cannot drift apart.
//
// App is also the composition root: it is the one place that knows which
// storage, engine, and queue implementations are in use.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/shaolianbo/jonus/internal/config"
	"github.com/shaolianbo/jonus/internal/engine"
	"github.com/shaolianbo/jonus/internal/engine/cloud"
	"github.com/shaolianbo/jonus/internal/engine/mock"
	"github.com/shaolianbo/jonus/internal/job"
	"github.com/shaolianbo/jonus/internal/job/sqlite"
	"github.com/shaolianbo/jonus/internal/queue"
	"github.com/shaolianbo/jonus/internal/storage"
)

// titleLimit bounds a title derived from a prompt, in runes.
const titleLimit = 40

// App holds the wired-up services.
type App struct {
	cfg    *config.Config
	log    *slog.Logger
	layout *storage.Layout

	jobs   job.JobStore
	tracks job.TrackStore
	engine engine.Engine
	hub    *queue.Hub
	queue  *queue.Queue

	closers []func() error
}

// New builds the application: it opens the database, prepares the data
// directory, selects the engine, and wires the queue.
//
// The returned App must be closed.
func New(ctx context.Context, cfg *config.Config, log *slog.Logger) (*App, error) {
	layout, err := storage.NewLayout(cfg.DataDir)
	if err != nil {
		return nil, err
	}

	dbPath := cfg.DBPath
	if dbPath == "" {
		dbPath = layout.DBPath()
	}
	db, err := sqlite.Open(ctx, dbPath)
	if err != nil {
		return nil, err
	}

	eng, err := newEngine(cfg)
	if err != nil {
		db.Close()
		return nil, err
	}

	store := sqlite.NewStore(db)
	hub := queue.NewHub()

	a := &App{
		cfg:    cfg,
		log:    log,
		layout: layout,
		jobs:   store,
		tracks: store,
		engine: eng,
		hub:    hub,
		closers: []func() error{
			db.Close,
		},
	}
	a.queue = queue.New(store, store, eng, hub, layout, log)

	if cleaner, ok := eng.(engine.Cleaner); ok {
		a.closers = append(a.closers, cleaner.Close)
	}
	return a, nil
}

// newEngine selects the configured generation engine.
func newEngine(cfg *config.Config) (engine.Engine, error) {
	switch cfg.EngineName {
	case config.EngineMock:
		return mock.New(), nil

	case config.EngineACEStep:
		return nil, fmt.Errorf(
			"engine %q needs the Python sidecar, which is not wired up yet; run with --engine %s",
			config.EngineACEStep, config.EngineMock)

	case config.EngineCloud:
		return cloud.New(cloud.Config{
			Provider: cfg.Cloud.Provider,
			BaseURL:  cfg.Cloud.BaseURL,
			APIKey:   cfg.Cloud.APIKey,
			Model:    cfg.Cloud.Model,
			Timeout:  cfg.Cloud.Timeout,
		})

	default:
		return nil, fmt.Errorf("unknown engine %q", cfg.EngineName)
	}
}

// Run processes the job queue until ctx is cancelled.
func (a *App) Run(ctx context.Context) error { return a.queue.Run(ctx) }

// Close releases everything the application holds.
func (a *App) Close() error {
	var errs []error
	// Close in reverse order of construction, so dependents go before what
	// they depend on.
	for i := len(a.closers) - 1; i >= 0; i-- {
		if err := a.closers[i](); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Config returns the configuration the application was built with.
func (a *App) Config() *config.Config { return a.cfg }

// Layout returns the on-disk layout, for callers that need paths.
func (a *App) Layout() *storage.Layout { return a.layout }

// Hub returns the progress hub, which the SSE endpoints subscribe to.
func (a *App) Hub() *queue.Hub { return a.hub }

// Subscribe returns a live event stream for one job, or for every job when
// jobID is empty. The caller must Close the subscription.
func (a *App) Subscribe(jobID job.ID) *queue.Subscription { return a.hub.Subscribe(jobID) }

// GenerateRequest describes a generation to submit.
type GenerateRequest struct {
	Kind   engine.Kind
	Title  string
	Prompt string

	// Style is a comma-separated list of tags describing how the song should
	// sound. It is folded into the prompt rather than kept apart because that
	// is what the engine takes: one description, not a description plus tags.
	Style string

	Lyrics       string
	Instrumental bool
	Duration     time.Duration
	Language     string
	Seed         int64
	Model        string
	Priority     int
	Inputs       []engine.Input
}

// Generate validates a request and submits it to the queue.
//
// It returns as soon as the job is durable; the caller watches the returned
// job's events for progress.
func (a *App) Generate(ctx context.Context, in GenerateRequest) (*job.Job, error) {
	if !in.Kind.Valid() {
		return nil, fmt.Errorf("unknown job kind %q", in.Kind)
	}
	if !a.engine.Capabilities().Supports(in.Kind) {
		return nil, fmt.Errorf("engine %s does not support %q", a.engine.Name(), in.Kind)
	}

	spec := engine.Spec{
		Prompt:       withStyle(in.Prompt, in.Style),
		Lyrics:       in.Lyrics,
		Instrumental: in.Instrumental,
		Duration:     in.Duration,
		Language:     in.Language,
		Seed:         in.Seed,
		Model:        in.Model,
	}

	// The stored parameters are the request minus its output directory. Where
	// files go is a property of this machine's data directory, not of the job,
	// so it is applied when the job runs.
	params, err := json.Marshal(engine.Request{Kind: in.Kind, Spec: spec, Inputs: in.Inputs})
	if err != nil {
		return nil, fmt.Errorf("encode job parameters: %w", err)
	}

	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = deriveTitle(spec.Prompt)
	}

	j := job.New(job.Draft{
		Kind:         string(in.Kind),
		Engine:       a.engine.Name(),
		Title:        title,
		Prompt:       spec.Prompt,
		Lyrics:       spec.Lyrics,
		Instrumental: spec.Instrumental,
		Duration:     spec.Duration,
		Language:     spec.Language,
		Seed:         spec.Seed,
		ParamsJSON:   string(params),
		Priority:     in.Priority,
	})

	if err := a.queue.Submit(ctx, j); err != nil {
		return nil, err
	}
	a.log.Info("job submitted", "job", j.ID, "kind", j.Kind, "title", j.Title)
	return j, nil
}

// withStyle folds style tags into a prompt.
//
// The two are joined rather than passed separately because the engine's input is
// one description. Keeping the join here means the CLI and the web UI cannot
// disagree about what a style tag does.
func withStyle(prompt, style string) string {
	prompt = strings.TrimSpace(prompt)
	style = strings.TrimSpace(style)

	switch {
	case style == "":
		return prompt
	case prompt == "":
		return style
	default:
		return prompt + ", " + style
	}
}

// deriveTitle makes a readable title out of a prompt.
func deriveTitle(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "Untitled"
	}

	// Collapse whitespace so a multi-line prompt does not produce a title with
	// newlines in it.
	prompt = strings.Join(strings.Fields(prompt), " ")

	runes := []rune(prompt)
	if len(runes) <= titleLimit {
		return prompt
	}
	return strings.TrimSpace(string(runes[:titleLimit])) + "…"
}

// JobDetail is a job together with the audio it produced.
type JobDetail struct {
	Job    *job.Job     `json:"job"`
	Tracks []*job.Track `json:"tracks"`
}

// Job returns one job and its tracks.
func (a *App) Job(ctx context.Context, id job.ID) (*JobDetail, error) {
	j, err := a.jobs.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	tracks, err := a.tracks.ListTracks(ctx, job.TrackFilter{JobID: id})
	if err != nil {
		return nil, err
	}
	return &JobDetail{Job: j, Tracks: tracks}, nil
}

// ListJobs returns jobs matching f, newest first.
func (a *App) ListJobs(ctx context.Context, f job.Filter) ([]*job.Job, error) {
	return a.jobs.List(ctx, f)
}

// CancelJob stops a job.
func (a *App) CancelJob(ctx context.Context, id job.ID) error {
	if err := a.queue.Cancel(ctx, id); err != nil {
		return err
	}
	a.log.Info("job cancellation requested", "job", id)
	return nil
}

// RetryJob creates a new job from a finished one's parameters.
func (a *App) RetryJob(ctx context.Context, id job.ID) (*job.Job, error) {
	retry, err := a.queue.Retry(ctx, id)
	if err != nil {
		return nil, err
	}
	a.log.Info("job retried", "job", retry.ID, "original", id)
	return retry, nil
}

// DeleteJob removes a job, its tracks, and their files.
//
// The database row goes first: if removing files then fails, the result is
// orphaned audio on disk rather than a library entry pointing at a file that is
// gone.
func (a *App) DeleteJob(ctx context.Context, id job.ID) error {
	j, err := a.jobs.Get(ctx, id)
	if err != nil {
		return err
	}
	if !j.State.Terminal() {
		return fmt.Errorf("job %s is %s: cancel it before deleting", id, j.State)
	}

	tracks, err := a.tracks.ListTracks(ctx, job.TrackFilter{JobID: id})
	if err != nil {
		return err
	}
	trackIDs := make([]job.ID, len(tracks))
	for i, t := range tracks {
		trackIDs[i] = t.ID
	}

	if err := a.jobs.Delete(ctx, id); err != nil {
		return err
	}
	if err := a.layout.RemoveJob(id, trackIDs); err != nil {
		return err
	}

	a.hub.Forget(id)
	a.log.Info("job deleted", "job", id, "tracks", len(trackIDs))
	return nil
}

// Library returns tracks matching f, newest first.
func (a *App) Library(ctx context.Context, f job.TrackFilter) ([]*job.Track, error) {
	return a.tracks.ListTracks(ctx, f)
}

// Track returns one track.
func (a *App) Track(ctx context.Context, id job.ID) (*job.Track, error) {
	return a.tracks.GetTrack(ctx, id)
}

// EngineStatus describes the generation engine for the UI.
type EngineStatus struct {
	Name         string              `json:"name"`
	State        string              `json:"state"`
	Capabilities engine.Capabilities `json:"capabilities"`

	// Models is populated only by engines that can enumerate them.
	Models []string `json:"models,omitempty"`

	// LastError explains a failed engine, so the UI can say what went wrong
	// rather than only that something did.
	LastError string `json:"last_error,omitempty"`
}

// EngineState values reported by EngineStatus.
const (
	// EngineReady means the engine can accept work now.
	EngineReady = "ready"

	// EngineUnavailable means the engine exists but cannot run.
	EngineUnavailable = "unavailable"
)

// EngineStatus reports the current state of the engine.
func (a *App) EngineStatus(ctx context.Context) EngineStatus {
	status := EngineStatus{
		Name:         a.engine.Name(),
		State:        EngineReady,
		Capabilities: a.engine.Capabilities(),
	}

	if lister, ok := a.engine.(engine.ModelLister); ok {
		models, err := lister.ListModels(ctx)
		if err != nil {
			status.State = EngineUnavailable
			status.LastError = err.Error()
			return status
		}
		status.Models = models
	}
	return status
}
