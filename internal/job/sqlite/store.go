package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/Bo101010/jonus/internal/job"
)

// Store is the SQLite implementation of job.JobStore and job.TrackStore.
type Store struct {
	db *sqlx.DB
}

// Compile-time proof that Store satisfies both ports.
var (
	_ job.JobStore   = (*Store)(nil)
	_ job.TrackStore = (*Store)(nil)
)

// NewStore wraps an open database handle.
func NewStore(db *sqlx.DB) *Store {
	return &Store{db: db}
}

// jobColumns is the column list shared by every job query, kept in one place
// so the scan order and the SELECT never drift apart.
const jobColumns = `id, kind, state, engine, title, prompt, lyrics, instrumental,
	duration_ms, language, seed, params_json, engine_task_id, phase, progress,
	error, priority, attempt, created_at, started_at, finished_at`

// jobRow mirrors the jobs table for scanning.
type jobRow struct {
	ID           string  `db:"id"`
	Kind         string  `db:"kind"`
	State        string  `db:"state"`
	Engine       string  `db:"engine"`
	Title        string  `db:"title"`
	Prompt       string  `db:"prompt"`
	Lyrics       string  `db:"lyrics"`
	Instrumental bool    `db:"instrumental"`
	DurationMS   int64   `db:"duration_ms"`
	Language     string  `db:"language"`
	Seed         int64   `db:"seed"`
	ParamsJSON   string  `db:"params_json"`
	EngineTaskID string  `db:"engine_task_id"`
	Phase        string  `db:"phase"`
	Progress     float64 `db:"progress"`
	Error        string  `db:"error"`
	Priority     int     `db:"priority"`
	Attempt      int     `db:"attempt"`
	CreatedAt    int64   `db:"created_at"`
	StartedAt    *int64  `db:"started_at"`
	FinishedAt   *int64  `db:"finished_at"`
}

func (r jobRow) toDomain() *job.Job {
	return &job.Job{
		ID:           job.ID(r.ID),
		Kind:         r.Kind,
		State:        job.State(r.State),
		Engine:       r.Engine,
		Title:        r.Title,
		Prompt:       r.Prompt,
		Lyrics:       r.Lyrics,
		Instrumental: r.Instrumental,
		Duration:     time.Duration(r.DurationMS) * time.Millisecond,
		Language:     r.Language,
		Seed:         r.Seed,
		ParamsJSON:   r.ParamsJSON,
		EngineTaskID: r.EngineTaskID,
		Phase:        r.Phase,
		Progress:     r.Progress,
		Error:        r.Error,
		Priority:     r.Priority,
		Attempt:      r.Attempt,
		CreatedAt:    fromMillis(r.CreatedAt),
		StartedAt:    fromMillisPtr(r.StartedAt),
		FinishedAt:   fromMillisPtr(r.FinishedAt),
	}
}

