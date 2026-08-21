package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

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
refused. Stop a detached tunnel by killing its printed supervisor pid;
state and logs live beside rig's config.`,
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
	return cmd
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

func newSyncCmd() *cobra.Command {
	var (
		dest       string
		del        bool
		includeGit bool
		excludes   []string
	)
	cmd := &cobra.Command{
		Use:   "sync <local-path> [more-paths...]",
		Short: "rsync local directories to the instance",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp()
			if err != nil {
				return err
			}
			in, err := a.Resolve(cmd.Context(), instanceFlag)
			if err != nil {
				return err
			}
			t := a.Target(in)
			for _, src := range args {
				src = strings.TrimSuffix(config.ExpandHome(src), "/")
				d := dest
				if d == "" {
					d = "~/" + filepath.Base(src)
				}
				fmt.Printf("sync %s -> %s:%s\n", src, in.Name, d)
				if err := t.Rsync(src+"/", d+"/", syncExcludes(includeGit, excludes), del); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dest, "dest", "", "remote destination (default ~/<basename>)")
	cmd.Flags().BoolVar(&del, "delete", false, "delete remote files not present locally")
	cmd.Flags().BoolVar(&includeGit, "include-git", false, "also sync .git directories")
	cmd.Flags().StringArrayVar(&excludes, "exclude", nil, "rsync exclude pattern, repeatable (stacks on the defaults)")
	return cmd
}
