package httpapi

import (
	"time"

	"github.com/shaolianbo/jonus/internal/app"
	"github.com/shaolianbo/jonus/internal/engine"
	"github.com/shaolianbo/jonus/internal/job"
)

// The views below are the HTTP wire format. They are separate from the domain
// types on purpose:
//
//   - the domain carries time.Duration, which would serialise as a nanosecond
//     integer that no JavaScript client wants;
//   - job.ParamsJSON is an internal replay record and must not be on the wire;
//   - derived fields such as audio_url belong to the transport, not the model.
//
// Every exported field is tagged, so renaming a Go field can never silently
// change the API.

type jobView struct {
	ID           string     `json:"id"`
	Kind         string     `json:"kind"`
	State        string     `json:"state"`
	Engine       string     `json:"engine"`
	Title        string     `json:"title"`
	Prompt       string     `json:"prompt"`
	Lyrics       string     `json:"lyrics"`
	Instrumental bool       `json:"instrumental"`
	DurationMS   int64      `json:"duration_ms"`
	Language     string     `json:"language"`
	Seed         int64      `json:"seed"`
	Phase        string     `json:"phase"`
	Progress     float64    `json:"progress"`
	Error        string     `json:"error,omitempty"`
	Priority     int        `json:"priority"`
	Attempt      int        `json:"attempt"`
	CreatedAt    time.Time  `json:"created_at"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
}

func newJobView(j *job.Job) jobView {
	return jobView{
		ID:           j.ID.String(),
		Kind:         j.Kind,
		State:        string(j.State),
		Engine:       j.Engine,
		Title:        j.Title,
		Prompt:       j.Prompt,
		Lyrics:       j.Lyrics,
		Instrumental: j.Instrumental,
		DurationMS:   j.Duration.Milliseconds(),
		Language:     j.Language,
		Seed:         j.Seed,
		Phase:        j.Phase,
		Progress:     j.Progress,
		Error:        j.Error,
		Priority:     j.Priority,
		Attempt:      j.Attempt,
		CreatedAt:    j.CreatedAt,
		StartedAt:    j.StartedAt,
		FinishedAt:   j.FinishedAt,
	}
}

func newJobViews(jobs []*job.Job) []jobView {
	views := make([]jobView, len(jobs))
	for i, j := range jobs {
		views[i] = newJobView(j)
	}
	return views
}

type trackView struct {
	ID         string    `json:"id"`
	JobID      string    `json:"job_id"`
	Title      string    `json:"title"`
	Format     string    `json:"format"`
	DurationMS int64     `json:"duration_ms"`
	SizeBytes  int64     `json:"size_bytes"`
	SampleRate int       `json:"sample_rate,omitempty"`
	Seed       int64     `json:"seed"`
	IsStem     bool      `json:"is_stem"`
	StemName   string    `json:"stem_name,omitempty"`
	CreatedAt  time.Time `json:"created_at"`

	// AudioURL is where the browser streams this track. Streaming goes through
	// the API rather than the filesystem so that paths stay server-side.
	AudioURL string `json:"audio_url"`
}

func newTrackView(t *job.Track) trackView {
	return trackView{
		ID:         t.ID.String(),
		JobID:      t.JobID.String(),
		Title:      t.Title,
		Format:     t.Format,
		DurationMS: t.Duration.Milliseconds(),
		SizeBytes:  t.SizeBytes,
		SampleRate: t.SampleRate,
		Seed:       t.Seed,
		IsStem:     t.IsStem,
		StemName:   t.StemName,
		CreatedAt:  t.CreatedAt,
		AudioURL:   "/api/tracks/" + t.ID.String() + "/audio",
	}
}

func newTrackViews(tracks []*job.Track) []trackView {
	views := make([]trackView, len(tracks))
	for i, t := range tracks {
		views[i] = newTrackView(t)
	}
	return views
}

// jobDetailView is a job together with the audio it produced.
type jobDetailView struct {
	Job    jobView     `json:"job"`
	Tracks []trackView `json:"tracks"`
}

func newJobDetailView(d *app.JobDetail) jobDetailView {
	return jobDetailView{
		Job:    newJobView(d.Job),
		Tracks: newTrackViews(d.Tracks),
	}
}

type capabilitiesView struct {
	Kinds            []engine.Kind `json:"kinds"`
	Instrumental     bool          `json:"instrumental"`
	Lyrics           bool          `json:"lyrics"`
	MaxDurationMS    int64         `json:"max_duration_ms"`
	MaxConcurrent    int           `json:"max_concurrent"`
	ControlsDuration bool          `json:"controls_duration"`
}

func newCapabilitiesView(c engine.Capabilities) capabilitiesView {
	return capabilitiesView{
		Kinds:            c.Kinds,
		Instrumental:     c.Instrumental,
		Lyrics:           c.Lyrics,
		MaxDurationMS:    c.MaxDuration.Milliseconds(),
		MaxConcurrent:    c.MaxConcurrent,
		ControlsDuration: c.ControlsDuration,
	}
}

type engineStatusView struct {
	Name         string           `json:"name"`
	State        string           `json:"state"`
	Capabilities capabilitiesView `json:"capabilities"`
	Models       []string         `json:"models"`
	LastError    string           `json:"last_error,omitempty"`
}
