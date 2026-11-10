package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/shaolianbo/jonus/internal/app"
	"github.com/shaolianbo/jonus/internal/job"
	"github.com/shaolianbo/jonus/internal/queue"
)

// followPollInterval is how often a follower re-reads the job from the database.
const followPollInterval = time.Second

// runQueue advances the job queue in the background until stop is called.
//
// A command that waits for a job has to run the work itself: the queue lives in
// the process, so a process that only watches would wait forever for a job
// nobody is going to pick up. It is safe to run one here even when `jonus serve`
// is also running, because claiming a job is a single atomic statement — the two
// processes split the queue between them rather than racing for the same job.
//
// stop cancels the queue and waits for the worker to finish what it was doing,
// so a cancelled command does not leave a generation writing files behind it.
func runQueue(ctx context.Context, a *app.App) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)

	go func() { done <- a.Run(ctx) }()

	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
				fmt.Fprintf(os.Stderr, "the job queue stopped: %v\n", err)
			}
		})
	}
}

// followJob watches a job until it finishes, then reports what it produced.
//
// It consumes the same subscription the web UI's event stream uses, so the two
// surfaces report identical progress. The rendering here is the only thing that
// differs.
func followJob(ctx context.Context, a *app.App, id job.ID, output string, asJSON bool) error {
	// Subscribe before reading the current state: a fast job can finish between
	// the two, and reading first would then wait forever for an event that has
	// already been published.
	sub := a.Subscribe(id)
	defer sub.Close()

	detail, err := a.Job(ctx, id)
	if err != nil {
		return err
	}

	var printer *progressPrinter
	if !asJSON {
		printer = newProgressPrinter(os.Stderr)
	}

	state := detail.Job.State
	if !state.Terminal() {
		if printer != nil {
			printer.update(queue.Event{
				State:    detail.Job.State,
				Phase:    detail.Job.Phase,
				Fraction: detail.Job.Progress,
			})
		}
		state, err = drainUntilTerminal(ctx, a, id, sub, printer)
		if err != nil {
			return err
		}
	}

	// Re-read: the terminal event carries the state, but not the tracks.
	final, err := a.Job(ctx, id)
	if err != nil {
		return err
	}
	if printer != nil {
		printer.clear()
	}

	return reportOutcome(final, state, output, asJSON)
}

// drainUntilTerminal follows a job until it reaches a terminal state.
//
// It listens to the in-process event stream and also re-reads the job on a
// ticker. The ticker is not belt-and-braces: a process only receives events its
// own queue published, so when the job is being run by a separate `jonus serve`
// the stream stays silent, and polling is what notices the job finishing.
func drainUntilTerminal(
	ctx context.Context,
	a *app.App,
	id job.ID,
	sub *queue.Subscription,
	printer *progressPrinter,
) (job.State, error) {
	ticker := time.NewTicker(followPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()

		case event, ok := <-sub.Events():
			if !ok {
				return "", errors.New("the job's event stream closed unexpectedly")
			}
			if printer != nil {
				printer.update(event)
			}
			if event.State.Terminal() {
				return event.State, nil
			}

		case <-ticker.C:
			detail, err := a.Job(ctx, id)
			if err != nil {
				return "", err
			}
			if printer != nil {
				printer.update(queue.Event{
					State:    detail.Job.State,
					Phase:    detail.Job.Phase,
					Fraction: detail.Job.Progress,
				})
			}
			if detail.Job.State.Terminal() {
				return detail.Job.State, nil
			}
		}
	}
}

// reportOutcome prints how the job ended and, on success, where its audio is.
func reportOutcome(detail *app.JobDetail, state job.State, output string, asJSON bool) error {
	if asJSON {
		return printJSON(outcomeJSON(detail, state, output))
	}

	switch state {
	case job.StateSucceeded:
		fmt.Printf("done  %s\n", detail.Job.Title)
		for _, track := range detail.Tracks {
			fmt.Printf("  %s  %s\n", humanDuration(track.Duration), track.Path)
		}
		if len(detail.Tracks) == 0 {
			fmt.Println("  (the engine reported success but produced no tracks)")
		}
	default:
		message := detail.Job.Error
		if message == "" {
			message = string(state)
		}
		fmt.Printf("%s  %s\n", state, message)
	}

	if output == "" {
		return outcomeError(state, detail.Job)
	}

	// Copying is offered only for a single track: with stems or several
	// variants there is no obvious one to copy, and guessing would be worse
	// than saying so.
	if len(detail.Tracks) != 1 {
		return fmt.Errorf("--output needs exactly one produced track, but the job made %d",
			len(detail.Tracks))
	}
	if err := copyTrack(detail.Tracks[0].Path, output); err != nil {
		return err
	}
	fmt.Printf("  wrote %s\n", output)

	return outcomeError(state, detail.Job)
}

