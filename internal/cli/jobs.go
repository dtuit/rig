package cli

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/dtuit/rig/internal/core"
	"github.com/dtuit/rig/internal/remote"
	"github.com/dtuit/rig/internal/shellq"
)

func newRunCmd() *cobra.Command {
	var (
		name    string
		workdir string
		wait    bool
		timeout time.Duration
		pattern string
	)
	cmd := &cobra.Command{
		Use:   "run -- <command ...>",
		Short: "Run a command detached on the instance (survives disconnect)",
		Long: `Runs the command in a named tmux session on the instance, logging to
~/.rig/logs/<name>.log and recording the exit code. Each argument is
shell-quoted, so the job sees exactly the argv you typed. Watch it with
rig logs <name> -f, list with rig ps, kill with rig stop <name>.
With --wait, blocks until the job finishes and exits with its exit code
(equivalent to rig run followed by rig wait); --for and --timeout are
passed through to the wait.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if timeout != 0 && !wait {
				return fmt.Errorf("--timeout requires --wait")
			}
			if pattern != "" && !wait {
				return fmt.Errorf("--for requires --wait")
			}
			a, err := newApp()
			if err != nil {
				return err
			}
			in, err := a.Resolve(cmd.Context(), instanceFlag)
			if err != nil {
				return err
			}
			// issue 10: quote each argument so "A D E" stays one word on the instance.
			command := shellq.Join(args)
			if name == "" {
				name = jobNameFrom(args)
			}
			t := a.Target(in)
			if err := remote.StartJob(t, name, workdir, command); err != nil {
				return err
			}
			// issues 15, 17: short instance name as rig status shows it; wait hint first.
			fmt.Printf("job %q started on %s\n  rig wait %[1]s\n  rig logs %[1]s -f\n",
				name, strings.TrimPrefix(in.Name, core.HostPrefix))
			if wait {
				return waitJob(t, name, timeout, pattern)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "job name (default: derived from command)")
	cmd.Flags().StringVar(&workdir, "workdir", "", "remote working directory")
	cmd.Flags().BoolVar(&wait, "wait", false, "block until the job finishes; exit with its exit code")
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "with --wait: give up after this long and exit 124")
	cmd.Flags().StringVar(&pattern, "for", "", "with --wait: return once a log line matches this extended regex (see rig wait --for)")
	return cmd
}

// jobNameFrom derives a tmux-safe job name from the command.
func jobNameFrom(args []string) string {
	base := args[0]
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	var b strings.Builder
	for _, r := range base {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "job"
	}
	return b.String()
}

func newPsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ps",
		Short: "List jobs on the instance",
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp()
			if err != nil {
				return err
			}
			in, err := a.Resolve(cmd.Context(), instanceFlag)
			if err != nil {
				return err
			}
			jobs, err := remote.ListJobs(a.Target(in))
			if err != nil {
				return err
			}
			if jsonFlag {
				return renderJSON(jobs)
			}
			if len(jobs) == 0 {
				fmt.Println("no jobs")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "JOB\tSTATUS")
			for _, j := range jobs {
				status := "running"
				if !j.Running {
					status = fmt.Sprintf("exited %d", j.ExitCode)
				}
				fmt.Fprintf(w, "%s\t%s\n", j.Name, status)
			}
			return w.Flush()
		},
	}
}

func newLogsCmd() *cobra.Command {
	var (
		follow bool
		lines  int
	)
	cmd := &cobra.Command{
		Use:   "logs <job>",
		Short: "Show a job's log",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp()
			if err != nil {
				return err
			}
			in, err := a.Resolve(cmd.Context(), instanceFlag)
			if err != nil {
				return err
			}
			return remote.TailLog(a.Target(in), args[0], follow, lines)
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "follow the log")
	cmd.Flags().IntVarP(&lines, "lines", "n", 100, "lines of history")
	return cmd
}

func newStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop <job>",
		Short: "Kill a running job",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp()
			if err != nil {
				return err
			}
			in, err := a.Resolve(cmd.Context(), instanceFlag)
			if err != nil {
				return err
			}
			if err := remote.StopJob(a.Target(in), args[0]); err != nil {
				return err
			}
			fmt.Printf("job %q stopped\n", args[0])
			return nil
		},
	}
}
