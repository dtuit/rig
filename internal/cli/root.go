// Package cli wires the cobra command tree.
package cli

import (
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
	app = &core.App{
		Cfg:  cfg,
		Prov: prov,
		Log: func(format string, a ...any) {
			fmt.Fprintf(os.Stderr, "rig: "+format+"\n", a...)
		},
	}
	return app, nil
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

	root.AddCommand(
		newUpCmd(),
		newAdoptCmd(),
		newDownCmd(),
		newStatusCmd(),
		newTypesCmd(),
		newImagesCmd(),
		newVolumesCmd(),
		newExtendCmd(),
		newSSHCmd(),
		newTunnelCmd(),
		newSyncCmd(),
		newRunCmd(),
		newPsCmd(),
		newLogsCmd(),
		newStopCmd(),
		newConfigCmd(),
	)
	return root
}

func Execute() {
	if err := NewRoot().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "rig:", err)
		os.Exit(1)
	}
}
