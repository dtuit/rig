package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/dtuit/rig/internal/config"
	"github.com/dtuit/rig/internal/core"
	"github.com/dtuit/rig/internal/provider"
	"github.com/dtuit/rig/internal/tunnel"
)

// propagateExit turns a subprocess exit status into rig's own exit code
// (issue 07): rig ssh -- <cmd> exits with the remote command's code. The
// message stays empty — the remote command already wrote its own stderr.
func propagateExit(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() > 0 {
		return exitError{code: ee.ExitCode()}
	}
	return err
}

func newSSHCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ssh [-- cmd ...]",
		Short: "SSH into the instance (agent forwarding on)",
		Long: `Opens an interactive session, or runs the given command and exits with
its exit code — so "rig ssh -- test -f done && next" works. Exit 255 means
ssh itself failed (unreachable instance) OR the remote command tore down its
own connection (e.g. pkill on its process tree); don't chain on commands
that kill their own session.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp()
			if err != nil {
				return err
			}
			in, err := a.Resolve(cmd.Context(), instanceFlag)
			if err != nil {
				return err
			}
			return propagateExit(a.Target(in).Interactive(nil, strings.Join(args, " ")))
		},
	}
	return cmd
}

// parseForwards turns port specs ("8000", "9000:80") into local:remote pairs.
func parseForwards(args []string) ([][2]int, error) {
	var forwards [][2]int
	for _, arg := range args {
		l, r, found := strings.Cut(arg, ":")
		if !found {
			r = l
		}
		lp, err1 := strconv.Atoi(l)
		rp, err2 := strconv.Atoi(r)
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("bad port spec %q (want port or local:remote)", arg)
		}
		forwards = append(forwards, [2]int{lp, rp})
	}
	return forwards, nil
}

func newTunnelCmd() *cobra.Command {
	var detach bool
	cmd := &cobra.Command{
		Use:   "tunnel [port[:remoteport] ...]",
		Short: "Forward local ports to the instance (blocks until ^C)",
		Long: `Forwards each local port to the instance (port, or local:remote).
Without arguments, falls back to the ports of the profile whose name
matches the instance's; pass ports explicitly otherwise: rig tunnel 8000 9000:80.
Foreground by default: blocks until ^C.

--detach instead ensures a supervised background tunnel and returns once
the forwards are actually usable — exit 0 means the ports work now.
Idempotent: re-ensuring a live tunnel is a no-op, so it is safe to run at
the start of any workflow. A local port already forwarded elsewhere is
refused. Verify with "rig tunnel status" (--json for machines), release
with "rig tunnel stop"; state and logs live beside rig's config.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp()
			if err != nil {
				return err
			}
			in, err := a.Resolve(cmd.Context(), instanceFlag)
			if err != nil {
				return err
			}
			forwards, err := parseForwards(args)
			if err != nil {
				return err
			}
			if len(forwards) == 0 {
				// fall back to ports of any profile matching the instance name
				name := strings.TrimPrefix(in.Name, "rig-")
				if p, err := a.Cfg.Profile(name); err == nil {
					for _, port := range p.Ports {
						forwards = append(forwards, [2]int{port, port})
					}
				}
				if len(forwards) == 0 {
					return fmt.Errorf("no ports given and profile %q has none", name)
				}
			}
			var desc []string
			for _, f := range forwards {
				desc = append(desc, fmt.Sprintf("localhost:%d -> %s:%d", f[0], in.IP, f[1]))
			}
			if detach {
				m := &tunnel.Manager{Dir: tunnel.Dir()}
				var fwds []tunnel.Forward
				for _, f := range forwards {
					fwds = append(fwds, tunnel.Forward{Local: f[0], Remote: f[1]})
				}
				ep := tunnel.Endpoint{Instance: in.Name, ID: in.ID, IP: in.IP}
				st, already, err := m.Ensure(ep, fwds)
				if err != nil {
					return err
				}
				if already {
					fmt.Printf("tunnel to %s already up (%s)\n", in.Name, strings.Join(desc, ", "))
				} else {
					fmt.Printf("tunnel up: %s (supervisor pid %d)\n", strings.Join(desc, ", "), st.PID)
				}
				return nil
			}
			fmt.Printf("tunneling %s (^C to stop)\n", strings.Join(desc, ", "))
			return a.Target(in).Tunnel(forwards)
		},
	}
	cmd.Flags().BoolVar(&detach, "detach", false, "ensure a supervised background tunnel; exit 0 once the forwards are usable (idempotent)")
	cmd.AddCommand(newTunnelStatusCmd(), newTunnelStopCmd())
	return cmd
}

