package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/shaolianbo/jonus/internal/app"
	"github.com/shaolianbo/jonus/internal/engine"
	"github.com/shaolianbo/jonus/internal/job"
)

// maxDurationSeconds bounds a requested length on the command line, matching
// the API's limit.
const maxDurationSeconds = 3600

func newGenerateCmd() *cobra.Command {
	var (
		title        string
		style        string
		lyrics       string
		lyricsFile   string
		instrumental bool
		duration     time.Duration
		lang         string
		seed         int64
		model        string
		kind         string
		output       string
		wait         bool
		asJSON       bool
	)

	cmd := &cobra.Command{
		Use:   "generate [prompt]",
		Short: "Generate a song from a description",
		Long: `Generate a song from a description.

The job is queued and this command returns immediately unless --wait is given.
With --wait it follows the job's progress, using the same event stream the web
UI consumes, so both surfaces report exactly the same thing.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			prompt := strings.Join(args, " ")

			text, err := resolveLyrics(lyrics, lyricsFile)
			if err != nil {
				return err
			}
			if strings.TrimSpace(prompt) == "" && strings.TrimSpace(text) == "" {
				return fmt.Errorf("provide a prompt, --lyrics, or --lyrics-file")
			}
			if err := validateDuration(duration); err != nil {
				return err
			}

			jobKind := engine.Kind(kind)
			if kind == "" {
				jobKind = engine.KindText2Music
			}
			if !jobKind.Valid() {
				return fmt.Errorf("unknown --kind %q", kind)
			}

			cfg, err := resolveConfig(cmd)
			if err != nil {
				return err
			}
			log := newLogger(cfg)

			a, err := app.New(cmd.Context(), &cfg, log)
			if err != nil {
				return err
			}
			defer a.Close()

			created, err := a.Generate(cmd.Context(), app.GenerateRequest{
				Kind:         jobKind,
				Title:        title,
				Prompt:       prompt,
				Style:        style,
				Lyrics:       text,
				Instrumental: instrumental,
				Duration:     duration,
				Language:     lang,
				Seed:         seed,
				Model:        model,
			})
			if err != nil {
				return err
			}

			if !wait {
				return reportSubmitted(created, asJSON)
			}

			// Run the queue here for as long as this command is waiting: the
			// queue is in-process, so without this the job would sit pending
			// forever unless a `jonus serve` happened to be running.
			stop := runQueue(cmd.Context(), a)
			defer stop()

			return followJob(cmd.Context(), a, created.ID, output, asJSON)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&title, "title", "", "title for the track (default: derived from the prompt)")
	flags.StringVar(&style, "style", "", "style tags appended to the prompt, for example \"acoustic, warm\"")
	flags.StringVar(&lyrics, "lyrics", "", "lyrics; lines may carry [Verse] and [Chorus] tags")
	flags.StringVar(&lyricsFile, "lyrics-file", "", "read lyrics from a file instead of --lyrics")
	flags.BoolVar(&instrumental, "instrumental", false, "generate without a lead vocal")
	flags.DurationVar(&duration, "duration", 0, "requested length, for example 60s (default: the engine's)")
	flags.StringVar(&lang, "lang", "", "language hint, for example zh or en")
	flags.Int64Var(&seed, "seed", 0, "seed for reproducible output (0 picks one at random)")
	flags.StringVar(&model, "model", "", "engine-specific model name")
	flags.StringVar(&kind, "kind", "", "operation: text2music, cover, repaint, lego, extract, or complete")
	flags.StringVar(&output, "output", "", "copy the finished audio to this path")
	flags.BoolVar(&wait, "wait", false, "follow the job until it finishes")
	flags.BoolVar(&asJSON, "json", false, "print machine-readable JSON")

	return cmd
}

// resolveLyrics reads lyrics from exactly one of the two sources.
func resolveLyrics(inline, path string) (string, error) {
	if inline != "" && path != "" {
		return "", fmt.Errorf("use either --lyrics or --lyrics-file, not both")
	}
	if path == "" {
		return inline, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read lyrics file: %w", err)
	}
	return string(data), nil
}

func validateDuration(d time.Duration) error {
	switch {
	case d < 0:
		return fmt.Errorf("--duration cannot be negative")
	case d > maxDurationSeconds*time.Second:
		return fmt.Errorf("--duration must be at most %ds", maxDurationSeconds)
	default:
		return nil
	}
}

// reportSubmitted prints the job that was queued.
func reportSubmitted(j *job.Job, asJSON bool) error {
	if asJSON {
		return printJSON(map[string]any{
			"id":     j.ID.String(),
			"state":  string(j.State),
			"title":  j.Title,
			"engine": j.Engine,
		})
	}

	fmt.Printf("queued %s  %s\n", j.ID, j.Title)
	fmt.Printf("follow it with: jonus jobs show %s --follow\n", j.ID)
	return nil
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
