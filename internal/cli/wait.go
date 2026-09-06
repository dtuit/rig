package cli

import (
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/dtuit/rig/internal/remote"
	"github.com/dtuit/rig/internal/sshx"
)

func newWaitCmd() *cobra.Command {
	var (
		timeout time.Duration
		pattern string
	)
	cmd := &cobra.Command{
		Use:   "wait <job> [--for <regex>]",
		Short: "Block until a job finishes (or its log matches); exit with the job's exit code",
		Long: `Blocks until the job leaves the running state, then exits with the job's
own exit code, so "rig wait train && next" behaves like running the job in
the foreground. With --timeout, exits 124 when the wait expires (a job that
itself exits 124 is indistinguishable). Polling runs on the instance, not by
reconnecting per tick; transient ssh failures are retried.

With --for '<regex>' (extended regex, grep -E), blocks until a line of the
job's log matches, prints that line on stdout and exits 0 — for catching a
stage transition or a progress line mid-run. If the job ends before any
line matches, exits 125; --timeout still exits 124.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp()
			if err != nil {
				return err
			}
			in, err := a.Resolve(cmd.Context(), instanceFlag)
			if err != nil {
				return err
			}
			return waitJob(a.Target(in), args[0], timeout, pattern)
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "give up after this long and exit 124 (default: wait forever)")
	cmd.Flags().StringVar(&pattern, "for", "", "return once a log line matches this extended regex; print it; exit 125 if the job ends first")
	return cmd
}

// waitJob blocks on the job; with a non-empty pattern it waits for a matching
// log line instead of the exit (issue 12).
func waitJob(t sshx.Target, name string, timeout time.Duration, pattern string) error {
	cfg := remote.WaitConfig{Timeout: timeout}
	if pattern != "" {
		line, err := remote.WaitForLine(t, name, pattern, cfg)
		return waitForOutcome(line, err, name)
	}
	code, err := remote.WaitJob(t, name, cfg)
	return waitOutcome(code, err, name)
}

func waitOutcome(code int, err error, name string) error {
	switch {
	case errors.Is(err, remote.ErrWaitTimeout):
		return exitError{124, fmt.Sprintf("timed out waiting for job %q", name)}
	case err != nil:
		return err
	case code != 0:
		return exitError{code, fmt.Sprintf("job %q exited %d", name, code)}
	}
	fmt.Printf("job %q exited 0\n", name)
	return nil
}

// waitForOutcome maps a --for result: the matched line on stdout and exit 0,
// 124 on timeout, 125 when the job ended without a match (ADR-0005).
func waitForOutcome(line string, err error, name string) error {
	switch {
	case errors.Is(err, remote.ErrWaitTimeout):
		return exitError{124, fmt.Sprintf("timed out waiting for job %q", name)}
	case errors.Is(err, remote.ErrNoMatch):
		return exitError{125, fmt.Sprintf("job %q ended before its log matched (%v)", name, err)}
	case err != nil:
		return err
	}
	fmt.Println(line)
	return nil
}