// Create implements job.JobStore.
func (s *Store) Create(ctx context.Context, j *job.Job) error {
	const q = `INSERT INTO jobs (` + jobColumns + `)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	_, err := s.db.ExecContext(ctx, q,
		j.ID, j.Kind, j.State, j.Engine, j.Title, j.Prompt, j.Lyrics, j.Instrumental,
		j.Duration.Milliseconds(), j.Language, j.Seed, j.ParamsJSON, j.EngineTaskID,
		j.Phase, j.Progress, j.Error, j.Priority, j.Attempt,
		toMillis(j.CreatedAt), toMillisPtr(j.StartedAt), toMillisPtr(j.FinishedAt),
	)
	if err != nil {
		return fmt.Errorf("insert job %s: %w", j.ID, err)
	}
	return nil
}

// Get implements job.JobStore.
func (s *Store) Get(ctx context.Context, id job.ID) (*job.Job, error) {
	var row jobRow
	err := s.db.GetContext(ctx, &row, `SELECT `+jobColumns+` FROM jobs WHERE id = ?`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("job %s: %w", id, job.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("select job %s: %w", id, err)
	}
	return row.toDomain(), nil
}

// List implements job.JobStore.
func (s *Store) List(ctx context.Context, f job.Filter) ([]*job.Job, error) {
	q := `SELECT ` + jobColumns + ` FROM jobs`
	var args []any

	if len(f.States) > 0 {
		states := make([]string, len(f.States))
		for i, st := range f.States {
			states[i] = string(st)
		}
		query, inArgs, err := sqlx.In(`state IN (?)`, states)
		if err != nil {
			return nil, fmt.Errorf("build state filter: %w", err)
		}
		q += ` WHERE ` + query
		args = append(args, inArgs...)
	}
	q += ` ORDER BY created_at DESC`
	q += limitClause(f.Limit, f.Offset)
	args = append(args, limitArgs(f.Limit, f.Offset)...)

	var rows []jobRow
	if err := s.db.SelectContext(ctx, &rows, s.db.Rebind(q), args...); err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	return toJobs(rows), nil
}

// ClaimNext implements job.JobStore.
func (s *Store) ClaimNext(ctx context.Context) (*job.Job, error) {
	const q = `
		UPDATE jobs
		   SET state = 'running', started_at = ?, attempt = attempt + 1
		 WHERE id = (
		       SELECT id FROM jobs
		        WHERE state = 'pending'
		        ORDER BY priority DESC, created_at ASC
		        LIMIT 1)
		RETURNING ` + jobColumns

	var row jobRow
	err := s.db.GetContext(ctx, &row, q, toMillis(time.Now()))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, job.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("claim next job: %w", err)
	}
	return row.toDomain(), nil
}

// SaveProgress implements job.JobStore.
func (s *Store) SaveProgress(ctx context.Context, u job.Update) error {
	const q = `UPDATE jobs SET phase = ?, progress = ? WHERE id = ? AND state = 'running'`

	res, err := s.db.ExecContext(ctx, q, u.Phase, u.Fraction, u.JobID)
	if err != nil {
		return fmt.Errorf("save progress for job %s: %w", u.JobID, err)
	}
	// A job that is no longer running has been canceled or finished; its
	// progress is no longer meaningful, which is not an error.
	if _, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("save progress for job %s: %w", u.JobID, err)
	}
	return nil
}

// Finish implements job.JobStore.
func (s *Store) Finish(ctx context.Context, id job.ID, f job.Finish) error {
	if err := f.Validate(); err != nil {
		return err
	}

	const q = `UPDATE jobs SET state = ?, error = ?, finished_at = ?
		WHERE id = ? AND state = 'running'`

	res, err := s.db.ExecContext(ctx, q, f.State, f.Error, toMillis(time.Now()), id)
	if err != nil {
		return fmt.Errorf("finish job %s: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("finish job %s: %w", id, err)
	}
	if affected == 0 {
		// Either the job does not exist, or it was already taken out of
		// running by a cancel. Distinguish the two for the caller.
		if _, err := s.Get(ctx, id); err != nil {
			return err
		}
		return nil
	}
	return nil
}

// RequestCancel implements job.JobStore.
//
// A pending job is canceled outright. A running job is left for its worker,
// which owns the context that stops the engine; the caller cancels that
// in memory and the worker then finishes the job.
func (s *Store) RequestCancel(ctx context.Context, id job.ID) error {
	const q = `UPDATE jobs SET state = 'canceled', finished_at = ?
		WHERE id = ? AND state = 'pending'`

	res, err := s.db.ExecContext(ctx, q, toMillis(time.Now()), id)
	if err != nil {
		return fmt.Errorf("cancel job %s: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("cancel job %s: %w", id, err)
	}
	if affected > 0 {
		return nil
	}

	// Nothing changed: the job is either already running (the caller handles
	// it) or it does not exist.
	current, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if current.State == job.StateRunning {
		return nil
	}
	return fmt.Errorf("job %s is %s and cannot be canceled: %w",
		id, current.State, job.ErrInvalidTransition)
}

// RecoverInterrupted implements job.JobStore.
func (s *Store) RecoverInterrupted(ctx context.Context) (int, error) {
	const q = `UPDATE jobs
		   SET state = 'interrupted',
		       error = 'interrupted by restart',
		       finished_at = ?
		 WHERE state = 'running'`

	res, err := s.db.ExecContext(ctx, q, toMillis(time.Now()))
	if err != nil {
		return 0, fmt.Errorf("recover interrupted jobs: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("recover interrupted jobs: %w", err)
	}
	return int(affected), nil
}

// Delete implements job.JobStore.
func (s *Store) Delete(ctx context.Context, id job.ID) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM jobs WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete job %s: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete job %s: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("job %s: %w", id, job.ErrNotFound)
	}
	return nil
}

// AppendEvent implements job.JobStore.
func (s *Store) AppendEvent(ctx context.Context, id job.ID, e job.Event) error {
	const q = `INSERT INTO job_events (job_id, at, kind, payload) VALUES (?, ?, ?, ?)`

	if _, err := s.db.ExecContext(ctx, q, id, toMillis(e.At), e.Kind, e.Payload); err != nil {
		return fmt.Errorf("append event to job %s: %w", id, err)
	}
	return nil
}

// trackColumns mirrors jobColumns for the tracks table.
const trackColumns = `id, job_id, title, path, format, duration_ms, size_bytes,
	sample_rate, seed, is_stem, stem_name, peaks_path, created_at`

type trackRow struct {
	ID         string `db:"id"`
	JobID      string `db:"job_id"`
	Title      string `db:"title"`
	Path       string `db:"path"`
	Format     string `db:"format"`
	DurationMS int64  `db:"duration_ms"`
	SizeBytes  int64  `db:"size_bytes"`
	SampleRate int    `db:"sample_rate"`
	Seed       int64  `db:"seed"`
	IsStem     bool   `db:"is_stem"`
	StemName   string `db:"stem_name"`
	PeaksPath  string `db:"peaks_path"`
	CreatedAt  int64  `db:"created_at"`
}

func (r trackRow) toDomain() *job.Track {
	return &job.Track{
		ID:         job.ID(r.ID),
		JobID:      job.ID(r.JobID),
		Title:      r.Title,
		Path:       r.Path,
		Format:     r.Format,
		Duration:   time.Duration(r.DurationMS) * time.Millisecond,
		SizeBytes:  r.SizeBytes,
		SampleRate: r.SampleRate,
		Seed:       r.Seed,
		IsStem:     r.IsStem,
		StemName:   r.StemName,
		PeaksPath:  r.PeaksPath,
		CreatedAt:  fromMillis(r.CreatedAt),
	}
}

// CreateTrack implements job.TrackStore.
func (s *Store) CreateTrack(ctx context.Context, t *job.Track) error {
	const q = `INSERT INTO tracks (` + trackColumns + `)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	_, err := s.db.ExecContext(ctx, q,
		t.ID, t.JobID, t.Title, t.Path, t.Format, t.Duration.Milliseconds(),
		t.SizeBytes, t.SampleRate, t.Seed, t.IsStem, t.StemName, t.PeaksPath,
		toMillis(t.CreatedAt),
	)
	if err != nil {
		return fmt.Errorf("insert track %s: %w", t.ID, err)
	}
	return nil
}

