package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/dtuit/rig/internal/remote"
)

// adopt finishes (or repairs) initialization of an already-created instance:
// TTL watchdog, ~/.rig layout, volume mounts. Used when `rig up` was
// interrupted between create and init, or after an image-side reset.
func newAdoptCmd() *cobra.Command {
	var ttl string
	cmd := &cobra.Command{
		Use:   "adopt [name]",
		Short: "Install/repair rig's footprint (TTL watchdog, volume mounts) on an existing instance",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp()
			if err != nil {
				return err
			}
			key := instanceFlag
			if len(args) == 1 {
				key = args[0]
			}
			in, err := a.Resolve(cmd.Context(), key)
			if err != nil {
				return err
			}
			if in.IP == "" {
				return fmt.Errorf("%s has no IP yet (status %s)", in.Name, in.Status)
			}
			d, err := time.ParseDuration(ttl)
			if err != nil {
				return fmt.Errorf("bad --ttl: %w", err)
			}
			t := a.Target(in)
			if err := t.WaitForSSH(5 * time.Minute); err != nil {
				return err
			}
			if err := remote.Init(t, d); err != nil {
				return err
			}
			fmt.Printf("%s adopted: TTL %s\n", in.Name, d)
			for _, volID := range in.VolumeIDs {
				if volID == in.OSVolumeID {
					continue
				}
				if err := a.MountVolume(cmd.Context(), t, volID); err != nil {
					fmt.Printf("warning: mounting %s: %v\n", volID, err)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&ttl, "ttl", "12h", "self-destruct TTL from now")
	return cmd
}
