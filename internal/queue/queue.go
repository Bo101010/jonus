// Package queue runs generation jobs and broadcasts their progress.
//
// The queue is the only place that calls an engine. It reads work from the job
// store, so a job submitted by the CLI, by the web UI, or left over from a
// previous run is all handled the same way.
package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/shaolianbo/jonus/internal/engine"
	"github.com/shaolianbo/jonus/internal/job"
	"github.com/shaolianbo/jonus/internal/storage"
)

// Tuning defaults.
const (
	// defaultPollInterval bounds how long a job submitted by another process
	// waits before this queue notices it. In-process submissions wake the
	// worker immediately; the interval is only a backstop.
	defaultPollInterval = time.Second

	// progressFlushInterval is how often progress is written to the database.
	// The hub carries every report; only this throttle reaches the disk.
	progressFlushInterval = 2 * time.Second

	// finalizeTimeout bounds the bookkeeping that records how a job ended. It
	// runs on a context detached from cancellation, because a job that was
	// cancelled or interrupted still has to be recorded as such.
	finalizeTimeout = 5 * time.Second
)

// Queue runs jobs from the job store against a single engine.
type Queue struct {
	store  job.JobStore
	tracks job.TrackStore
	engine engine.Engine
	hub    *Hub
	layout *storage.Layout
	log    *slog.Logger

	workers      int
	pollInterval time.Duration

	// notify wakes a waiting worker when work is submitted in this process.
	notify chan struct{}

	mu      sync.Mutex
	running map[job.ID]context.CancelFunc
}

// Option configures a Queue.
type Option func(*Queue)

// WithWorkers sets how many jobs may run at once. It is clamped by the engine's
// declared MaxConcurrent: a local accelerator is a single device, so running
// two generations against it would only make both slower.
func WithWorkers(n int) Option {
	return func(q *Queue) { q.workers = n }
}

// WithPollInterval overrides how often the worker looks for externally
// submitted work.
func WithPollInterval(d time.Duration) Option {
	return func(q *Queue) { q.pollInterval = d }
}

// New builds a queue.
func New(
	store job.JobStore,
	tracks job.TrackStore,
	eng engine.Engine,
	hub *Hub,
	layout *storage.Layout,
	log *slog.Logger,
	opts ...Option,
) *Queue {
	q := &Queue{
		store:        store,
		tracks:       tracks,
		engine:       eng,
		hub:          hub,
		layout:       layout,
		log:          log,
		workers:      1,
		pollInterval: defaultPollInterval,
		notify:       make(chan struct{}, 1),
		running:      make(map[job.ID]context.CancelFunc),
	}
	for _, opt := range opts {
		opt(q)
	}

	if maxConcurrent := eng.Capabilities().MaxConcurrent; maxConcurrent > 0 {
		q.workers = min(q.workers, maxConcurrent)
	}
	q.workers = max(q.workers, 1)
	return q
}

// Submit records a pending job and wakes a worker.
//
// The job is durable before this returns, so a submission survives the process
// being killed immediately afterwards.
func (q *Queue) Submit(ctx context.Context, j *job.Job) error {
	if j.State != job.StatePending {
		return fmt.Errorf("queue: cannot submit job %s in state %s", j.ID, j.State)
	}
	if err := q.store.Create(ctx, j); err != nil {
		return err
	}

	q.publish(Event{
		JobID: j.ID,
		State: job.StatePending,
		Phase: string(engine.PhaseQueued),
		At:    time.Now(),
	})
	q.wake()
	return nil
}

// Run processes jobs until ctx is cancelled.
//
// Jobs left running by a previous process are marked interrupted before the
// first job is claimed, so the queue never reports work it is not doing.
func (q *Queue) Run(ctx context.Context) error {
	if n, err := q.store.RecoverInterrupted(ctx); err != nil {
		return fmt.Errorf("queue: recover interrupted jobs: %w", err)
	} else if n > 0 {
		q.log.Info("marked jobs from a previous run as interrupted", "count", n)
	}

	q.log.Info("queue started", "workers", q.workers, "engine", q.engine.Name())

	var wg sync.WaitGroup
	for worker := range q.workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			q.work(ctx, worker)
		}()
	}

	<-ctx.Done()
	wg.Wait()
	q.log.Info("queue stopped")
	return nil
}

// Cancel stops a job.
//
// A running job is stopped through its context: the engine is asked to give up,
// and the worker then records the outcome. A pending job is cancelled in the
// store. In both cases the final state is written by whoever owns the job.
func (q *Queue) Cancel(ctx context.Context, id job.ID) error {
	q.mu.Lock()
	cancel, running := q.running[id]
	q.mu.Unlock()

	if running {
		cancel()
		return nil
	}
	return q.store.RequestCancel(ctx, id)
}

