// Package cli wires the cobra command tree.
package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dtuit/rig/internal/config"
	"github.com/dtuit/rig/internal/core"
	"github.com/dtuit/rig/internal/provider"
	"github.com/dtuit/rig/internal/remote"
	"github.com/dtuit/rig/internal/tui"

	// drivers register themselves with the provider registry on import
	_ "github.com/dtuit/rig/internal/provider/aws"
	_ "github.com/dtuit/rig/internal/provider/runpod"
	_ "github.com/dtuit/rig/internal/provider/vast"
	_ "github.com/dtuit/rig/internal/provider/verda"
)

var (
	app          *core.App
	instanceFlag string
	jsonFlag     bool
	providerFlag string
)

// newApp builds the App for commands that know no profile: the provider is
// --provider, else config provider:, else verda.
func newApp() (*core.App, error) {
	return newAppFor(nil, "")
}

// newAppFor builds the App with a profile's provider pin in the running
// order: --provider → pin → config provider: → verda. cfg may be nil, in
// which case it is loaded; up passes the config it already resolved the
// profile from.
func newAppFor(cfg *config.Config, pin string) (*core.App, error) {
	if app != nil {
		return app, nil
	}
	if cfg == nil {
		var err error
		if cfg, err = config.Load(); err != nil {
			return nil, err
		}
	}
	name := cfg.Provider
	switch {
	case providerFlag != "":
		if pin != "" && pin != providerFlag {
			stderrLogf("--provider %s overrides profile pin %s", providerFlag, pin)
		}
		name = providerFlag
	case pin != "":
		name = pin
	}
	if name == "" {
		name = "verda"
	}
	prov, err := provider.Open(name, cfg.ProviderConfig(name))
	if err != nil {
		return nil, err
	}
	app = &core.App{Cfg: cfg, Prov: prov, Log: stderrLogf}
	return app, nil
}

// configForDisplay is the config for a command that drives no provider:
// `rig spend` reads the local ledger and must keep working with no
// credentials at all, so it never opens a driver. A config that will not
// load is a warning, not a failure — the ledger is still worth printing.
func configForDisplay() *config.Config {
	if app != nil {
		return app.Cfg
	}
	cfg, err := config.Load()
	if err != nil {
		stderrLogf("warning: reading config: %v", err)
		return &config.Config{}
	}
	return cfg
}

// dashboard opens the overview UI over the providers this invocation is
// about, and runs the launch flow whenever the user asks for one from it.
// The loop lives here rather than in tui because a launch needs the plain
// terminal the picker and the launch log want, and because `rig up`'s own
// closing summary is this package's to print.
func dashboard(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	fleet := core.NewFleet(cfg, stderrLogf, dashboardProviders(cfg)...)
	for {
		res, err := tui.Dashboard(ctx, fleet)
		if err != nil {
			return err
		}
		switch res.Outcome {
		case tui.Create:
			in, err := createFromDashboard(ctx, fleet)
			if err != nil {
				fmt.Fprintln(os.Stderr, "rig:", err)
			}
			// A cancelled picker leaves nothing to read, so it goes
			// straight back; a launch or a failure has output the user
			// has to see before the alt screen paints over it.
			if in != nil || err != nil {
				pause()
			}
		case tui.FollowLog:
			// The dashboard cannot follow a log itself: the ssh that does
			// it attaches this process's stdout. So it hands the job back
			// here, exactly as it hands back a create, and ctrl-C on the
			// follower returns to the dashboard.
			if err := followFromDashboard(ctx, fleet, res); err != nil {
				fmt.Fprintln(os.Stderr, "rig:", err)
				pause()
			}
		default:
			return nil
		}
	}
}

// dashboardProviders is which clouds the dashboard shows: just the one
// --provider pins, as it pins every other command, and otherwise every
// provider the config knows — the estate a burn rate has to cover.
func dashboardProviders(cfg *config.Config) []string {
	if providerFlag != "" {
		return []string{providerFlag}
	}
	return core.ProviderNames(cfg)
}

