// Package httpapi is the HTTP transport: it translates requests into
// application calls and results into JSON or server-sent events.
//
// It holds no business logic. Anything a CLI command would also need to decide
// lives in the app package, so the two front ends cannot disagree.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/shaolianbo/jonus/internal/app"
	"github.com/shaolianbo/jonus/internal/job"
)

// maxRequestBody bounds a request body. Requests are small JSON documents;
// without a limit, a stray client could make the process allocate freely.
const maxRequestBody = 1 << 20 // 1 MiB

// Server serves the JSON API, the event streams, and the embedded web UI.
//
// It deliberately does not own an http.Server or its lifecycle: the caller
// builds one around Handler, which keeps this package testable with
// httptest.NewServer and leaves shutdown to the composition root.
type Server struct {
	app *app.App
	log *slog.Logger

	// ui serves the single-page application for non-API paths. It may be nil,
	// which leaves the server API-only.
	ui http.Handler

	dev bool
}

// Option configures a Server.
type Option func(*Server)

// WithDev enables development behaviour, currently a permissive CORS policy so
// that a Vite dev server on another port can call the API.
func WithDev(dev bool) Option {
	return func(s *Server) { s.dev = dev }
}

// New builds a server.
func New(a *app.App, log *slog.Logger, ui http.Handler, opts ...Option) *Server {
	s := &Server{app: a, log: log, ui: ui}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Handler returns the routed handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/engine/status", s.handleEngineStatus)
	mux.HandleFunc("GET /api/models", s.handleModels)

	mux.HandleFunc("GET /api/jobs", s.handleListJobs)
	mux.HandleFunc("POST /api/jobs", s.handleCreateJob)
	mux.HandleFunc("GET /api/jobs/{id}", s.handleGetJob)
	mux.HandleFunc("DELETE /api/jobs/{id}", s.handleDeleteJob)
	mux.HandleFunc("POST /api/jobs/{id}/cancel", s.handleCancelJob)
	mux.HandleFunc("POST /api/jobs/{id}/retry", s.handleRetryJob)
	mux.HandleFunc("GET /api/jobs/{id}/events", s.handleJobEvents)

	mux.HandleFunc("GET /api/events", s.handleAllEvents)

	mux.HandleFunc("GET /api/tracks", s.handleListTracks)
	mux.HandleFunc("GET /api/tracks/{id}", s.handleGetTrack)
	mux.HandleFunc("GET /api/tracks/{id}/audio", s.handleTrackAudio)

	// Go's router picks the most specific pattern, so registration order does
	// not matter here. What does matter is this catch-all: without it, a
	// mistyped API path would fall through to the SPA and be answered with
	// HTML and a 200, which is a miserable thing to debug from a fetch call.
	mux.HandleFunc("/api/", handleAPINotFound)

	if s.ui != nil {
		mux.Handle("/", s.ui)
	}

	// Outermost first: recover, then observe.
	return s.recoverPanics(s.logRequests(s.withCORS(mux)))
}

// handleAPINotFound answers any unmatched /api path as JSON.
func handleAPINotFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotFound, "no such API endpoint: "+r.URL.Path)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// logRequests records each request at debug level. Successful API calls are not
// interesting enough for the default level, but failures are.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		level := slog.LevelDebug
		if rec.status >= http.StatusInternalServerError {
			level = slog.LevelError
		}
		s.log.Log(r.Context(), level, "request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"elapsed", time.Since(start).Round(time.Millisecond),
		)
	})
}

// recoverPanics keeps one bad request from taking down the server.
func (s *Server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			// A panic on a hijacked or already-written response cannot be
			// turned into an error page; the connection just ends.
			if rec == http.ErrAbortHandler {
				panic(rec)
			}
			s.log.Error("handler panicked",
				"method", r.Method,
				"path", r.URL.Path,
				"panic", rec,
				"stack", string(debug.Stack()),
			)
			writeError(w, http.StatusInternalServerError, "internal error")
		}()
		next.ServeHTTP(w, r)
	})
}

// withCORS allows the Vite dev server to call the API from another origin.
//
// It is enabled only in dev, and only for loopback origins, because a
// permissive policy on a local server that holds the user's library would let
// any web page they visit read it.
func (s *Server) withCORS(next http.Handler) http.Handler {
	if !s.dev {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if isLoopbackOrigin(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isLoopbackOrigin(origin string) bool {
	if origin == "" {
		return false
	}
	rest, ok := strings.CutPrefix(origin, "http://")
	if !ok {
		rest, ok = strings.CutPrefix(origin, "https://")
	}
	if !ok {
		return false
	}
	host, _, found := strings.Cut(rest, ":")
	if !found {
		host = rest
	}
	return host == "localhost" || host == "127.0.0.1" || host == "[::1]"
}

// statusRecorder remembers the status code for logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// Unwrap exposes the writer underneath.
//
// This is not decoration: http.ResponseController finds Flush and Hijack by
// unwrapping, so without it the event streams lose their Flush and every SSE
// response ends the moment it is written. The failure is silent — the handler
// just returns early and the client sees a closed connection.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// writeJSON encodes v as the response body.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)

	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// The status line is already sent, so the only useful thing left is to
		// note it. The client will see a truncated body.
		slog.Default().Warn("writing JSON response failed", "error", err)
	}
}

// errorBody is the shape of every error response.
type errorBody struct {
	Error string `json:"error"`
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, errorBody{Error: message})
}

// fail maps an application error onto a status code and writes it.
func (s *Server) fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, job.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, job.ErrInvalidTransition):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		// The client went away; nothing will read the response.
		writeError(w, http.StatusRequestTimeout, err.Error())
	default:
		s.log.Error("request failed", "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

// decodeJSON reads a request body into v, rejecting anything oversized or
// containing unknown fields.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)

	dec := json.NewDecoder(r.Body)
	// An unknown field is a typo in a client, not something to ignore: silently
	// dropping it means the caller's intent is lost without any signal.
	dec.DisallowUnknownFields()

	if err := dec.Decode(v); err != nil {
		return describeDecodeError(err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain a single JSON object")
	}
	return nil
}

func describeDecodeError(err error) error {
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError

	switch {
	case errors.As(err, &syntaxErr):
		return errors.New("request body is not valid JSON: " + syntaxErr.Error())
	case errors.As(err, &typeErr):
		return errors.New("field " + typeErr.Field + " has the wrong type")
	case errors.Is(err, io.EOF):
		return errors.New("request body is empty")
	default:
		return err
	}
}
