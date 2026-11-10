// Package job holds the domain model for generation jobs and the tracks they
// produce, along with the persistence ports the rest of the application uses.
//
// The model is deliberately free of storage and engine details: it must not
// import the SQLite adapter or any engine implementation.
package job

import (
	"errors"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"
)

// ID identifies a job or a track. It is a ULID: lexicographically sortable by
// creation time, which lets the database order by primary key.
type ID string

// String implements fmt.Stringer.
func (id ID) String() string { return string(id) }

// State is the lifecycle position of a job.
type State string

const (
	// StatePending is queued and waiting for a worker.
	StatePending State = "pending"

	// StateRunning is currently being generated.
	StateRunning State = "running"

	// StateSucceeded finished and produced tracks.
	StateSucceeded State = "succeeded"

	// StateFailed finished with an error.
	StateFailed State = "failed"

	// StateCanceled was stopped at the user's request.
	StateCanceled State = "canceled"

	// StateInterrupted was running when the process exited. It is terminal
	// because the engine's work was lost with the process; the user can
	// retry it, which creates a fresh job from the stored parameters.
	StateInterrupted State = "interrupted"
)

// Terminal reports whether no further transition is possible without creating
// a new job.
func (s State) Terminal() bool {
	switch s {
	case StateSucceeded, StateFailed, StateCanceled, StateInterrupted:
		return true
	default:
		return false
	}
}

// Valid reports whether s is a known state.
func (s State) Valid() bool {
	switch s {
	case StatePending, StateRunning, StateSucceeded, StateFailed, StateCanceled, StateInterrupted:
		return true
	default:
		return false
	}
}

// CanTransition reports whether a job may move from one state to another.
//
// The legal edges are:
//
//	pending     -> running | canceled
//	running     -> succeeded | failed | canceled | interrupted
//	interrupted -> pending            (retry)
//
// Every terminal state is a dead end.
func CanTransition(from, to State) bool {
	switch from {
	case StatePending:
		return to == StateRunning || to == StateCanceled
	case StateRunning:
		return to == StateSucceeded || to == StateFailed ||
			to == StateCanceled || to == StateInterrupted
	case StateInterrupted:
		return to == StatePending
	default:
		return false
	}
}

// ErrNotFound is returned when a job or track does not exist.
var ErrNotFound = errors.New("not found")

// ErrInvalidTransition is returned when a state change is not allowed.
var ErrInvalidTransition = errors.New("invalid state transition")

// Job is one generation request and its outcome.
type Job struct {
	ID     ID
	Kind   string // engine.Kind, stored as a string to keep this package independent
	State  State
	Engine string
	Title  string

	// Prompt, Lyrics, and the remaining generation fields mirror the request
	// the engine was given. ParamsJSON is the authoritative copy: it holds
	// the whole request, so a job can be replayed exactly.
	Prompt       string
	Lyrics       string
	Instrumental bool
	Duration     time.Duration
	Language     string
	Seed         int64

	// ParamsJSON is the serialised engine request used to replay this job.
	ParamsJSON string

	// EngineTaskID is the engine-side identifier, when the engine has one.
	EngineTaskID string

	// Phase and Progress are the last persisted progress report.
	Phase    string
	Progress float64

	// Error is the failure message for StateFailed.
	Error string

	// Priority orders the queue; higher runs first. Attempt counts executions.
	Priority int
	Attempt  int

	CreatedAt  time.Time
	StartedAt  *time.Time
	FinishedAt *time.Time
}

// Track is one audio artifact produced by a job.
type Track struct {
	ID         ID
	JobID      ID
	Title      string
	Path       string
	Format     string
	Duration   time.Duration
	SizeBytes  int64
	SampleRate int
	Seed       int64

	// IsStem marks a separated stem rather than a standalone track.
	IsStem bool

	// StemName names the instrument when IsStem is set, for example "vocals".
	StemName string

	// PeaksPath caches the computed waveform summary, when one exists.
	PeaksPath string

	CreatedAt time.Time
}

// Update is a progress report flowing to subscribers. It is intentionally
// small: it crosses the boundary to the web layer and is broadcast on every
// tick, so it must not carry the whole job.
type Update struct {
	JobID    ID
	State    State
	Phase    string
	Fraction float64
	Message  string
	At       time.Time
}

// EventKind classifies an entry in a job's append-only event log.
type EventKind string

const (
	EventState    EventKind = "state"
	EventProgress EventKind = "progress"
	EventLog      EventKind = "log"
	EventError    EventKind = "error"
)

// Event is one entry in a job's history.
type Event struct {
	Kind    EventKind
	Payload string
	At      time.Time
}

// Filter narrows a job listing.
type Filter struct {
	// States limits the result to these states. Empty means all states.
	States []State
	Limit  int
	Offset int
}

// TrackFilter narrows a track listing.
type TrackFilter struct {
	// JobID limits the result to one job's tracks. Empty means all jobs.
	JobID ID

	// ExcludeStems drops separated stems, leaving whole songs.
	//
	// The library lists songs by default: stems are a working material derived
	// from a song, not something the user created in its own right, so showing
	// them beside it would triple the length of the list without adding
	// anything to browse.
	ExcludeStems bool

	Limit  int
	Offset int
}

// Finish describes how a running job ended.
type Finish struct {
	State State
	Error string
}

// Validate checks that f is a terminal state, which is the only thing a caller
// may finish a job with.
func (f Finish) Validate() error {
	if !f.State.Terminal() {
		return fmt.Errorf("finish state %q is not terminal", f.State)
	}
	return nil
}

// NewID returns a fresh identifier.
//
// ULIDs sort by creation time, so the primary key already orders jobs by age
// and the queue needs no separate sequence.
func NewID() ID {
	return ID(ulid.Make().String())
}

// Draft is the description of a job to create. It exists so that creating a job
// is one call with named fields rather than a long positional argument list,
// and so that ID, state, and creation time are set in exactly one place.
type Draft struct {
	Kind   string
	Engine string
	Title  string

	Prompt       string
	Lyrics       string
	Instrumental bool
	Duration     time.Duration
	Language     string
	Seed         int64

	// ParamsJSON is the serialised engine request, stored so the job can be
	// replayed without reconstructing it from the fields above.
	ParamsJSON string

	Priority int
}

// New builds a pending job from a draft, assigning it an ID and a creation
// time.
func New(d Draft) *Job {
	return &Job{
		ID:           NewID(),
		Kind:         d.Kind,
		State:        StatePending,
		Engine:       d.Engine,
		Title:        d.Title,
		Prompt:       d.Prompt,
		Lyrics:       d.Lyrics,
		Instrumental: d.Instrumental,
		Duration:     d.Duration,
		Language:     d.Language,
		Seed:         d.Seed,
		ParamsJSON:   d.ParamsJSON,
		Priority:     d.Priority,
		CreatedAt:    time.Now(),
	}
}

// Draft returns a description of j suitable for creating a copy of it.
//
// It is how retry works: the copy is a new job, so the original keeps its
// history and its failure.
func (j *Job) Draft() Draft {
	return Draft{
		Kind:         j.Kind,
		Engine:       j.Engine,
		Title:        j.Title,
		Prompt:       j.Prompt,
		Lyrics:       j.Lyrics,
		Instrumental: j.Instrumental,
		Duration:     j.Duration,
		Language:     j.Language,
		Seed:         j.Seed,
		ParamsJSON:   j.ParamsJSON,
		Priority:     j.Priority,
	}
}
