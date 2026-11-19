package cli

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/dtuit/rig/internal/core"
	"github.com/dtuit/rig/internal/remote"
	"github.com/dtuit/rig/internal/shellq"
	"github.com/dtuit/rig/internal/sshx"
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
~/.rig/logs/<name>.<run>.log — with <name>.log pointing at the current run —
and recording the run and its exit code in ~/.rig/jobs.jsonl, so an earlier
run's log survives a retry. Each argument is
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
			short := strings.TrimPrefix(in.Name, core.HostPrefix)
			fmt.Printf("job %q started on %s\n  rig wait -i %[2]s %[1]s\n  rig logs -i %[2]s %[1]s -f\n", name, short)
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
	var all bool
	cmd := &cobra.Command{
		Use:   "ps",
		Short: "List jobs on the instance (--all for every recorded run)",
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
			// The default is one row per job, as rig has always shown; the
			// history is there to be asked for.
			if !all {
				jobs = remote.Latest(jobs)
			}
			if jsonFlag {
				return renderJSON(jobs)
			}
			if len(jobs) == 0 {
				fmt.Println("no jobs")
				return nil
			}
			printJobs(jobs, all)
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "every recorded run, not just the latest of each job")
	return cmd
}

// printJobs renders the job table. STARTED and TOOK are blank for a job the
// instance has no record of — one started before the footprint kept one —
// rather than invented from a file's mtime.
func printJobs(jobs []remote.Job, all bool) {
	now := time.Now()
	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	head := []string{"JOB", "STATUS", "STARTED", "TOOK", "COMMAND"}
	if all {
		head = slices.Insert(head, 1, "RUN")
	}
	fmt.Fprintln(w, strings.Join(head, "\t"))
	unrecorded := false
	for _, j := range jobs {
		started, took := "-", "-"
		if !j.Started.IsZero() {
			started = age(j.Started) + " ago"
		}
		if d, ok := j.Took(now); ok {
			took = shortDuration(d)
		}
		if !j.Recorded() {
			unrecorded = true
		}
		row := []string{j.Name, jobStatus(j), started, took, j.Command}
		if all {
			row = slices.Insert(row, 1, runLabel(j))
		}
		fmt.Fprintln(w, strings.Join(row, "\t"))
	}
	w.Flush()
	if unrecorded {
		fmt.Println("\nsome jobs predate this instance's job record: no history for those (rig adopt installs it)")
	}
}

func jobStatus(j remote.Job) string {
	switch {
	case j.Running:
		return "running"
	case j.ExitCode == remote.ExitUnknown:
		return "interrupted"
	case j.ExitCode == 0:
		return "done"
	default:
		return fmt.Sprintf("exited %d", j.ExitCode)
	}
}

func runLabel(j remote.Job) string {
	if !j.Recorded() {
		return "-"
	}
	return j.Started.Local().Format("01-02 15:04")
}

func shortDuration(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

func newLogsCmd() *cobra.Command {
	var (
		follow   bool
		lines    int
		run      int64
		watchdog bool
	)
	cmd := &cobra.Command{
		Use:   "logs [job]",
		Short: "Show a job's log (--run for an earlier run, --watchdog for the TTL log)",
		Long: `Without --watchdog, tails a job's log: the current run, or an earlier one
with --run (rig ps --all lists them).

--watchdog tails the TTL watchdog's own record instead, which takes no job
name. That is what answers "why is this instance still up past its TTL" —
the watchdog writes down every deadline it defers and the job or busy.d hook
it deferred for.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch {
			case watchdog && len(args) > 0:
				return fmt.Errorf("--watchdog takes no job name")
			case !watchdog && len(args) == 0:
				return fmt.Errorf("which job? (rig ps lists them, or --watchdog for the TTL log)")
			}
			a, err := newApp()
			if err != nil {
				return err
			}
			in, err := a.Resolve(cmd.Context(), instanceFlag)
			if err != nil {
				return err
			}
			if watchdog {
				return printWatchdogLog(a.Target(in), lines)
			}
			// Without --run this is the current run, which is what
			// <job>.log points at; the flag reaches an earlier one, which
			// exists only for jobs the record covers.
			if run == 0 {
				return remote.TailLog(a.Target(in), args[0], follow, lines)
			}
			jobs, err := remote.ListJobs(a.Target(in))
			if err != nil {
				return err
			}
			for _, j := range jobs {
				if j.Name == args[0] && j.Run == run {
					return remote.TailRunLog(a.Target(in), j, follow, lines)
				}
			}
			return fmt.Errorf("no run %d of job %s (rig ps --all lists the runs it has)", run, args[0])
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "follow the log")
	cmd.Flags().IntVarP(&lines, "lines", "n", 100, "lines of history")
	cmd.Flags().Int64Var(&run, "run", 0, "run id from rig ps --json (default: the current run)")
	cmd.Flags().BoolVar(&watchdog, "watchdog", false, "the TTL watchdog's log instead of a job's")
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

// printWatchdogLog shows what the TTL watchdog has written down. An empty
// log is the normal state of a young instance — it records arming and then
// nothing until a deadline actually passes — so it says that rather than
// looking like a failure.
func printWatchdogLog(t sshx.Target, lines int) error {
	entries, err := remote.WatchdogLog(t, lines)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		fmt.Println("no watchdog log yet (an instance adopted before rig kept one has none; rig adopt installs it)")
		return nil
	}
	for _, e := range entries {
		fmt.Println(e)
	}
	return nil
}
