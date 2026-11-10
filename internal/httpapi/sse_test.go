package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Bo101010/jonus/internal/app"
	"github.com/Bo101010/jonus/internal/config"
	"github.com/Bo101010/jonus/internal/queue"
)

// newTestServer builds a server over a real application on a temporary data
// directory, with the queue running.
//
// The stream endpoints are the one part of the API that cannot be tested against
// a stub: their whole job is to stay open and forward events as they happen, so
// a test has to have something that actually produces them.
func newTestServer(t *testing.T) (*httptest.Server, *app.App) {
	t.Helper()

	cfg := config.Config{
		DataDir:    t.TempDir(),
		EngineName: config.EngineMock,
		LogLevel:   slog.LevelError,
	}
	log := slog.New(slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{Level: slog.LevelError}))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	a, err := app.New(ctx, &cfg, log)
	if err != nil {
		t.Fatalf("build app: %v", err)
	}
	t.Cleanup(func() { a.Close() })

	go func() { _ = a.Run(ctx) }()

	srv := httptest.NewServer(New(a, log, nil).Handler())
	t.Cleanup(srv.Close)

	return srv, a
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// TestEventStreamStaysOpenAndForwardsProgress is the regression test for a
// response writer that hides Flush from http.ResponseController.
//
// When that happens the handler writes its opening snapshot, fails to flush,
// returns, and closes the connection — so the client sees exactly one event and
// no error. Asserting on a *second* event is the only way to tell a working
// stream from that failure.
func TestEventStreamStaysOpenAndForwardsProgress(t *testing.T) {
	srv, _ := newTestServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/events", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}

	// Create the job only once the stream is open, so the events cannot have
	// been sitting in a buffer before the subscription existed.
	created := createJob(t, srv.URL, `{"prompt":"stream test","duration_seconds":5}`)

	events := readEvents(t, resp.Body, created)
	if len(events) < 2 {
		t.Fatalf("got %d events for job %s, want at least 2 (a stream that never flushes sends only its snapshot)",
			len(events), created)
	}

	// The last event of a finished job must say so, otherwise a client watching
	// the stream would wait forever.
	last := events[len(events)-1]
	if last.State != "succeeded" {
		t.Errorf("final event state = %q, want succeeded", last.State)
	}
}

// TestJobEventStreamRejectsUnknownJob checks that a mistyped id fails loudly
// instead of opening a stream that will never say anything.
func TestJobEventStreamRejectsUnknownJob(t *testing.T) {
	srv, _ := newTestServer(t)

	resp, err := http.Get(srv.URL + "/api/jobs/does-not-exist/events")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

// TestJobEventStreamClosesWhenAlreadyFinished checks that watching a finished
// job returns instead of holding the connection open for a heartbeat.
func TestJobEventStreamClosesWhenAlreadyFinished(t *testing.T) {
	srv, _ := newTestServer(t)

	created := createJob(t, srv.URL, `{"prompt":"already done","duration_seconds":5}`)

	// Wait for the job to finish by polling the API rather than the stream, so
	// this test does not depend on the streaming path it is not testing.
	deadline := time.Now().Add(30 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("the job never finished")
		}
		var detail struct {
			Job struct {
				State string `json:"state"`
			} `json:"job"`
		}
		resp, err := http.Get(srv.URL + "/api/jobs/" + created)
		if err != nil {
			t.Fatalf("poll job: %v", err)
		}
		err = json.NewDecoder(resp.Body).Decode(&detail)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("decode job: %v", err)
		}
		if detail.Job.State == "succeeded" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/jobs/"+created+"/events", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer resp.Body.Close()

	events := readEvents(t, resp.Body, created)
	if len(events) == 0 {
		t.Fatal("a finished job's stream sent no snapshot")
	}
	if got := events[len(events)-1].State; got != "succeeded" {
		t.Errorf("snapshot state = %q, want succeeded", got)
	}

	// The handler returning closes the body; reading must not block until the
	// context deadline.
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 1)
		for {
			if _, err := resp.Body.Read(buf); err != nil {
				return
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the stream stayed open for a job that had already finished")
	}
}

func createJob(t *testing.T, baseURL, body string) string {
	t.Helper()

	resp, err := http.Post(baseURL+"/api/jobs", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("create job status = %d, want %d", resp.StatusCode, http.StatusAccepted)
	}

	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode created job: %v", err)
	}
	return created.ID
}

// readEvents reads events for jobID until the stream ends or the job reports a
// terminal state.
func readEvents(t *testing.T, body io.Reader, jobID string) []queue.Event {
	t.Helper()

	var (
		events []queue.Event
		sc     = bufio.NewScanner(body)
	)
	// A single event is small, but a scanner stops at 64KiB by default and a
	// silent truncation would look like a stream that closed early.
	sc.Buffer(make([]byte, 0, 4096), 1<<20)

	for sc.Scan() {
		line := sc.Text()
		payload, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}

		var event queue.Event
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			t.Fatalf("decode event %q: %v", payload, err)
		}
		if event.JobID.String() != jobID {
			continue
		}

		events = append(events, event)
		if event.State.Terminal() {
			break
		}
	}
	return events
}