// Retry creates a new job from a finished one's stored parameters.
//
// The original keeps its outcome: a retry is a new attempt, not a rewrite of
// history.
func (q *Queue) Retry(ctx context.Context, id job.ID) (*job.Job, error) {
	original, err := q.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !original.State.Terminal() {
		return nil, fmt.Errorf("queue: job %s is %s, not finished: %w",
			id, original.State, job.ErrInvalidTransition)
	}

	retry := job.New(original.Draft())
	if err := q.Submit(ctx, retry); err != nil {
		return nil, err
	}
	return retry, nil
}

// work claims and runs jobs until ctx is cancelled.
func (q *Queue) work(ctx context.Context, worker int) {
	ticker := time.NewTicker(q.pollInterval)
	defer ticker.Stop()

	for {
		if ctx.Err() != nil {
			return
		}

		claimed, err := q.store.ClaimNext(ctx)
		switch {
		case err == nil:
			q.run(ctx, worker, claimed)
			continue
		case errors.Is(err, job.ErrNotFound):
			// Nothing to do. Wait for a submission or the next poll.
		case ctx.Err() != nil:
			return
		default:
			q.log.Error("claiming a job failed", "worker", worker, "error", err)
		}

		select {
		case <-ctx.Done():
			return
		case <-q.notify:
		case <-ticker.C:
		}
	}
}

// run executes one claimed job and records how it ended.
func (q *Queue) run(ctx context.Context, worker int, j *job.Job) {
	jobCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	q.mu.Lock()
	q.running[j.ID] = cancel
	q.mu.Unlock()
	defer func() {
		q.mu.Lock()
		delete(q.running, j.ID)
		q.mu.Unlock()
	}()

	log := q.log.With("job", j.ID, "kind", j.Kind, "engine", j.Engine)
	log.Info("job started")
	started := time.Now()

	q.publish(Event{
		JobID: j.ID,
		State: job.StateRunning,
		Phase: string(engine.PhaseLoading),
		At:    started,
	})

	req, err := q.request(j)
	if err != nil {
		q.finish(ctx, log, j.ID, job.Finish{State: job.StateFailed, Error: err.Error()})
		return
	}

	writer := &progressWriter{
		store:     q.store,
		hub:       q.hub,
		jobID:     j.ID,
		baseCtx:   jobCtx,
		log:       log,
		lastFlush: started,
	}

	result, genErr := q.engine.Generate(jobCtx, req, writer.report)

	finish := classify(genErr, ctx.Err())
	if finish.State == job.StateSucceeded {
		if err := q.persistTracks(ctx, j, result); err != nil {
			finish = job.Finish{State: job.StateFailed, Error: err.Error()}
		}
	}

	q.finish(ctx, log, j.ID, finish)
	log.Info("job finished",
		"state", finish.State,
		"elapsed", time.Since(started).Round(time.Millisecond),
	)
}

// classify turns an engine error into the state the job ended in.
func classify(genErr, queueErr error) job.Finish {
	switch {
	case genErr == nil:
		return job.Finish{State: job.StateSucceeded}

	case errors.Is(genErr, context.Canceled):
		// A cancellation caused by the queue shutting down is not the user's
		// doing, so the job is interrupted rather than cancelled: it can be
		// retried without implying it was abandoned.
		if queueErr != nil {
			return job.Finish{State: job.StateInterrupted, Error: "interrupted by shutdown"}
		}
		return job.Finish{State: job.StateCanceled, Error: "canceled"}

	case errors.Is(genErr, context.DeadlineExceeded):
		return job.Finish{State: job.StateFailed, Error: "timed out: " + genErr.Error()}

	default:
		return job.Finish{State: job.StateFailed, Error: genErr.Error()}
	}
}

// request reconstructs the engine request for a job.
//
// The stored parameters are authoritative, so a job always replays the request
// it was created with rather than whatever the current defaults happen to be.
func (q *Queue) request(j *job.Job) (engine.Request, error) {
	if j.ParamsJSON == "" {
		return engine.Request{}, fmt.Errorf("job %s has no stored parameters", j.ID)
	}

	var req engine.Request
	if err := json.Unmarshal([]byte(j.ParamsJSON), &req); err != nil {
		return engine.Request{}, fmt.Errorf("job %s has unreadable parameters: %w", j.ID, err)
	}
	if !req.Kind.Valid() {
		return engine.Request{}, fmt.Errorf("job %s has unknown kind %q", j.ID, req.Kind)
	}
	if !q.engine.Capabilities().Supports(req.Kind) {
		return engine.Request{}, fmt.Errorf("engine %s does not support %q",
			q.engine.Name(), req.Kind)
	}

	// The layout owns where files go, so it is applied here rather than trusted
	// from the stored parameters.
	dir, err := q.layout.EnsureJobDir(j.ID)
	if err != nil {
		return engine.Request{}, err
	}
	req.OutDir = dir
	return req, nil
}