func outcomeJSON(detail *app.JobDetail, state job.State, output string) map[string]any {
	tracks := make([]map[string]any, len(detail.Tracks))
	for i, t := range detail.Tracks {
		tracks[i] = map[string]any{
			"id":          t.ID.String(),
			"title":       t.Title,
			"path":        t.Path,
			"format":      t.Format,
			"duration_ms": t.Duration.Milliseconds(),
			"size_bytes":  t.SizeBytes,
			"is_stem":     t.IsStem,
			"stem_name":   t.StemName,
		}
	}

	out := map[string]any{
		"id":      detail.Job.ID.String(),
		"state":   string(state),
		"title":   detail.Job.Title,
		"tracks":  tracks,
		"seed":    detail.Job.Seed,
		"error":   detail.Job.Error,
		"attempt": detail.Job.Attempt,
	}
	if output != "" {
		out["output"] = output
	}
	return out
}

// outcomeError turns an unsuccessful job into a non-zero exit.
//
// A failed job is a failed command: scripts need to be able to tell.
func outcomeError(state job.State, j *job.Job) error {
	switch state {
	case job.StateSucceeded:
		return nil
	case job.StateCanceled:
		return fmt.Errorf("job %s was cancelled", j.ID)
	case job.StateInterrupted:
		return fmt.Errorf("job %s was interrupted: %s", j.ID, j.Error)
	default:
		return fmt.Errorf("job %s failed: %s", j.ID, j.Error)
	}
}

// copyTrack writes src to dst, creating parent directories as needed.
func copyTrack(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer in.Close()

	if dir := filepath.Dir(dst); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}

	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}

	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return fmt.Errorf("write %s: %w", dst, err)
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
		return fmt.Errorf("close %s: %w", dst, err)
	}
	return nil
}

// progressPrinter renders progress on a terminal.
//
// On a terminal it rewrites one line, which keeps a long job from scrolling the
// screen. Piped elsewhere it prints a line per phase change, so a log gets a
// readable trace rather than a wall of carriage returns.
type progressPrinter struct {
	w           io.Writer
	tty         bool
	lastPhase   string
	lastPercent int
	width       int
}

func newProgressPrinter(w *os.File) *progressPrinter {
	info, err := w.Stat()
	tty := err == nil && info.Mode()&os.ModeCharDevice != 0
	return &progressPrinter{w: w, tty: tty, lastPercent: -1}
}

// update renders one progress event, if it says anything new.
func (p *progressPrinter) update(e queue.Event) {
	percent := int(e.Fraction * 100)
	phase := e.Phase
	if phase == "" {
		phase = string(e.State)
	}

	if p.tty {
		// Only redraw when the rendered text would actually differ.
		if phase == p.lastPhase && percent == p.lastPercent {
			return
		}
		fmt.Fprintf(p.w, "\r\033[K%-14s %3d%%", phase, percent)
		p.lastPhase, p.lastPercent = phase, percent
		return
	}

	if phase == p.lastPhase {
		return
	}
	fmt.Fprintf(p.w, "%s\n", phase)
	p.lastPhase = phase
}

// clear erases the progress line so the final report starts on a clean line.
func (p *progressPrinter) clear() {
	if !p.tty || p.lastPhase == "" {
		return
	}
	fmt.Fprint(p.w, "\r\033[K")
}

// humanDuration renders a duration as m:ss, which is how track lengths read.
func humanDuration(d time.Duration) string {
	total := int(d.Round(time.Second).Seconds())
	return fmt.Sprintf("%d:%02d", total/60, total%60)
}
