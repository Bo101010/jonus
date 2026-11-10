package cli

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/shaolianbo/jonus/internal/app"
	"github.com/shaolianbo/jonus/internal/job"
)

func newJobsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "jobs",
		Short: "Inspect and manage generation jobs",
		Args:  cobra.NoArgs,
	}

	cmd.AddCommand(
		newJobsListCmd(),
		newJobsShowCmd(),
		newJobsCancelCmd(),
		newJobsRetryCmd(),
		newJobsDeleteCmd(),
	)
	return cmd
}

func newJobsListCmd() *cobra.Command {
	var (
		states []string
		limit  int
		asJSON bool
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List jobs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			filter, err := parseStates(states)
			if err != nil {
				return err
			}
			filter.Limit = limit

			return withApp(cmd, func(a *app.App) error {
				jobs, err := a.ListJobs(cmd.Context(), filter)
				if err != nil {
					return err
				}

				if asJSON {
					views := make([]map[string]any, len(jobs))
					for i, j := range jobs {
						views[i] = jobSummary(j)
					}
					return printJSON(map[string]any{"jobs": views})
				}

				if len(jobs) == 0 {
					fmt.Println("no jobs yet; create one with: jonus generate \"轻快的民谣\"")
					return nil
				}

				w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
				fmt.Fprintln(w, "ID\tSTATE\tPROGRESS\tTITLE\tCREATED")
				for _, j := range jobs {
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
						j.ID,
						j.State,
						progressCell(j),
						truncate(j.Title, 32),
						humanAge(j.CreatedAt),
					)
				}
				return w.Flush()
			})
		},
	}

	flags := cmd.Flags()
	flags.StringSliceVar(&states, "state", nil, "only show these states (repeatable)")
	flags.IntVar(&limit, "limit", 20, "maximum number of jobs to show (0 for all)")
	flags.BoolVar(&asJSON, "json", false, "print machine-readable JSON")

	return cmd
}

func newJobsShowCmd() *cobra.Command {
	var (
		follow bool
		asJSON bool
	)

	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show one job, optionally following it until it finishes",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withApp(cmd, func(a *app.App) error {
				id := job.ID(args[0])

				if !follow {
					detail, err := a.Job(cmd.Context(), id)
					if err != nil {
						return err
					}
					if asJSON {
						return printJSON(map[string]any{
							"job":    jobSummary(detail.Job),
							"tracks": trackSummaries(detail),
						})
					}
					return renderJobDetail(detail)
				}

				// Following runs the queue too, so a job left pending by a
				// `generate` that did not wait still gets run.
				stop := runQueue(cmd.Context(), a)
				defer stop()

				return followJob(cmd.Context(), a, id, "", asJSON)
			})
		},
	}

	flags := cmd.Flags()
	flags.BoolVar(&follow, "follow", false,
		"wait for the job to finish, showing progress (runs the queue while waiting)")
	flags.BoolVar(&asJSON, "json", false, "print machine-readable JSON")

	return cmd
}

func newJobsCancelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cancel <id>",
		Short: "Cancel a job",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withApp(cmd, func(a *app.App) error {
				id := job.ID(args[0])
				if err := a.CancelJob(cmd.Context(), id); err != nil {
					return err
				}

				// Report the resulting state rather than assuming: a job that
				// had already started keeps running until its worker notices.
				detail, err := a.Job(cmd.Context(), id)
				if err != nil {
					return err
				}
				fmt.Printf("%s is %s\n", id, detail.Job.State)
				return nil
			})
		},
	}
	return cmd
}

func newJobsRetryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "retry <id>",
		Short: "Run a finished job again as a new job",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withApp(cmd, func(a *app.App) error {
				retried, err := a.RetryJob(cmd.Context(), job.ID(args[0]))
				if err != nil {
					return err
				}
				fmt.Printf("queued %s  %s\n", retried.ID, retried.Title)
				return nil
			})
		},
	}
	return cmd
}

func newJobsDeleteCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete a job, its tracks, and their files",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withApp(cmd, func(a *app.App) error {
				id := job.ID(args[0])

				if !force {
					detail, err := a.Job(cmd.Context(), id)
					if err != nil {
						return err
					}
					fmt.Printf("delete %s (%s) and %d track(s)? [y/N] ",
						id, detail.Job.Title, len(detail.Tracks))

					var answer string
					fmt.Scanln(&answer)
					if !strings.HasPrefix(strings.ToLower(answer), "y") {
						fmt.Println("not deleted")
						return nil
					}
				}

				if err := a.DeleteJob(cmd.Context(), id); err != nil {
					return err
				}
				fmt.Printf("deleted %s\n", id)
				return nil
			})
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

