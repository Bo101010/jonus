package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Bo101010/jonus/internal/job"
	"github.com/Bo101010/jonus/internal/queue"
)

// heartbeatInterval is how often a comment is sent on an idle stream.
//
// Without it, a proxy or the browser may decide a quiet connection is dead. It
// also gives the server a chance to notice that the client has gone away.
const heartbeatInterval = 15 * time.Second

// handleJobEvents streams progress for one job.
func (s *Server) handleJobEvents(w http.ResponseWriter, r *http.Request) {
	id := job.ID(r.PathValue("id"))

	// Check the job exists before opening a stream, so a mistyped ID gets a 404
	// rather than an indefinitely silent connection.
	detail, err := s.app.Job(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}

	sub := s.app.Subscribe(id)
	defer sub.Close()

	var initial []queue.Event
	if event, ok := s.app.Hub().Latest(id); ok {
		initial = append(initial, event)
	}

	s.stream(w, r, sub, initial, detail.Job.State.Terminal())
}

// handleAllEvents streams progress for every job.
func (s *Server) handleAllEvents(w http.ResponseWriter, r *http.Request) {
	sub := s.app.Subscribe("")
	defer sub.Close()

	s.stream(w, r, sub, s.app.Hub().Snapshot(), false)
}

// stream writes server-sent events until the client disconnects or the stream
// ends.
//
// The snapshot is written first so that a page opened halfway through a job
// shows the current state immediately instead of waiting for the next tick.
func (s *Server) stream(
	w http.ResponseWriter,
	r *http.Request,
	sub *queue.Subscription,
	snapshot []queue.Event,
	alreadyFinished bool,
) {
	rc := http.NewResponseController(w)

	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	header.Set("Connection", "keep-alive")
	// Tells nginx and friends not to buffer, which would defeat streaming.
	header.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	for _, event := range snapshot {
		if err := writeEvent(w, rc, event); err != nil {
			s.logStreamError(err)
			return
		}
	}
	// A job that already finished will never publish again, so close the stream
	// rather than holding a connection open for a heartbeat.
	if alreadyFinished {
		return
	}

	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return

		case event, ok := <-sub.Events():
			if !ok {
				return
			}
			if err := writeEvent(w, rc, event); err != nil {
				s.logStreamError(err)
				return
			}

		case <-heartbeat.C:
			if _, err := io.WriteString(w, ": keep-alive\n\n"); err != nil {
				s.logStreamError(err)
				return
			}
			if err := rc.Flush(); err != nil {
				s.logStreamError(err)
				return
			}
		}
	}
}

// logStreamError reports why a stream ended.
//
// A client that goes away mid-stream is ordinary and not worth a warning. A
// writer that cannot flush is a bug in the middleware chain, and the only
// symptom is that streams end early without saying why — so it is worth
// shouting about.
func (s *Server) logStreamError(err error) {
	if errors.Is(err, http.ErrNotSupported) {
		s.log.Error("the event stream cannot be flushed; a middleware is hiding the response writer",
			"error", err)
		return
	}
	s.log.Debug("event stream ended", "error", err)
}

// writeEvent serialises one event and flushes it.
func writeEvent(w io.Writer, rc *http.ResponseController, event queue.Event) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode event: %w", err)
	}
	if _, err := fmt.Fprintf(w, "event: progress\ndata: %s\n\n", payload); err != nil {
		return err
	}
	return rc.Flush()
}
