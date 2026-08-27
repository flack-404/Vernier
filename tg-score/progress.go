package main

import (
	"fmt"
	"os"
)

// stderrIsTTY reports whether progress output is going to a terminal.
//
// Progress lines are written with a carriage return so they overwrite in place.
// Redirected to a file or a pipe that produces one enormous line of every
// intermediate count, which is unreadable in a log and pointless in CI, so when
// stderr is not a terminal the counters are suppressed entirely.
var stderrIsTTY = func() bool {
	fi, err := os.Stderr.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}()

// progressFn returns a progress callback that redraws in place on a terminal and
// stays silent otherwise. every controls how often it reports.
func progressFn(label string, every int) func(done, total int) {
	if !stderrIsTTY {
		return nil
	}
	if every < 1 {
		every = 1
	}
	return func(done, total int) {
		if done != total && done%every != 0 {
			return
		}
		fmt.Fprintf(os.Stderr, "\r  %s %d / %d", label, done, total)
	}
}

// clearProgress erases the in-place progress line.
func clearProgress() {
	if stderrIsTTY {
		fmt.Fprintf(os.Stderr, "\r%-52s\r", "")
	}
}