// Get implements job.TrackStore.
func (s *Store) GetTrack(ctx context.Context, id job.ID) (*job.Track, error) {
	var row trackRow
	err := s.db.GetContext(ctx, &row, `SELECT `+trackColumns+` FROM tracks WHERE id = ?`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("track %s: %w", id, job.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("select track %s: %w", id, err)
	}
	return row.toDomain(), nil
}

// ListTracks implements job.TrackStore.
func (s *Store) ListTracks(ctx context.Context, f job.TrackFilter) ([]*job.Track, error) {
	q := `SELECT ` + trackColumns + ` FROM tracks`
	var (
		args       []any
		conditions []string
	)

	if f.JobID != "" {
		conditions = append(conditions, `job_id = ?`)
		args = append(args, f.JobID)
	}
	if f.ExcludeStems {
		conditions = append(conditions, `is_stem = 0`)
	}
	if len(conditions) > 0 {
		q += ` WHERE ` + strings.Join(conditions, ` AND `)
	}

	q += ` ORDER BY created_at DESC`
	q += limitClause(f.Limit, f.Offset)
	args = append(args, limitArgs(f.Limit, f.Offset)...)

	var rows []trackRow
	if err := s.db.SelectContext(ctx, &rows, q, args...); err != nil {
		return nil, fmt.Errorf("list tracks: %w", err)
	}

	tracks := make([]*job.Track, len(rows))
	for i, r := range rows {
		tracks[i] = r.toDomain()
	}
	return tracks, nil
}

// DeleteTrack implements job.TrackStore.
func (s *Store) DeleteTrack(ctx context.Context, id job.ID) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM tracks WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete track %s: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete track %s: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("track %s: %w", id, job.ErrNotFound)
	}
	return nil
}

// SetPeaksPath implements job.TrackStore.
func (s *Store) SetPeaksPath(ctx context.Context, id job.ID, path string) error {
	const q = `UPDATE tracks SET peaks_path = ? WHERE id = ?`

	if _, err := s.db.ExecContext(ctx, q, path, id); err != nil {
		return fmt.Errorf("set peaks path for track %s: %w", id, err)
	}
	return nil
}

func toJobs(rows []jobRow) []*job.Job {
	jobs := make([]*job.Job, len(rows))
	for i, r := range rows {
		jobs[i] = r.toDomain()
	}
	return jobs
}

// limitClause renders a LIMIT/OFFSET clause, omitting it entirely when no
// limit was requested. The values are placeholders, never interpolated.
func limitClause(limit, offset int) string {
	switch {
	case limit > 0 && offset > 0:
		return ` LIMIT ? OFFSET ?`
	case limit > 0:
		return ` LIMIT ?`
	default:
		return ``
	}
}

func limitArgs(limit, offset int) []any {
	switch {
	case limit > 0 && offset > 0:
		return []any{limit, offset}
	case limit > 0:
		return []any{limit}
	default:
		return nil
	}
}

func toMillis(t time.Time) int64 { return t.UnixMilli() }

func toMillisPtr(t *time.Time) *int64 {
	if t == nil {
		return nil
	}
	ms := t.UnixMilli()
	return &ms
}

func fromMillis(ms int64) time.Time { return time.UnixMilli(ms) }

func fromMillisPtr(ms *int64) *time.Time {
	if ms == nil {
		return nil
	}
	t := time.UnixMilli(*ms)
	return &t
}
