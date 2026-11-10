package sqlite

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/shaolianbo/jonus/internal/job"
)

// newTestDB opens a migrated database inside a directory whose name contains a
// space and parentheses — the shape of macOS's "Application Support", and the
// case the URL-based DSN has to survive.
//
// It returns the store plus the raw handle and path, for assertions that are
// about the connection rather than about the store.
func newTestDB(t *testing.T) (*Store, *sqlx.DB, string) {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "Jonus Test (data)")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create data dir: %v", err)
	}
	path := filepath.Join(dir, "jonus.db")

	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	return NewStore(db), db, path
}

func TestOpenAppliesPragmas(t *testing.T) {
	_, db, _ := newTestDB(t)
	ctx := context.Background()

	// WAL is the whole reason for the URL-encoded _pragma parameters: if the
	// driver failed to decode them, this silently stays "delete" and the
	// single-connection assumption becomes a correctness problem.
	var journalMode string
	if err := db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("journal_mode = %q, want %q", journalMode, "wal")
	}

	var foreignKeys int
	if err := db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatalf("read foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Errorf("foreign_keys = %d, want 1", foreignKeys)
	}

	var busyTimeout int
	if err := db.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		t.Fatalf("read busy_timeout: %v", err)
	}
	if busyTimeout != 5000 {
		t.Errorf("busy_timeout = %d, want 5000", busyTimeout)
	}
}

