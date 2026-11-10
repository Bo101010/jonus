package job

import "context"

// JobStore persists jobs.
//
// The port lives beside the domain type it operates on rather than in the
// SQLite adapter, so the queue and the application layer can depend on it
// without knowing how storage works.
type JobStore interface {
	// Create inserts a new job. The caller sets ID and CreatedAt.
	Create(ctx context.Context, j *Job) error

	// Get returns one job, or ErrNotFound.
	Get(ctx context.Context, id ID) (*Job, error)

	// List returns jobs matching f, newest first.
	List(ctx context.Context, f Filter) ([]*Job, error)

	// ClaimNext atomically moves the highest-priority pending job to running
	// and returns it. It returns ErrNotFound when the queue is empty.
	ClaimNext(ctx context.Context) (*Job, error)

	// SaveProgress records the latest progress report for a running job.
	SaveProgress(ctx context.Context, u Update) error

	// Finish moves a running job to a terminal state.
	Finish(ctx context.Context, id ID, f Finish) error

	// RequestCancel marks a pending job canceled, or asks a running job to
	// stop. It reports ErrNotFound when the job does not exist.
	RequestCancel(ctx context.Context, id ID) error

	// RecoverInterrupted marks every job left running by a previous process as
	// interrupted, and reports how many it changed.
	RecoverInterrupted(ctx context.Context) (int, error)

	// Delete removes a job and, by cascade, its tracks and events.
	Delete(ctx context.Context, id ID) error

	// AppendEvent adds an entry to a job's history.
	AppendEvent(ctx context.Context, id ID, e Event) error
}

// TrackStore persists the audio a job produces.
//
// Its methods carry the "Track" noun because one adapter implements both this
// port and JobStore, and Go does not allow a type to have two methods with the
// same name. JobStore, being the primary domain, keeps the bare names.
type TrackStore interface {
	// CreateTrack inserts a track. The caller sets ID and CreatedAt.
	CreateTrack(ctx context.Context, t *Track) error

	// GetTrack returns one track, or ErrNotFound.
	GetTrack(ctx context.Context, id ID) (*Track, error)

	// ListTracks returns tracks matching f, newest first.
	ListTracks(ctx context.Context, f TrackFilter) ([]*Track, error)

	// DeleteTrack removes one track record. Removing the file is the caller's job.
	DeleteTrack(ctx context.Context, id ID) error

	// SetPeaksPath records where a track's cached waveform summary lives.
	SetPeaksPath(ctx context.Context, id ID, path string) error
}
