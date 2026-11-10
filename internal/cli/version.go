package cli

import (
	"fmt"
	"runtime"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// Version is the jonus release version. Release builds override it with
// -ldflags "-X github.com/Bo101010/jonus/internal/cli.Version=...".
var Version = "dev"

func newVersionCmd() *cobra.Command {
	var short bool

	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print the jonus version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			if short {
				fmt.Fprintln(out, Version)
				return nil
			}
			fmt.Fprintf(out, "jonus %s\n", Version)
			fmt.Fprintf(out, "  go:   %s\n", runtime.Version())
			fmt.Fprintf(out, "  os:   %s/%s\n", runtime.GOOS, runtime.GOARCH)
			if rev := vcsRevision(); rev != "" {
				fmt.Fprintf(out, "  rev:  %s\n", rev)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&short, "short", false, "print only the version number")
	return cmd
}

// vcsRevision reports the git revision baked in at build time, if any.
func vcsRevision() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" {
			if len(s.Value) > 12 {
				return s.Value[:12]
			}
			return s.Value
		}
	}
	return ""
}
