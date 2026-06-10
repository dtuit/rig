package cli

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dtuit/rig/internal/config"
)

func newSSHCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ssh [-- cmd ...]",
		Short: "SSH into the instance (agent forwarding on)",
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp()
			if err != nil {
				return err
			}
			in, err := a.Resolve(cmd.Context(), instanceFlag)
			if err != nil {
				return err
			}
			return a.Target(in).Interactive(nil, strings.Join(args, " "))
		},
	}
	return cmd
}

func newTunnelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tunnel [port[:remoteport] ...]",
		Short: "Forward local ports to the instance (blocks until ^C)",
		Long:  "Without arguments, forwards the ports of the profile the instance was created from is unknown,\nso pass ports explicitly: rig tunnel 8000 9000:80",
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp()
			if err != nil {
				return err
			}
			in, err := a.Resolve(cmd.Context(), instanceFlag)
			if err != nil {
				return err
			}
			var forwards [][2]int
			for _, arg := range args {
				l, r, found := strings.Cut(arg, ":")
				if !found {
					r = l
				}
				lp, err1 := strconv.Atoi(l)
				rp, err2 := strconv.Atoi(r)
				if err1 != nil || err2 != nil {
					return fmt.Errorf("bad port spec %q (want port or local:remote)", arg)
				}
				forwards = append(forwards, [2]int{lp, rp})
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
			fmt.Printf("tunneling %s (^C to stop)\n", strings.Join(desc, ", "))
			return a.Target(in).Tunnel(forwards)
		},
	}
	return cmd
}

func newSyncCmd() *cobra.Command {
	var (
		dest       string
		del        bool
		includeGit bool
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
			excludes := []string{".venv", "__pycache__", "node_modules", ".mypy_cache", ".pytest_cache"}
			if !includeGit {
				excludes = append(excludes, ".git")
			}
			t := a.Target(in)
			for _, src := range args {
				src = strings.TrimSuffix(config.ExpandHome(src), "/")
				d := dest
				if d == "" {
					d = "~/" + filepath.Base(src)
				}
				fmt.Printf("sync %s -> %s:%s\n", src, in.Name, d)
				if err := t.Rsync(src+"/", d+"/", excludes, del); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dest, "dest", "", "remote destination (default ~/<basename>)")
	cmd.Flags().BoolVar(&del, "delete", false, "delete remote files not present locally")
	cmd.Flags().BoolVar(&includeGit, "include-git", false, "also sync .git directories")
	return cmd
}