// persistTracks records the audio a job produced.
//
// A track row is only written once its file exists, so the library never lists
// a track it cannot serve.
func (q *Queue) persistTracks(ctx context.Context, j *job.Job, result engine.Result) error {
	if len(result.Tracks) == 0 {
		return fmt.Errorf("engine %s reported success for job %s but produced no tracks",
			q.engine.Name(), j.ID)
	}

	for _, produced := range result.Tracks {
		if err := q.persistTrack(ctx, j, produced, "", false); err != nil {
			return err
		}

		// Stems are sorted so that their IDs, and therefore the library order,
		// do not depend on Go's map iteration order.
		names := make([]string, 0, len(produced.Stems))
		for name := range produced.Stems {
			names = append(names, name)
		}
		slices.Sort(names)

		for _, name := range names {
			stem := engine.Track{
				Path:       produced.Stems[name],
				Format:     produced.Format,
				Duration:   produced.Duration,
				SampleRate: produced.SampleRate,
				Seed:       produced.Seed,
			}
			if err := q.persistTrack(ctx, j, stem, name, true); err != nil {
				return err
			}
		}
	}
	return nil
}

// persistTrack writes one track row, filling in what only the filesystem knows.
func (q *Queue) persistTrack(ctx context.Context, j *job.Job, produced engine.Track, stemName string, isStem bool) error {
	info, err := os.Stat(produced.Path)
	if err != nil {
		return fmt.Errorf("job %s produced %s but it cannot be read: %w", j.ID, produced.Path, err)
	}

	title := j.Title
	if isStem {
		title = fmt.Sprintf("%s · %s", j.Title, stemName)
	}

	track := &job.Track{
		ID:         job.NewID(),
		JobID:      j.ID,
		Title:      title,
		Path:       produced.Path,
		Format:     produced.Format,
		Duration:   produced.Duration,
		SizeBytes:  info.Size(),
		SampleRate: produced.SampleRate,
		Seed:       produced.Seed,
		IsStem:     isStem,
		StemName:   stemName,
		CreatedAt:  time.Now(),
	}
	return q.tracks.CreateTrack(ctx, track)
}

// finish records a job's terminal state and announces it.
func (q *Queue) finish(ctx context.Context, log *slog.Logger, id job.ID, f job.Finish) {
	// The bookkeeping runs detached from cancellation: a cancelled or
	// interrupted job still has to be recorded as such, and using the cancelled
	// context would lose exactly the outcome that matters most.
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finalizeTimeout)
	defer cancel()

	if err := q.store.Finish(writeCtx, id, f); err != nil {
		log.Error("recording the job outcome failed", "state", f.State, "error", err)
	}

	q.publish(Event{
		JobID:    id,
		State:    f.State,
		Phase:    string(engine.PhaseDone),
		Fraction: terminalFraction(f.State),
		Error:    f.Error,
		At:       time.Now(),
	})
}

// terminalFraction reports how far a job got, so a failed or cancelled job does
// not leave a progress bar that looks like it is still working.
func terminalFraction(state job.State) float64 {
	if state == job.StateSucceeded {
		return 1
	}
	return 0
}

func (q *Queue) publish(e Event) { q.hub.Publish(e) }

// wake signals a waiting worker without blocking. A pending wake-up already in
// the channel makes this a no-op, which is correct: one wake-up drains the
// whole queue.
func (q *Queue) wake() {
	select {
	case q.notify <- struct{}{}:
	default:
	}
}

// progressWriter forwards engine progress to the hub and, on a throttle, to the
// database.
type progressWriter struct {
	store   job.JobStore
	hub     *Hub
	jobID   job.ID
	baseCtx context.Context
	log     *slog.Logger

	lastFlush time.Time
	lastPhase string
}

// report implements engine.ProgressFunc.
func (w *progressWriter) report(p engine.Progress) {
	at := p.At
	if at.IsZero() {
		at = time.Now()
	}

	w.hub.Publish(Event{
		JobID:    w.jobID,
		State:    job.StateRunning,
		Phase:    string(p.Phase),
		Fraction: p.Fraction,
		Message:  p.Message,
		At:       at,
	})

	// A phase change is always persisted, so no phase is lost between flushes;
	// between them the writes are throttled to keep a 10 Hz progress stream off
	// the disk.
	phase := string(p.Phase)
	phaseChanged := phase != w.lastPhase
	if !phaseChanged && time.Since(w.lastFlush) < progressFlushInterval {
		return
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(w.baseCtx), progressFlushInterval)
	defer cancel()

	err := w.store.SaveProgress(ctx, job.Update{
		JobID:    w.jobID,
		State:    job.StateRunning,
		Phase:    phase,
		Fraction: p.Fraction,
		Message:  p.Message,
		At:       at,
	})
	if err != nil {
		// Progress is not worth failing a job over: the hub has already told
		// every live subscriber, and the terminal write will correct the row.
		w.log.Warn("persisting progress failed", "phase", phase, "error", err)
		return
	}

	w.lastPhase = phase
	w.lastFlush = time.Now()
}
