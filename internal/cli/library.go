package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/Bo101010/jonus/internal/app"
	"github.com/Bo101010/jonus/internal/job"
)

func newLibraryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "library",
		Short: "Browse the tracks you have generated",
		Args:  cobra.NoArgs,
	}

	cmd.AddCommand(
		newLibraryListCmd(),
		newLibraryPlayCmd(),
		newLibraryExportCmd(),
	)
	return cmd
}

func newLibraryListCmd() *cobra.Command {
	var (
		jobID   string
		limit   int
		asJSON  bool
		include bool
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List tracks",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withApp(cmd, func(a *app.App) error {
				filter := job.TrackFilter{Limit: limit}
				if jobID != "" {
					filter.JobID = job.ID(jobID)
				}
				if !include {
					filter.ExcludeStems = true
				}

				tracks, err := a.Library(cmd.Context(), filter)
				if err != nil {
					return err
				}

				if asJSON {
					return printJSON(map[string]any{"tracks": libraryJSON(tracks)})
				}
				if len(tracks) == 0 {
					fmt.Println("the library is empty; make something with: jonus generate \"轻快的民谣\"")
					return nil
				}
				return renderLibrary(tracks)
			})
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&jobID, "job", "", "only show tracks from this job")
	flags.IntVar(&limit, "limit", 20, "maximum number of tracks to show (0 for all)")
	flags.BoolVar(&asJSON, "json", false, "print machine-readable JSON")
	flags.BoolVar(&include, "stems", false, "include separated stems")

	return cmd
}

func newLibraryPlayCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "play <track-id>",
		Short: "Play a track with the system's audio player",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withApp(cmd, func(a *app.App) error {
				track, err := a.Track(cmd.Context(), job.ID(args[0]))
				if err != nil {
					return err
				}
				return playFile(track.Path)
			})
		},
	}
	return cmd
}

func newLibraryExportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "export <track-id> <path>",
		Short: "Copy a track out of the library",
		Long: `Copy a track out of the library.

The file is copied as it is stored, so the extension of the destination decides
nothing about the format. Use the format the track already has.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withApp(cmd, func(a *app.App) error {
				track, err := a.Track(cmd.Context(), job.ID(args[0]))
				if err != nil {
					return err
				}
				if err := copyTrack(track.Path, args[1]); err != nil {
					return err
				}
				fmt.Printf("wrote %s\n", args[1])
				return nil
			})
		},
	}
	return cmd
}

// renderLibrary prints tracks as a table.
func renderLibrary(tracks []*job.Track) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tTITLE\tLENGTH\tFORMAT\tSIZE\tCREATED")
	for _, t := range tracks {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			t.ID,
			truncate(t.Title, 32),
			humanDuration(t.Duration),
			t.Format,
			humanBytes(t.SizeBytes),
			humanAge(t.CreatedAt),
		)
	}
	return w.Flush()
}

func libraryJSON(tracks []*job.Track) []map[string]any {
	out := make([]map[string]any, len(tracks))
	for i, t := range tracks {
		out[i] = map[string]any{
			"id":          t.ID.String(),
			"job_id":      t.JobID.String(),
			"title":       t.Title,
			"path":        t.Path,
			"format":      t.Format,
			"duration_ms": t.Duration.Milliseconds(),
			"size_bytes":  t.SizeBytes,
			"sample_rate": t.SampleRate,
			"is_stem":     t.IsStem,
			"stem_name":   t.StemName,
			"created_at":  t.CreatedAt,
		}
	}
	return out
}

// playFile hands a file to the desktop's default player.
//
// This deliberately uses the operating system rather than decoding in-process:
// playback is not a feature jonus needs to own, and every desktop already has a
// player that handles seeking and output devices better than a first
// implementation would. An in-process player can replace this later without
// changing the command.
func playFile(path string) error {
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("the audio file is missing: %w", err)
	}

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("afplay", path)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	// afplay blocks until the track ends, which is what a "play" command should
	// do; the others hand off to a player and return immediately.
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("play %s: %w", filepath.Base(path), err)
	}
	return nil
}
