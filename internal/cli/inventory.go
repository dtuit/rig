package cli

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/dtuit/rig/internal/provider"
)

// typeRow is the existing instance-type struct plus the computed availability
// column the human table already shows (ADR-0002).
type typeRow struct {
	provider.InstanceType
	Available []string `json:"available"`
}

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
			rows := []typeRow{}
			for _, t := range types {
				locs := where[t.Type]
				if availOnly && len(locs) == 0 {
					continue
				}
				if filter != "" && !strings.Contains(strings.ToLower(t.Type+t.GPU), strings.ToLower(filter)) {
					continue
				}
				slices.Sort(locs)
				rows = append(rows, typeRow{InstanceType: t, Available: locs})
			}
			// issue 16: deployable types first so a truncated read still shows
			// them; provider order is kept within each group.
			slices.SortStableFunc(rows, func(a, b typeRow) int {
				return boolRank(len(a.Available) > 0) - boolRank(len(b.Available) > 0)
			})
			if jsonFlag {
				return renderJSON(rows)
			}
			fmt.Printf("provider: %s\n", a.Provider())
			w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "TYPE\tGPU\tMEM\t$/H\tSPOT$/H\tAVAILABLE")
			unavailable := 0
			hasSpot := a.Prov.Capabilities().Spot
			for _, r := range rows {
				availStr := strings.Join(r.Available, ",")
				if availStr == "" {
					availStr = "-"
					unavailable++
				}
				// A cloud without spot capacity leaves the column empty
				// rather than quoting 0.00 as if it were a price.
				spotStr := ""
				if hasSpot {
					spotStr = fmt.Sprintf("%.2f", r.SpotPrice)
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%.2f\t%s\t%s\n",
					r.Type, r.GPU, r.MemoryText(), r.PricePerHour, spotStr, availStr)
			}
			if err := w.Flush(); err != nil {
				return err
			}
			if unavailable > 0 && !availOnly {
				fmt.Printf("%d types unavailable now; rig types --available hides them\n", unavailable)
				// A cloud that sells the same hardware on-demand and by
				// bid answers availability per market, so a priced row
				// with no locations is not a contradiction: Vast quotes a
				// shape whose only offers are bids ("N offers (bid only)"
				// in the picker) and lists it nowhere here.
				if hasSpot && !spot {
					fmt.Println("availability is per market: rig types --spot checks the spot one")
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&availOnly, "available", false, "only types deployable right now")
	cmd.Flags().BoolVar(&spot, "spot", false, "check spot availability instead of on-demand")
	cmd.Flags().StringVar(&filter, "filter", "", "substring filter on type/GPU")
	return cmd
}

// boolRank orders true before false in a sort comparator.
func boolRank(b bool) int {
	if b {
		return 0
	}
	return 1
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
			fmt.Printf("provider: %s\n", a.Provider())
			w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "IMAGE\tNAME\tDEFAULT")
			for _, im := range imgs {
				def := ""
				if im.Default {
					def = "*"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\n", im.Name, im.Display, def)
			}
			if err := w.Flush(); err != nil {
				return err
			}
			// A Docker-image cloud has no image catalogue to list: the rows
			// above are only rig's default and whatever the config names.
			if a.Prov.Capabilities().DockerImages {
				fmt.Println("any image reference is accepted with --image / image:")
			}
			return nil
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
			if jsonFlag && !rm {
				return renderJSON(vols)
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

const exampleConfig = `provider: verda                        # default; profiles may pin their own, --provider overrides
credentials_file: ~/secrets/verda.env  # or set VERDA_CLIENT_ID/SECRET
providers:                             # per-provider blocks; env vars always win over credentials_file
  verda:
    credentials_file: ~/secrets/verda.env  # same as the top-level line above
  runpod:
    credentials_file: ~/secrets/runpod.env # RUNPOD_API_KEY (or RUNPOD_KEY)
    cloud: secure                          # or community
    image: runpod/pytorch:1.2.0-rc.162-cu1290-torch291-ubuntu2404  # any Docker reference
    disk_gb: 20                            # container disk (wiped on restart)
  vast:
    credentials_file: ~/secrets/vast.env   # VAST_API_KEY
    min_reliability: 0.95                  # host reliability floor, 0..1
    bid_markup: 1.2                        # spot bid = min_bid x markup
    direct_only: false                     # true rents only hosts with open ports
    image: vastai/pytorch:cuda-12.8.1-auto # any Docker reference
    disk_gb: 20                            # container disk
    filters: { cuda_max_good: { gte: 12.8 } }  # extra offer-query clauses
  aws:
    profile: default                       # SDK credential chain; AWS_PROFILE also honoured
    region: ap-southeast-2                 # one region per block
    security_group: rig                    # created in the launch VPC, opens 22/tcp
    ssh_cidrs: []                          # empty = only your current public IP, as a /32
    vpc: ""                                # launch VPC; empty needs a default VPC in the region
    subnets: []                            # explicit subnet ids, at most one per zone
    default_image: ""                      # SSM parameter path (/aws/...) or ami-xxxx
    volume_type: gp3                       # EBS type for data volumes
    families: [g6, g6e, p5]                # instance families rig types lists
ssh:
  user: root                           # optional override of the provider's login user
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
  pod:
    provider: runpod                    # pin this profile to another cloud
    types: [1xA100-SXM]
`
