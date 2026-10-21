// Command evalwright runs regression tests for LLM prompts against local models.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

// Exit codes, so CI scripts can tell "tests failed" apart from "evalwright is misconfigured".
const (
	exitOK     = 0
	exitFailed = 1
	exitError  = 2
)

// errSuiteFailed means the run completed but at least one case failed.
var errSuiteFailed = errors.New("one or more cases failed")

func main() {
	os.Exit(run())
}

// run is separate from main so deferred cleanup runs before os.Exit.
func run() int {
	// Ctrl-C cancels ctx; the runner then stops and reports what it has so far.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err := newRootCmd().ExecuteContext(ctx)
	switch {
	case err == nil:
		return exitOK
	case errors.Is(err, errSuiteFailed):
		return exitFailed
	default:
		fmt.Fprintln(os.Stderr, "evalwright:", err)
		return exitError
	}
}