func TestOpenMigratesSchema(t *testing.T) {
	_, db, _ := newTestDB(t)

	for _, table := range []string{"jobs", "tracks", "job_events", "settings"} {
		var name string
		err := db.QueryRowContext(context.Background(),
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if err != nil {
			t.Errorf("table %s missing: %v", table, err)
		}
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	_, _, path := newTestDB(t)

	// Re-opening an already-migrated database must not fail: goose records the
	// applied version, and `serve` reopens the same file on every start.
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("reopen database: %v", err)
	}
	defer db.Close()
}

func TestJobRoundTrip(t *testing.T) {
	store, _, _ := newTestDB(t)
	ctx := context.Background()

	started := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	finished := started.Add(90 * time.Second)
	want := &job.Job{
		ID:           "01JOBROUNDTRIP00000000001",
		Kind:         "text2music",
		State:        job.StateSucceeded,
		Engine:       "mock",
		Title:        "晨光",
		Prompt:       "轻快的民谣，木吉他",
		Lyrics:       "[Verse]\n走过清晨的街",
		Instrumental: false,
		Duration:     95 * time.Second,
		Language:     "zh",
		Seed:         424242,
		ParamsJSON:   `{"kind":"text2music"}`,
		EngineTaskID: "task-7",
		Phase:        "done",
		Progress:     1,
		Priority:     3,
		Attempt:      2,
		CreatedAt:    started.Add(-time.Minute),
		StartedAt:    &started,
		FinishedAt:   &finished,
	}
	if err := store.Create(ctx, want); err != nil {
		t.Fatalf("create job: %v", err)
	}

	got, err := store.Get(ctx, want.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}

	if got.ID != want.ID || got.State != want.State || got.Engine != want.Engine {
		t.Errorf("identity fields differ: got %+v", got)
	}
	if got.Title != want.Title || got.Prompt != want.Prompt || got.Lyrics != want.Lyrics {
		t.Errorf("text fields differ: got title=%q prompt=%q lyrics=%q", got.Title, got.Prompt, got.Lyrics)
	}
	if got.Duration != want.Duration {
		t.Errorf("duration = %v, want %v", got.Duration, want.Duration)
	}
	if got.Seed != want.Seed {
		t.Errorf("seed = %d, want %d", got.Seed, want.Seed)
	}
	if got.ParamsJSON != want.ParamsJSON {
		t.Errorf("params_json = %q, want %q", got.ParamsJSON, want.ParamsJSON)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) {
		t.Errorf("created_at = %v, want %v", got.CreatedAt, want.CreatedAt)
	}
	if got.StartedAt == nil || !got.StartedAt.Equal(started) {
		t.Errorf("started_at = %v, want %v", got.StartedAt, started)
	}
	if got.FinishedAt == nil || !got.FinishedAt.Equal(finished) {
		t.Errorf("finished_at = %v, want %v", got.FinishedAt, finished)
	}
}

func TestGetMissingJobIsNotFound(t *testing.T) {
	store, _, _ := newTestDB(t)

	_, err := store.Get(context.Background(), "01NOSUCHJOB00000000000000")
	if !errors.Is(err, job.ErrNotFound) {
		t.Fatalf("err = %v, want job.ErrNotFound", err)
	}
}

func TestClaimNextTakesHighestPriorityThenOldest(t *testing.T) {
	store, _, _ := newTestDB(t)
	ctx := context.Background()

	base := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	mk := func(id string, priority int, age time.Duration) *job.Job {
		j := newQueuedJob(id, base.Add(-age))
		j.Priority = priority
		if err := store.Create(ctx, j); err != nil {
			t.Fatalf("create job %s: %v", id, err)
		}
		return j
	}

	mk("01OLDEST000000000000000001", 0, 10*time.Minute)
	mk("01URGENT000000000000000002", 5, time.Minute)
	mk("01NEWER000000000000000003", 0, time.Minute)

	got, err := store.ClaimNext(ctx)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if got.ID != "01URGENT000000000000000002" {
		t.Fatalf("claimed %s, want the high-priority job", got.ID)
	}
	if got.State != job.StateRunning {
		t.Errorf("state = %s, want running", got.State)
	}
	if got.Attempt != 1 {
		t.Errorf("attempt = %d, want 1", got.Attempt)
	}
	if got.StartedAt == nil {
		t.Error("started_at was not set on claim")
	}

	// Among equal priorities the older job goes first.
	got, err = store.ClaimNext(ctx)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if got.ID != "01OLDEST000000000000000001" {
		t.Fatalf("claimed %s, want the older job", got.ID)
	}
}

func TestClaimNextIsAtomic(t *testing.T) {
	store, _, _ := newTestDB(t)
	ctx := context.Background()

	if err := store.Create(ctx, newQueuedJob("01ONLYONE00000000000000001", time.Now())); err != nil {
		t.Fatalf("create job: %v", err)
	}

	// Ten concurrent claims against one pending job: exactly one must win,
	// because the claim is a single UPDATE ... RETURNING statement.
	const claims = 10
	results := make(chan error, claims)
	for range claims {
		go func() {
			_, err := store.ClaimNext(ctx)
			results <- err
		}()
	}

	var won, lost int
	for range claims {
		if err := <-results; err != nil {
			if !errors.Is(err, job.ErrNotFound) {
				t.Errorf("unexpected claim error: %v", err)
			}
			lost++
			continue
		}
		won++
	}
	if won != 1 {
		t.Errorf("%d claims succeeded, want exactly 1", won)
	}
	if lost != claims-1 {
		t.Errorf("%d claims found nothing, want %d", lost, claims-1)
	}
}

func TestClaimNextOnEmptyQueue(t *testing.T) {
	store, _, _ := newTestDB(t)

	_, err := store.ClaimNext(context.Background())
	if !errors.Is(err, job.ErrNotFound) {
		t.Fatalf("err = %v, want job.ErrNotFound", err)
	}
}

func TestSaveProgressOnlyAffectsRunningJobs(t *testing.T) {
	store, _, _ := newTestDB(t)
	ctx := context.Background()

	j := newQueuedJob("01PROGRESS0000000000000001", time.Now())
	if err := store.Create(ctx, j); err != nil {
		t.Fatalf("create job: %v", err)
	}

	// A pending job ignores progress: nothing is running yet.
	if err := store.SaveProgress(ctx, job.Update{JobID: j.ID, Phase: "generating", Fraction: 0.5}); err != nil {
		t.Fatalf("save progress: %v", err)
	}
	got, err := store.Get(ctx, j.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if got.Progress != 0 {
		t.Errorf("progress = %v, want 0 for a pending job", got.Progress)
	}

	if _, err := store.ClaimNext(ctx); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := store.SaveProgress(ctx, job.Update{JobID: j.ID, Phase: "generating", Fraction: 0.5}); err != nil {
		t.Fatalf("save progress: %v", err)
	}

	got, err = store.Get(ctx, j.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if got.Progress != 0.5 || got.Phase != "generating" {
		t.Errorf("progress = %v phase = %q, want 0.5 / generating", got.Progress, got.Phase)
	}
}

func TestFinishRecordsTerminalState(t *testing.T) {
	store, _, _ := newTestDB(t)
	ctx := context.Background()

	j := newQueuedJob("01FINISH000000000000000001", time.Now())
	if err := store.Create(ctx, j); err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := store.ClaimNext(ctx); err != nil {
		t.Fatalf("claim: %v", err)
	}

	finish := job.Finish{State: job.StateFailed, Error: "engine exploded"}
	if err := store.Finish(ctx, j.ID, finish); err != nil {
		t.Fatalf("finish: %v", err)
	}

	got, err := store.Get(ctx, j.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if got.State != job.StateFailed {
		t.Errorf("state = %s, want failed", got.State)
	}
	if got.Error != "engine exploded" {
		t.Errorf("error = %q, want the recorded message", got.Error)
	}
	if got.FinishedAt == nil {
		t.Error("finished_at was not set")
	}
}

func TestFinishRejectsNonTerminalState(t *testing.T) {
	store, _, _ := newTestDB(t)
	ctx := context.Background()

	j := newQueuedJob("01BADFINISH000000000000001", time.Now())
	if err := store.Create(ctx, j); err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := store.ClaimNext(ctx); err != nil {
		t.Fatalf("claim: %v", err)
	}

	err := store.Finish(ctx, j.ID, job.Finish{State: job.StateRunning})
	if err == nil {
		t.Fatal("finishing into a non-terminal state was accepted")
	}
}

func TestCancelPendingJob(t *testing.T) {
	store, _, _ := newTestDB(t)
	ctx := context.Background()

	j := newQueuedJob("01CANCELPEND00000000000001", time.Now())
	if err := store.Create(ctx, j); err != nil {
		t.Fatalf("create job: %v", err)
	}

	if err := store.RequestCancel(ctx, j.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	got, err := store.Get(ctx, j.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if got.State != job.StateCanceled {
		t.Errorf("state = %s, want canceled", got.State)
	}
	if got.FinishedAt == nil {
		t.Error("finished_at was not set")
	}

	// A canceled job is no longer claimable.
	if _, err := store.ClaimNext(ctx); !errors.Is(err, job.ErrNotFound) {
		t.Errorf("claim after cancel: err = %v, want ErrNotFound", err)
	}
}

func TestCancelRunningJobLeavesItToItsWorker(t *testing.T) {
	store, _, _ := newTestDB(t)
	ctx := context.Background()

	j := newQueuedJob("01CANCELRUN000000000000001", time.Now())
	if err := store.Create(ctx, j); err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := store.ClaimNext(ctx); err != nil {
		t.Fatalf("claim: %v", err)
	}

	// The worker owns the context that stops the engine, so a running job is
	// not cancelled behind its back; the worker reports the outcome.
	if err := store.RequestCancel(ctx, j.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	got, err := store.Get(ctx, j.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if got.State != job.StateRunning {
		t.Errorf("state = %s, want running (the worker decides)", got.State)
	}

	if err := store.Finish(ctx, j.ID, job.Finish{State: job.StateCanceled}); err != nil {
		t.Fatalf("finish: %v", err)
	}
	got, err = store.Get(ctx, j.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if got.State != job.StateCanceled {
		t.Errorf("state = %s, want canceled", got.State)
	}
}

func TestCancelTerminalJobIsRejected(t *testing.T) {
	store, _, _ := newTestDB(t)
	ctx := context.Background()

	j := newQueuedJob("01CANCELDONE000000000000001", time.Now())
	if err := store.Create(ctx, j); err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := store.ClaimNext(ctx); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := store.Finish(ctx, j.ID, job.Finish{State: job.StateSucceeded}); err != nil {
		t.Fatalf("finish: %v", err)
	}

	if err := store.RequestCancel(ctx, j.ID); !errors.Is(err, job.ErrInvalidTransition) {
		t.Errorf("err = %v, want job.ErrInvalidTransition", err)
	}
}

func TestRecoverInterrupted(t *testing.T) {
	store, _, _ := newTestDB(t)
	ctx := context.Background()

	base := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	for i, id := range []string{
		"01RECOVER00000000000000001",
		"01RECOVER00000000000000002",
		"01RECOVER00000000000000003",
	} {
		if err := store.Create(ctx, newQueuedJob(id, base.Add(time.Duration(i)*time.Second))); err != nil {
			t.Fatalf("create job %s: %v", id, err)
		}
	}

	// Two jobs were mid-flight when the process died; one never started.
	if _, err := store.ClaimNext(ctx); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := store.ClaimNext(ctx); err != nil {
		t.Fatalf("claim: %v", err)
	}

	n, err := store.RecoverInterrupted(ctx)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if n != 2 {
		t.Errorf("recovered %d jobs, want 2", n)
	}

	// interrupted is terminal, so the untouched pending job is still pending.
	pending, err := store.List(ctx, job.Filter{States: []job.State{job.StatePending}})
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pending) != 1 {
		t.Errorf("%d pending jobs, want 1", len(pending))
	}

	interrupted, err := store.List(ctx, job.Filter{States: []job.State{job.StateInterrupted}})
	if err != nil {
		t.Fatalf("list interrupted: %v", err)
	}
	if len(interrupted) != 2 {
		t.Fatalf("%d interrupted jobs, want 2", len(interrupted))
	}
	for _, j := range interrupted {
		if j.Error == "" {
			t.Errorf("job %s has no explanation for being interrupted", j.ID)
		}
	}

	// Recovery is not repeatable: nothing is running any more.
	n, err = store.RecoverInterrupted(ctx)
	if err != nil {
		t.Fatalf("recover again: %v", err)
	}
	if n != 0 {
		t.Errorf("second recovery changed %d jobs, want 0", n)
	}
}

func TestListFiltersByStateAndPaginates(t *testing.T) {
	store, _, _ := newTestDB(t)
	ctx := context.Background()

	base := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	for i := range 5 {
		id := "01LIST0000000000000000000" + string(rune('a'+i))
		if err := store.Create(ctx, newQueuedJob(id, base.Add(time.Duration(i)*time.Minute))); err != nil {
			t.Fatalf("create job %s: %v", id, err)
		}
	}

	all, err := store.List(ctx, job.Filter{})
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 5 {
		t.Fatalf("%d jobs, want 5", len(all))
	}
	// Newest first.
	if all[0].CreatedAt.Before(all[4].CreatedAt) {
		t.Error("jobs are not ordered newest first")
	}

	page, err := store.List(ctx, job.Filter{Limit: 2, Offset: 1})
	if err != nil {
		t.Fatalf("list page: %v", err)
	}
	if len(page) != 2 {
		t.Fatalf("%d jobs in page, want 2", len(page))
	}
	if page[0].ID != all[1].ID || page[1].ID != all[2].ID {
		t.Error("pagination did not return the expected slice")
	}

	running, err := store.List(ctx, job.Filter{States: []job.State{job.StateRunning}})
	if err != nil {
		t.Fatalf("list running: %v", err)
	}
	if len(running) != 0 {
		t.Errorf("%d running jobs, want 0", len(running))
	}
}

func TestDeleteJobCascadesToTracks(t *testing.T) {
	store, db, _ := newTestDB(t)
	ctx := context.Background()

	j := newQueuedJob("01CASCADE00000000000000001", time.Now())
	if err := store.Create(ctx, j); err != nil {
		t.Fatalf("create job: %v", err)
	}
	track := &job.Track{
		ID:        "01TRACK000000000000000001",
		JobID:     j.ID,
		Title:     "晨光",
		Path:      "/tmp/晨光.wav",
		Format:    "wav",
		Duration:  95 * time.Second,
		SizeBytes: 16_777_244,
		CreatedAt: time.Now(),
	}
	if err := store.CreateTrack(ctx, track); err != nil {
		t.Fatalf("create track: %v", err)
	}
	if err := store.AppendEvent(ctx, j.ID, job.Event{
		At: time.Now(), Kind: "state", Payload: `{"state":"pending"}`,
	}); err != nil {
		t.Fatalf("append event: %v", err)
	}

	if err := store.Delete(ctx, j.ID); err != nil {
		t.Fatalf("delete job: %v", err)
	}

	if _, err := store.GetTrack(ctx, track.ID); !errors.Is(err, job.ErrNotFound) {
		t.Errorf("track survived the job delete: err = %v", err)
	}
	var events int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM job_events WHERE job_id = ?`, j.ID).Scan(&events); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if events != 0 {
		t.Errorf("%d events survived the job delete, want 0", events)
	}
}

func TestTrackRoundTripAndPeaksPath(t *testing.T) {
	store, _, _ := newTestDB(t)
	ctx := context.Background()

	j := newQueuedJob("01TRACKJOB0000000000000001", time.Now())
	if err := store.Create(ctx, j); err != nil {
		t.Fatalf("create job: %v", err)
	}

	stem := &job.Track{
		ID:         "01STEM0000000000000000001",
		JobID:      j.ID,
		Title:      "人声",
		Path:       "/tmp/vocals.wav",
		Format:     "wav",
		Duration:   95 * time.Second,
		SizeBytes:  4096,
		SampleRate: 44100,
		Seed:       99,
		IsStem:     true,
		StemName:   "vocals",
		CreatedAt:  time.Now(),
	}
	if err := store.CreateTrack(ctx, stem); err != nil {
		t.Fatalf("create track: %v", err)
	}

	got, err := store.GetTrack(ctx, stem.ID)
	if err != nil {
		t.Fatalf("get track: %v", err)
	}
	if !got.IsStem || got.StemName != "vocals" {
		t.Errorf("stem fields lost: is_stem=%v stem_name=%q", got.IsStem, got.StemName)
	}
	if got.SampleRate != 44100 || got.Seed != 99 {
		t.Errorf("sample_rate=%d seed=%d, want 44100 / 99", got.SampleRate, got.Seed)
	}
	if got.SizeBytes != 4096 {
		t.Errorf("size_bytes = %d, want 4096", got.SizeBytes)
	}
	if got.PeaksPath != "" {
		t.Errorf("peaks_path = %q, want empty before it is computed", got.PeaksPath)
	}

	if err := store.SetPeaksPath(ctx, stem.ID, "/tmp/vocals.peaks.json"); err != nil {
		t.Fatalf("set peaks path: %v", err)
	}
	got, err = store.GetTrack(ctx, stem.ID)
	if err != nil {
		t.Fatalf("get track: %v", err)
	}
	if got.PeaksPath != "/tmp/vocals.peaks.json" {
		t.Errorf("peaks_path = %q, want the stored path", got.PeaksPath)
	}

	tracks, err := store.ListTracks(ctx, job.TrackFilter{JobID: j.ID})
	if err != nil {
		t.Fatalf("list tracks: %v", err)
	}
	if len(tracks) != 1 || tracks[0].ID != stem.ID {
		t.Errorf("list tracks returned %d rows, want the one stem", len(tracks))
	}
}

func TestDeleteTrackKeepsJob(t *testing.T) {
	store, _, _ := newTestDB(t)
	ctx := context.Background()

	j := newQueuedJob("01KEEPJOB00000000000000001", time.Now())
	if err := store.Create(ctx, j); err != nil {
		t.Fatalf("create job: %v", err)
	}
	track := &job.Track{
		ID: "01GONETRACK000000000000001", JobID: j.ID,
		Path: "/tmp/a.wav", Format: "wav", CreatedAt: time.Now(),
	}
	if err := store.CreateTrack(ctx, track); err != nil {
		t.Fatalf("create track: %v", err)
	}

	if err := store.DeleteTrack(ctx, track.ID); err != nil {
		t.Fatalf("delete track: %v", err)
	}
	if _, err := store.Get(ctx, j.ID); err != nil {
		t.Errorf("job was removed along with its track: %v", err)
	}
	if err := store.DeleteTrack(ctx, track.ID); !errors.Is(err, job.ErrNotFound) {
		t.Errorf("second delete: err = %v, want ErrNotFound", err)
	}
}

// newQueuedJob builds a minimal pending job with a fixed kind and engine.
func newQueuedJob(id string, createdAt time.Time) *job.Job {
	return &job.Job{
		ID:         job.ID(id),
		Kind:       "text2music",
		State:      job.StatePending,
		Engine:     "mock",
		Prompt:     "test",
		Duration:   60 * time.Second,
		ParamsJSON: "{}",
		CreatedAt:  createdAt,
	}
}