// forwardsDesc renders a tunnel's forwards compactly: "8000,9090->80".
func forwardsDesc(st tunnel.State) string {
	var parts []string
	for _, f := range st.Forwards {
		if f.Local == f.Remote {
			parts = append(parts, strconv.Itoa(f.Local))
		} else {
			parts = append(parts, fmt.Sprintf("%d->%d", f.Local, f.Remote))
		}
	}
	return strings.Join(parts, ",")
}

func newTunnelStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "List detached tunnels (instance, forwards, health, supervisor)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			m := &tunnel.Manager{Dir: tunnel.Dir()}
			states, err := m.List()
			if err != nil {
				return err
			}
			return renderTunnels(states)
		},
	}
}

// renderTunnels is the render half of the gather/render seam: the listed
// states go to exactly one renderer — the structs themselves as JSON
// (ADR-0002), or the human table.
func renderTunnels(states []tunnel.State) error {
	if jsonFlag {
		return renderJSON(states)
	}
	if len(states) == 0 {
		fmt.Println("no detached tunnels")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "INSTANCE\tIP\tFORWARDS\tPHASE\tPID\tAGE")
	for _, st := range states {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\n",
			strings.TrimPrefix(st.Instance, core.HostPrefix), st.IP,
			forwardsDesc(st), st.Phase, st.PID, age(st.StartedAt))
	}
	return w.Flush()
}

// resolveTunnelInstance picks which instance's tunnels a stop targets, from
// local state alone — no provider call, so releasing ports still works when
// the instance is already gone or the provider is unreachable. An empty
// result with no error means nothing is running: already the desired state.
func resolveTunnelInstance(states []tunnel.State, key string) (string, error) {
	if key != "" {
		full := key
		if !strings.HasPrefix(key, core.HostPrefix) {
			full = core.HostPrefix + key
		}
		// honor -i's name-or-ID-prefix contract against the recorded state
		for _, st := range states {
			if st.Instance == full || strings.HasPrefix(st.ID, key) {
				return st.Instance, nil
			}
		}
		return full, nil // no live tunnel for it: stop becomes a no-op
	}
	seen := map[string]bool{}
	var names []string
	for _, st := range states {
		if !seen[st.Instance] {
			seen[st.Instance] = true
			names = append(names, st.Instance)
		}
	}
	switch len(names) {
	case 0:
		return "", nil
	case 1:
		return names[0], nil
	default:
		var short []string
		for _, n := range names {
			short = append(short, strings.TrimPrefix(n, core.HostPrefix))
		}
		return "", fmt.Errorf("tunnels to multiple instances (%s); pick one with -i", strings.Join(short, ", "))
	}
}

func newTunnelStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop [port ...]",
		Short: "Stop the instance's detached tunnels (all, or just these local ports)",
		Long: `Stops the resolved instance's detached tunnels — every one of them, or
only the supervisors covering the given local ports. Stopping what is
already stopped is success, so the command is idempotent like ensure.
Resolution uses the recorded tunnel state, not the provider: ports can be
released even after the instance is gone.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			var ports []int
			for _, arg := range args {
				p, err := strconv.Atoi(arg)
				if err != nil {
					return fmt.Errorf("bad port %q", arg)
				}
				ports = append(ports, p)
			}
			m := &tunnel.Manager{Dir: tunnel.Dir()}
			states, err := m.List()
			if err != nil {
				return err
			}
			instance, err := resolveTunnelInstance(states, instanceFlag)
			if err != nil {
				return err
			}
			if instance == "" {
				fmt.Println("no detached tunnels to stop")
				return nil
			}
			stopped, err := m.StopInstance(instance, ports)
			reportStopped(stopped)
			if err != nil {
				return err
			}
			if len(stopped) == 0 {
				fmt.Println("no detached tunnels to stop")
			}
			return nil
		},
	}
}

func reportStopped(stopped []tunnel.State) {
	for _, st := range stopped {
		fmt.Printf("stopped tunnel to %s (%s)\n",
			strings.TrimPrefix(st.Instance, core.HostPrefix), forwardsDesc(st))
	}
}

// stopTunnelsBestEffort releases an instance's detached tunnels for rig
// down: a failure warns but never blocks the teardown.
func stopTunnelsBestEffort(name string) {
	m := &tunnel.Manager{Dir: tunnel.Dir()}
	stopped, err := m.StopInstance(name, nil)
	reportStopped(stopped)
	if err != nil {
		stderrLogf("warning: stopping tunnels for %s: %v", name, err)
	}
}

// newTunnelSuperviseCmd is the hidden supervisor entry point --detach
// re-execs (tunnel.Launcher). It rebuilds the ssh target from config alone —
// the state carries the pinned IP, so no provider call or credentials are
// needed.
func newTunnelSuperviseCmd() *cobra.Command {
	return &cobra.Command{
		Use:    tunnel.SuperviseCommand + " <state-json>",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var st tunnel.State
			if err := json.Unmarshal([]byte(args[0]), &st); err != nil {
				return fmt.Errorf("bad state argument: %w", err)
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			a := &core.App{Cfg: cfg, Log: stderrLogf}
			return tunnel.Supervise(tunnel.Dir(), st, a.Target(&provider.Instance{IP: st.IP}))
		},
	}
}

// syncExcludes returns the rsync exclude patterns: the defaults (per-machine
// state that must not cross instances) always apply, user patterns stack on
// top.
func syncExcludes(includeGit bool, extra []string) []string {
	// .env: local config; syncing one environment's paths onto another breaks things
	excludes := []string{".venv", "__pycache__", "node_modules", ".mypy_cache", ".pytest_cache", ".env"}
	if !includeGit {
		excludes = append(excludes, ".git")
	}
	return append(excludes, extra...)
}

// syncPair is one local source and where it lands on the instance, in the
// exact form rsync receives them: a directory carries a trailing slash on
// both sides (copy contents into dest), a file carries none (dest is the
// file's remote path).
type syncPair struct {
	Src, Dest string
}

// splitSyncArg splits a "src:dest" argument on its last colon, but only when
// what follows looks like a remote path (starts with / or ~) — so a bare
// path, a Windows drive letter, or an odd filename with a colon stays a
// plain source. dest is "" when the argument carries none.
func splitSyncArg(arg string) (src, dest string) {
	i := strings.LastIndex(arg, ":")
	if i < 0 {
		return arg, ""
	}
	if rest := arg[i+1:]; strings.HasPrefix(rest, "/") || strings.HasPrefix(rest, "~") {
		return arg[:i], rest
	}
	return arg, ""
}

// planSync turns one argument into its rsync pair. defaultDest is --dest
// (empty means ~/<basename>) and applies to arguments without their own;
// isDir reports whether the local source is a directory (issue 14: a file
// source must not grow a trailing slash, or rsync fails on "file/").
func planSync(arg, defaultDest string, isDir bool) syncPair {
	src, dest := splitSyncArg(arg)
	src = strings.TrimSuffix(config.ExpandHome(src), "/")
	if dest == "" {
		dest = defaultDest
	}
	if dest == "" {
		dest = "~/" + filepath.Base(src)
	}
	if isDir {
		return syncPair{Src: src + "/", Dest: strings.TrimSuffix(dest, "/") + "/"}
	}
	return syncPair{Src: src, Dest: dest}
}

// showProgress decides whether rsync streams progress (issue 09): --quiet
// always wins, --progress forces it on, and otherwise only a terminal
// watching a single stream gets it — piped output (an agent reading it) and
// parallel streams (interleaved bars) do not.
func showProgress(quiet, force, tty bool, jobs int) bool {
	switch {
	case quiet:
		return false
	case force:
		return true
	default:
		return tty && jobs <= 1
	}
}

// stdoutIsTTY reports whether stdout is a terminal.
func stdoutIsTTY() bool {
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func newSyncCmd() *cobra.Command {
	var (
		dest       string
		del        bool
		includeGit bool
		excludes   []string
		jobs       int
		quiet      bool
		progress   bool
	)
	cmd := &cobra.Command{
		Use:   "sync <local-path>[:remote-path] [more...]",
		Short: "rsync local directories or files to the instance",
		Long: `rsyncs each local path to the instance. A directory's contents land in
