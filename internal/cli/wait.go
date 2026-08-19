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
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "wait <job>",
		Short: "Block until a job finishes; exit with the job's exit code",
		Long: `Blocks until the job leaves the running state, then exits with the job's
own exit code, so "rig wait train && next" behaves like running the job in
the foreground. With --timeout, exits 124 when the wait expires (a job that
itself exits 124 is indistinguishable). Polling runs on the instance, not by
reconnecting per tick; transient ssh failures are retried.`,
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
			return waitJob(a.Target(in), args[0], timeout)
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "give up after this long and exit 124 (default: wait forever)")
	return cmd
}

func waitJob(t sshx.Target, name string, timeout time.Duration) error {
	code, err := remote.WaitJob(t, name, remote.WaitConfig{Timeout: timeout})
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
