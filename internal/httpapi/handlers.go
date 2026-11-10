package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/shaolianbo/jonus/internal/app"
	"github.com/shaolianbo/jonus/internal/engine"
	"github.com/shaolianbo/jonus/internal/job"
)

// maxDurationSeconds bounds a requested track length. It is a sanity limit on
// the request itself; the engine applies its own, lower limit.
const maxDurationSeconds = 3600

func (s *Server) handleEngineStatus(w http.ResponseWriter, r *http.Request) {
	status := s.app.EngineStatus(r.Context())
	writeJSON(w, http.StatusOK, engineStatusView{
		Name:         status.Name,
		State:        status.State,
		Capabilities: newCapabilitiesView(status.Capabilities),
		Models:       status.Models,
		LastError:    status.LastError,
	})
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	status := s.app.EngineStatus(r.Context())
	models := status.Models
	if models == nil {
		models = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	filter, err := jobFilter(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	jobs, err := s.app.ListJobs(r.Context(), filter)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": newJobViews(jobs)})
}

// createJobRequest is the body of POST /api/jobs.
type createJobRequest struct {
	Kind            string  `json:"kind"`
	Title           string  `json:"title"`
	Prompt          string  `json:"prompt"`
	Style           string  `json:"style"`
	Lyrics          string  `json:"lyrics"`
	Instrumental    bool    `json:"instrumental"`
	DurationSeconds float64 `json:"duration_seconds"`
	Language        string  `json:"language"`
	Seed            int64   `json:"seed"`
	Model           string  `json:"model"`
	Priority        int     `json:"priority"`
}

// validate rejects requests that cannot produce anything, so the user gets a
// clear error instead of a job that fails a moment later.
func (req createJobRequest) validate() error {
	if strings.TrimSpace(req.Prompt) == "" && strings.TrimSpace(req.Lyrics) == "" {
		return errors.New("provide a prompt, lyrics, or both")
	}
	switch {
	case req.DurationSeconds < 0:
		return errors.New("duration_seconds cannot be negative")
	case req.DurationSeconds > maxDurationSeconds:
		return fmt.Errorf("duration_seconds must be at most %d", maxDurationSeconds)
	}
	if req.Kind != "" && !engine.Kind(req.Kind).Valid() {
		return fmt.Errorf("unknown kind %q", req.Kind)
	}
	return nil
}

func (s *Server) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	var req createJobRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := req.validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	kind := engine.Kind(req.Kind)
	if kind == "" {
		kind = engine.KindText2Music
	}

	created, err := s.app.Generate(r.Context(), app.GenerateRequest{
		Kind:         kind,
		Title:        req.Title,
		Prompt:       req.Prompt,
		Style:        req.Style,
		Lyrics:       req.Lyrics,
		Instrumental: req.Instrumental,
		Duration:     secondsToDuration(req.DurationSeconds),
		Language:     req.Language,
		Seed:         req.Seed,
		Model:        req.Model,
		Priority:     req.Priority,
	})
	if err != nil {
		s.fail(w, err)
		return
	}

	// 202: the job is accepted and queued, not finished.
	writeJSON(w, http.StatusAccepted, newJobView(created))
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	detail, err := s.app.Job(r.Context(), job.ID(r.PathValue("id")))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newJobDetailView(detail))
}

func (s *Server) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	id := job.ID(r.PathValue("id"))

	if err := s.app.CancelJob(r.Context(), id); err != nil {
		s.fail(w, err)
		return
	}

	// The state after cancelling depends on whether the job had started, so
	// report what actually happened rather than assuming.
	detail, err := s.app.Job(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, newJobDetailView(detail))
}

func (s *Server) handleRetryJob(w http.ResponseWriter, r *http.Request) {
	retried, err := s.app.RetryJob(r.Context(), job.ID(r.PathValue("id")))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, newJobView(retried))
}

func (s *Server) handleDeleteJob(w http.ResponseWriter, r *http.Request) {
	if err := s.app.DeleteJob(r.Context(), job.ID(r.PathValue("id"))); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListTracks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	filter := job.TrackFilter{JobID: job.ID(q.Get("job_id"))}
	limit, err := intParam(q, "limit", 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	offset, err := intParam(q, "offset", 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	filter.Limit, filter.Offset = limit, offset

	tracks, err := s.app.Library(r.Context(), filter)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tracks": newTrackViews(tracks)})
}

func (s *Server) handleGetTrack(w http.ResponseWriter, r *http.Request) {
	track, err := s.app.Track(r.Context(), job.ID(r.PathValue("id")))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newTrackView(track))
}

// handleTrackAudio streams a track's audio file.
//
// http.ServeContent does the work: it answers Range requests, conditional
// requests, and HEAD, which is what lets the browser seek in a track instead of
// downloading all of it first.
func (s *Server) handleTrackAudio(w http.ResponseWriter, r *http.Request) {
	track, err := s.app.Track(r.Context(), job.ID(r.PathValue("id")))
	if err != nil {
		s.fail(w, err)
		return
	}

	f, err := os.Open(track.Path)
	if err != nil {
		if os.IsNotExist(err) {
			// The record exists but its audio does not, which is a different
			// problem from an unknown track and should say so.
			writeError(w, http.StatusGone, "the audio file for this track is missing")
			return
		}
		s.fail(w, err)
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		s.fail(w, err)
		return
	}
	if info.IsDir() {
		writeError(w, http.StatusInternalServerError, "track path is a directory")
		return
	}

	name := downloadName(track)
	w.Header().Set("Content-Type", audioMIME(track.Format))
	if r.URL.Query().Has("download") {
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	}

	http.ServeContent(w, r, name, info.ModTime(), f)
}

// audioMIME maps a container format onto its media type.
func audioMIME(format string) string {
	switch strings.ToLower(format) {
	case "wav":
		return "audio/wav"
	case "mp3":
		return "audio/mpeg"
	case "flac":
		return "audio/flac"
	case "ogg":
		return "audio/ogg"
	case "m4a":
		return "audio/mp4"
	default:
		return "application/octet-stream"
	}
}

// downloadName builds a safe filename from a track's title.
func downloadName(track *job.Track) string {
	name := strings.Map(func(r rune) rune {
		// Path separators, quotes, and control characters have no business in
		// a filename, and a title is user-supplied.
		switch {
		case r == '/' || r == '\\' || r == '"' || r < 0x20:
			return '-'
		default:
			return r
		}
	}, track.Title)
	name = strings.TrimSpace(name)
	if name == "" {
		name = track.ID.String()
	}

	format := strings.ToLower(track.Format)
	if format == "" {
		format = "audio"
	}
	return name + "." + format
}

// jobFilter parses list query parameters.
func jobFilter(q url.Values) (job.Filter, error) {
	var f job.Filter

	for _, part := range strings.Split(q.Get("state"), ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		state := job.State(part)
		if !state.Valid() {
			return f, fmt.Errorf("unknown state %q", part)
		}
		f.States = append(f.States, state)
	}

	limit, err := intParam(q, "limit", 0)
	if err != nil {
		return f, err
	}
	offset, err := intParam(q, "offset", 0)
	if err != nil {
		return f, err
	}
	if limit < 0 || offset < 0 {
		return f, errors.New("limit and offset cannot be negative")
	}
	f.Limit, f.Offset = limit, offset
	return f, nil
}

func intParam(q url.Values, key string, fallback int) (int, error) {
	raw := q.Get(key)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a whole number", key)
	}
	return value, nil
}

// secondsToDuration converts a JSON seconds value to a Duration, treating zero
// as "engine default".
func secondsToDuration(seconds float64) time.Duration {
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds * float64(time.Second))
}
