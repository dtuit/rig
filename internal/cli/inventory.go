package cli

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func newTypesCmd() *cobra.Command {
	var (
		availOnly bool
		spot      bool
		filter    string
	)
	cmd := &cobra.Command{
		Use:   "types",
		Short: "List instance types with pricing and live availability",
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp()
			if err != nil {
				return err
			}
			types, err := a.Prov.InstanceTypes(cmd.Context())
			if err != nil {
				return err
			}
			avail, err := a.Prov.Availability(cmd.Context(), spot)
			if err != nil {
				return err
			}
			where := map[string][]string{}
			for loc, ts := range avail {
				for _, t := range ts {
					where[t] = append(where[t], loc)
				}
			}
			w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "TYPE\tGPU\tMEM\t$/H\tSPOT$/H\tAVAILABLE")
			for _, t := range types {
				locs := where[t.Type]
				if availOnly && len(locs) == 0 {
					continue
				}
				if filter != "" && !strings.Contains(strings.ToLower(t.Type+t.GPU), strings.ToLower(filter)) {
					continue
				}
				slices.Sort(locs)
				availStr := strings.Join(locs, ",")
				if availStr == "" {
					availStr = "-"
				}
				fmt.Fprintf(w, "%s\t%s\t%dGB\t%.2f\t%.2f\t%s\n",
					t.Type, t.GPU, t.MemoryGB, t.PricePerHour, t.SpotPrice, availStr)
			}
			return w.Flush()
		},
	}
	cmd.Flags().BoolVar(&availOnly, "available", false, "only types deployable right now")
	cmd.Flags().BoolVar(&spot, "spot", false, "check spot availability instead of on-demand")
	cmd.Flags().StringVar(&filter, "filter", "", "substring filter on type/GPU")
	return cmd
}

func newImagesCmd() *cobra.Command {
	var forType string
	cmd := &cobra.Command{
		Use:   "images",
		Short: "List OS images (optionally for one instance type)",
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp()
			if err != nil {
				return err
			}
			imgs, err := a.Prov.Images(cmd.Context(), forType)
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "IMAGE\tNAME\tDEFAULT")
			for _, im := range imgs {
				def := ""
				if im.Default {
					def = "*"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\n", im.Name, im.Display, def)
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVar(&forType, "type", "", "limit to images valid for this instance type")
	return cmd
}

func newVolumesCmd() *cobra.Command {
	var (
		rm  bool
		yes bool
	)
	cmd := &cobra.Command{
		Use:   "volumes [name-to-delete]",
		Short: "List persistent volumes (or delete one with --rm)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp()
			if err != nil {
				return err
			}
			vols, err := a.Prov.ListVolumes(cmd.Context())
			if err != nil {
				return err
			}
			if rm {
				if len(args) != 1 {
					return fmt.Errorf("--rm requires a volume name")
				}
				for _, v := range vols {
					if v.Name == args[0] {
						if len(v.InstanceIDs) > 0 {
							return fmt.Errorf("volume %s is attached; rig down first", v.Name)
						}
						if !yes && !confirm(fmt.Sprintf("DELETE volume %s (%dGB, %s) and all its data?", v.Name, v.SizeGB, v.Location)) {
							return fmt.Errorf("aborted")
						}
						if err := a.Prov.DeleteVolume(cmd.Context(), v.ID); err != nil {
							return err
						}
						fmt.Printf("volume %s deleted\n", v.Name)
						return nil
					}
				}
				return fmt.Errorf("no volume named %q", args[0])
			}
			w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tSIZE\tTYPE\tLOC\tSTATUS\tATTACHED")
			for _, v := range vols {
				att := ""
				if len(v.InstanceIDs) > 0 {
					att = fmt.Sprintf("%d instance(s)", len(v.InstanceIDs))
				}
				fmt.Fprintf(w, "%s\t%dGB\t%s\t%s\t%s\t%s\n", v.Name, v.SizeGB, v.Type, v.Location, v.Status, att)
			}
			return w.Flush()
		},
	}
	cmd.Flags().BoolVar(&rm, "rm", false, "delete the named volume (must be detached)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip confirmation")
	return cmd
}

func newConfigCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "config",
		Short: "Show config path and an example configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			a, errApp := newApp()
			if errApp == nil {
				fmt.Printf("config: %s\n", a.Cfg.Path())
				if len(a.Cfg.Profiles) > 0 {
					fmt.Println("profiles:")
					for name := range a.Cfg.Profiles {
						fmt.Printf("  %s\n", name)
					}
				}
				return nil
			}
			fmt.Printf("config not usable (%v)\n\nExample %s:\n%s", errApp, "~/.config/rig/config.yml", exampleConfig)
			return nil
		},
	}
}

const exampleConfig = `provider: verda
credentials_file: ~/secrets/verda.env  # or set VERDA_CLIENT_ID/SECRET
ssh:
  user: root
  public_key: ~/.ssh/id_ed25519.pub
  forward_agent: true
defaults:
  ttl: 12h
profiles:
  train:
    types: [1H100.80S.32V, 1A100.22V]   # first available wins
    spot: false
    volume: { name: rig-data, size_gb: 500 }
    ttl: 24h
    env_files: [~/.config/rig/train.env]
  train-big:
    types: [8H200.141S.176V, 8B200.240V]
    spot: true
    volume: { name: rig-data, size_gb: 500 }
  cheap:
    types: [1A6000.10V, 1L40S.20V]
    spot: true
`