// followFromDashboard tails one run's log until the user interrupts it,
// which is what returns them to the dashboard. An interrupt is the expected
// ending, so it is not an error.
func followFromDashboard(ctx context.Context, fleet *core.Fleet, res tui.Result) error {
	if res.Instance == nil {
		return nil
	}
	app, err := fleet.FindApp(*res.Instance)
	if err != nil {
		return err
	}
	fmt.Printf("following %s on %s — ctrl-C returns to the dashboard\n\n",
		res.Job.Name, strings.TrimPrefix(res.Instance.Name, core.HostPrefix))
	// ctrl-C reaches every process in the foreground group, so without
	// this it kills rig and the promise above is a lie. Notify, not
	// Ignore: a handler is reset to the default across exec, so the ssh
	// child still dies on the interrupt, while SIG_IGN would be inherited
	// and ctrl-C would do nothing at all.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)
	err = remote.TailRunLog(app.Target(res.Instance), res.Job, true, 200)
	if err != nil && ctx.Err() == nil && !isInterrupt(err) {
		return err
	}
	return nil
}

// isInterrupt spots the ssh exit that ctrl-C produces, so quitting the
// follower does not print an error on the way back.
func isInterrupt(err error) bool {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		// 130 is the shell's SIGINT convention; ssh passes it through
		return ee.ExitCode() == 130 || ee.ExitCode() == 255
	}
	return false
}

func pause() {
	fmt.Print("\npress enter to return to the dashboard ")
	bufio.NewReader(os.Stdin).ReadString('\n')
}

// createFromDashboard runs one launch: which cloud (when the fleet drives
// several), then that cloud's picker, then the launch itself. A cancelled
// picker is neither an instance nor an error.
func createFromDashboard(ctx context.Context, fleet *core.Fleet) (*provider.Instance, error) {
	name, err := tui.PickProvider(fleet.Names(), fleet.Cfg.Provider)
	if err != nil || name == "" {
		return nil, err
	}
	a, err := fleet.App(name)
	if err != nil {
		return nil, err
	}
	spec, err := tui.Picker(ctx, a)
	if err != nil || spec == nil {
		return nil, err
	}
	in, err := a.Up(ctx, *spec)
	if err != nil {
		return nil, err
	}
	printUp(in)
	return in, nil
}

// stderrLogf is the CLI's shared progress logger: rig-prefixed, on stderr.
func stderrLogf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "rig: "+format+"\n", a...)
}

func NewRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "rig",
		Short:         "rig — GPU cloud workbench",
		Long:          "rig provisions ephemeral GPU instances with persistent volumes,\ngets you (or your agent) onto them, and runs detached jobs that\nsurvive disconnects. Without arguments it opens the dashboard.",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return dashboard(cmd.Context())
		},
	}
	root.PersistentFlags().StringVarP(&instanceFlag, "instance", "i", "", "target instance name or ID prefix")
	root.PersistentFlags().StringVar(&providerFlag, "provider", "", "cloud provider to drive (default: profile pin, then config provider:, then verda)")
	root.PersistentFlags().BoolVar(&jsonFlag, "json", false, "machine-readable output (status, ps, types, volumes, spend, tunnel status)")

	root.AddCommand(
		newUpCmd(),
		newAdoptCmd(),
		newDownCmd(),
		newStatusCmd(),
		newSpendCmd(),
		newTypesCmd(),
		newImagesCmd(),
		newVolumesCmd(),
		newExtendCmd(),
		newSSHCmd(),
		newTunnelCmd(),
		newTunnelSuperviseCmd(),
		newSyncCmd(),
		newFetchCmd(),
		newRunCmd(),
		newWaitCmd(),
		newPsCmd(),
		newLogsCmd(),
		newStopCmd(),
		newConfigCmd(),
	)
	return root
}

// exitError carries a specific process exit code out of a RunE; Execute
// unwraps it instead of the blanket exit 1. An empty msg exits silently —
// used when the underlying command already wrote its own stderr.
type exitError struct {
	code int
	msg  string
}

func (e exitError) Error() string { return e.msg }

func Execute() {
	if err := NewRoot().Execute(); err != nil {
		if err.Error() != "" {
			fmt.Fprintln(os.Stderr, "rig:", err)
		}
		var ee exitError
		if errors.As(err, &ee) {
			os.Exit(ee.code)
		}
		os.Exit(1)
	}
}
