// Command jonus is the entry point for the jonus local AI music studio.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/Bo101010/jonus/internal/cli"
)

func main() {
	os.Exit(run())
}

// run keeps the process exit in one place so that deferred cleanup inside the
// command tree still executes before the process ends.
func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := cli.Execute(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "jonus:", err)
		return 1
	}
	return 0
}