// renderJobDetail prints a job and its tracks.
func renderJobDetail(detail *app.JobDetail) error {
	j := detail.Job

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "id\t%s\n", j.ID)
	fmt.Fprintf(w, "title\t%s\n", j.Title)
	fmt.Fprintf(w, "state\t%s\n", j.State)
	fmt.Fprintf(w, "engine\t%s\n", j.Engine)
	fmt.Fprintf(w, "kind\t%s\n", j.Kind)
	fmt.Fprintf(w, "seed\t%d\n", j.Seed)
	if j.Duration > 0 {
		fmt.Fprintf(w, "requested\t%s\n", j.Duration)
	}
	if j.Phase != "" {
		fmt.Fprintf(w, "progress\t%s %d%%\n", j.Phase, int(j.Progress*100))
	}
	if j.Attempt > 1 {
		fmt.Fprintf(w, "attempt\t%d\n", j.Attempt)
	}
	fmt.Fprintf(w, "created\t%s\n", j.CreatedAt.Format(time.RFC3339))
	if j.Error != "" {
		fmt.Fprintf(w, "error\t%s\n", j.Error)
	}
	if err := w.Flush(); err != nil {
		return err
	}

	if len(detail.Tracks) == 0 {
		return nil
	}

	fmt.Println()
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TRACK\tKIND\tLENGTH\tSIZE\tPATH")
	for _, t := range detail.Tracks {
		kind := "track"
		if t.IsStem {
			kind = "stem:" + t.StemName
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			t.ID, kind, humanDuration(t.Duration), humanBytes(t.SizeBytes), t.Path)
	}
	return tw.Flush()
}

// jobSummary is the JSON shape of a job for the CLI.
func jobSummary(j *job.Job) map[string]any {
	return map[string]any{
		"id":          j.ID.String(),
		"kind":        j.Kind,
		"state":       string(j.State),
		"engine":      j.Engine,
		"title":       j.Title,
		"phase":       j.Phase,
		"progress":    j.Progress,
		"duration_ms": j.Duration.Milliseconds(),
		"seed":        j.Seed,
		"attempt":     j.Attempt,
		"error":       j.Error,
		"created_at":  j.CreatedAt,
		"started_at":  j.StartedAt,
		"finished_at": j.FinishedAt,
	}
}

func trackSummaries(detail *app.JobDetail) []map[string]any {
	out := make([]map[string]any, len(detail.Tracks))
	for i, t := range detail.Tracks {
		out[i] = map[string]any{
			"id":          t.ID.String(),
			"title":       t.Title,
			"path":        t.Path,
			"format":      t.Format,
			"duration_ms": t.Duration.Milliseconds(),
			"size_bytes":  t.SizeBytes,
			"sample_rate": t.SampleRate,
			"is_stem":     t.IsStem,
			"stem_name":   t.StemName,
		}
	}
	return out
}

// parseStates validates a repeated --state flag.
func parseStates(states []string) (job.Filter, error) {
	var f job.Filter
	for _, raw := range states {
		// Allow both "--state running --state pending" and "--state running,pending".
		for _, part := range strings.Split(raw, ",") {
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
	}
	return f, nil
}

// progressCell renders a job's progress for the list view.
func progressCell(j *job.Job) string {
	switch {
	case j.State == job.StateSucceeded:
		return "100%"
	case j.State == job.StateRunning:
		return fmt.Sprintf("%s %d%%", j.Phase, int(j.Progress*100))
	case j.State == job.StatePending:
		return "queued"
	default:
		return "-"
	}
}

func truncate(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit-1]) + "…"
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	value := float64(n)
	for _, suffix := range []string{"KiB", "MiB", "GiB"} {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.1f %s", value, suffix)
		}
	}
	return fmt.Sprintf("%.1f TiB", value/unit)
}

// humanAge renders how long ago something happened.
func humanAge(t time.Time) string {
	ago := time.Since(t)
	switch {
	case ago < time.Minute:
		return "just now"
	case ago < time.Hour:
		return fmt.Sprintf("%dm ago", int(ago.Minutes()))
	case ago < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(ago.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(ago.Hours()/24))
	}
}