the destination (default ~/<basename>); a file lands at the destination
path itself. Each argument may carry its own destination as src:dest
(the part after the last colon must start with / or ~); --dest covers
the rest. --jobs runs that many transfers at once — three or four
streams saturate a typical link. Progress output is shown only on a
terminal running a single stream; --quiet drops it, --progress forces it.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if jobs < 1 {
				return fmt.Errorf("--jobs must be at least 1")
			}
			var plans []syncPair
			for _, arg := range args {
				src, _ := splitSyncArg(arg)
				fi, err := os.Stat(config.ExpandHome(src))
				if err != nil {
					return fmt.Errorf("sync %s: %w", src, err)
				}
				plans = append(plans, planSync(arg, dest, fi.IsDir()))
			}
			a, err := newApp()
			if err != nil {
				return err
			}
			in, err := a.Resolve(cmd.Context(), instanceFlag)
			if err != nil {
				return err
			}
			t := a.Target(in)
			short := strings.TrimPrefix(in.Name, core.HostPrefix)
			excl := syncExcludes(includeGit, excludes)
			withProgress := showProgress(quiet, progress, stdoutIsTTY(), jobs)
			if jobs > 1 {
				// separate TCP connections, or the streams share one multiplexed window
				t.NoMultiplex = true
			}

			// bounded fan-out; every failure is collected and reported, not just the first
			var (
				wg   sync.WaitGroup
				sem  = make(chan struct{}, jobs)
				mu   sync.Mutex
				errs []error
			)
			for _, p := range plans {
				sem <- struct{}{}
				wg.Add(1)
				go func(p syncPair) {
					defer wg.Done()
					defer func() { <-sem }()
					fmt.Printf("sync %s -> %s:%s\n", strings.TrimSuffix(p.Src, "/"), short, strings.TrimSuffix(p.Dest, "/"))
					if err := t.Rsync(p.Src, p.Dest, excl, del, withProgress); err != nil {
						mu.Lock()
						errs = append(errs, fmt.Errorf("sync %s: %w", strings.TrimSuffix(p.Src, "/"), err))
						mu.Unlock()
					}
				}(p)
			}
			wg.Wait()
			return errors.Join(errs...)
		},
	}
	cmd.Flags().StringVar(&dest, "dest", "", "remote destination for paths without their own src:dest (default ~/<basename>)")
	cmd.Flags().BoolVar(&del, "delete", false, "delete remote files not present locally")
	cmd.Flags().BoolVar(&includeGit, "include-git", false, "also sync .git directories")
	cmd.Flags().StringArrayVar(&excludes, "exclude", nil, "rsync exclude pattern, repeatable (stacks on the defaults)")
	cmd.Flags().IntVar(&jobs, "jobs", 1, "transfers to run concurrently")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "no progress output (automatic when stdout is not a terminal)")
	cmd.Flags().BoolVar(&progress, "progress", false, "force progress output even when piped or parallel")
	return cmd
}
