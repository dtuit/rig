// Package cli wires the cobra command tree.
package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/dtuit/rig/internal/config"
	"github.com/dtuit/rig/internal/core"
	"github.com/dtuit/rig/internal/provider/verda"
	"github.com/dtuit/rig/internal/tui"
)

var (
	app          *core.App
	instanceFlag string
	jsonFlag     bool
)

func newApp() (*core.App, error) {
	if app != nil {
		return app, nil
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	id, secret, err := cfg.Credentials()
	if err != nil {
		return nil, err
	}
	prov, err := verda.New(id, secret)
	if err != nil {
		return nil, err
	}
	app = &core.App{Cfg: cfg, Prov: prov, Log: stderrLogf}
	return app, nil
}

// stderrLogf is the CLI's shared progress logger: rig-prefixed, on stderr.
func stderrLogf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "rig: "+format+"\n", a...)
}

func NewRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "rig",
		Short:         "rig — GPU cloud workbench (Verda)",
		Long:          "rig provisions ephemeral GPU instances with persistent volumes,\ngets you (or your agent) onto them, and runs detached jobs that\nsurvive disconnects. Without arguments it opens the dashboard.",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp()
			if err != nil {
				return err
			}
			return tui.Dashboard(cmd.Context(), a)
		},
	}
	root.PersistentFlags().StringVarP(&instanceFlag, "instance", "i", "", "target instance name or ID prefix")
	root.PersistentFlags().BoolVar(&jsonFlag, "json", false, "machine-readable output (status, ps, types, volumes, spend)")

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
