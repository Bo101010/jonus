package queue

import (
	"sync"
	"time"

	"github.com/shaolianbo/jonus/internal/job"
)

// subscriberBuffer is how many events a subscriber may fall behind before the
// hub starts discarding its oldest ones.
const subscriberBuffer = 128

// Event is one progress or state change, as broadcast to subscribers.
//
// It is the wire shape of a job update: the SSE endpoints serialise it
// directly, so its JSON tags are part of the HTTP API.
type Event struct {
	JobID    job.ID    `json:"job_id"`
	State    job.State `json:"state"`
	Phase    string    `json:"phase,omitempty"`
	Fraction float64   `json:"fraction"`
	Message  string    `json:"message,omitempty"`
	Error    string    `json:"error,omitempty"`
	At       time.Time `json:"at"`
}

// Hub broadcasts job events to interested subscribers in memory.
//
// Progress arrives far faster than it is worth persisting — the engine reports
// several times a second and the UI cannot draw faster than that — so the hub
// carries it and the store is written on a throttle instead. Keeping the two
// separate is what stops a progress bar from costing thousands of writes.
type Hub struct {
	mu     sync.Mutex
	nextID int
	subs   map[int]*Subscription
	latest map[job.ID]Event
}

// NewHub builds an empty hub.
func NewHub() *Hub {
	return &Hub{
		subs:   make(map[int]*Subscription),
		latest: make(map[job.ID]Event),
	}
}

// Publish delivers e to every subscriber watching its job.
//
// Publish never blocks: a subscriber that has fallen behind loses its oldest
// buffered event rather than stalling the worker. Losing a progress tick is
// harmless because progress is a snapshot — a reader that wants certainty can
// call Latest.
func (h *Hub) Publish(e Event) {
	if e.At.IsZero() {
		e.At = time.Now()
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	h.latest[e.JobID] = e
	for _, sub := range h.subs {
		if sub.jobID != "" && sub.jobID != e.JobID {
			continue
		}
		sub.deliver(e)
	}
}

// Subscribe returns a subscription for one job, or for every job when jobID is
// empty.
//
// The caller must Close the subscription, or its goroutine will keep receiving
// events for as long as the process runs.
func (h *Hub) Subscribe(jobID job.ID) *Subscription {
	h.mu.Lock()
	defer h.mu.Unlock()

	sub := &Subscription{
		hub:    h,
		id:     h.nextID,
		jobID:  jobID,
		events: make(chan Event, subscriberBuffer),
	}
	h.nextID++
	h.subs[sub.id] = sub
	return sub
}

// Latest returns the most recent event published for a job.
func (h *Hub) Latest(id job.ID) (Event, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	e, ok := h.latest[id]
	return e, ok
}

// Snapshot returns the latest event for every job that has one.
//
// A subscriber takes this on connect so that a page opened halfway through a
// job shows the current state immediately instead of waiting for the next tick.
func (h *Hub) Snapshot() []Event {
	h.mu.Lock()
	defer h.mu.Unlock()

	events := make([]Event, 0, len(h.latest))
	for _, e := range h.latest {
		events = append(events, e)
	}
	return events
}

// Forget drops a job's remembered event, so a deleted job does not linger in
// snapshots for the lifetime of the process.
func (h *Hub) Forget(id job.ID) {
	h.mu.Lock()
	defer h.mu.Unlock()

	delete(h.latest, id)
}

// unsubscribe removes a subscription and closes its channel.
//
// Closing happens under the hub lock, which is what makes it safe: Publish also
// holds the lock, so once the subscription is gone no further send can be in
// flight. Only the sender closes.
func (h *Hub) unsubscribe(id int) {
	h.mu.Lock()
	defer h.mu.Unlock()

	sub, ok := h.subs[id]
	if !ok {
		return
	}
	delete(h.subs, id)
	close(sub.events)
}

// Subscription is one reader's view of the event stream.
type Subscription struct {
	hub    *Hub
	id     int
	jobID  job.ID
	events chan Event
	once   sync.Once
}

// Events is the stream of events. It is closed when the subscription closes.
func (s *Subscription) Events() <-chan Event { return s.events }

// JobID is the job this subscription watches, or empty for all jobs.
func (s *Subscription) JobID() job.ID { return s.jobID }

// Close ends the subscription. It is safe to call more than once.
func (s *Subscription) Close() {
	s.once.Do(func() { s.hub.unsubscribe(s.id) })
}

// deliver enqueues an event, discarding the oldest buffered one if the reader
// has fallen behind.
func (s *Subscription) deliver(e Event) {
	select {
	case s.events <- e:
		return
	default:
	}

	// The buffer is full. Dropping the oldest event keeps the reader moving
	// towards the newest state, which is the one it will draw.
	select {
	case <-s.events:
	default:
	}

	select {
	case s.events <- e:
	default:
	}
}
