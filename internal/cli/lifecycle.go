package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/dtuit/rig/internal/config"
	"github.com/dtuit/rig/internal/core"
	"github.com/dtuit/rig/internal/ledger"
	"github.com/dtuit/rig/internal/provider"
	"github.com/dtuit/rig/internal/remote"
	"github.com/dtuit/rig/internal/tui"
)

func newUpCmd() *cobra.Command {
	var (
		name     string
		typesCSV string
		spot     bool
		location string
		image    string
		ttl      string
		volume   string
		volSize  int
		envFiles []string
	)
	cmd := &cobra.Command{
		Use:   "up [profile]",
		Short: "Create an instance (interactive picker without a profile)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp()
			if err != nil {
				return err
			}
			var spec core.UpSpec
			switch {
			case len(args) == 1:
				prof, err := a.Cfg.Profile(args[0])
				if err != nil {
					return err
				}
				spec, err = core.SpecFromProfile(args[0], prof)
				if err != nil {
					return err
				}
			case typesCSV != "":
				d, err := time.ParseDuration(orDefault(ttl, a.Cfg.Defaults.TTL))
				if err != nil {
					return fmt.Errorf("bad --ttl: %w", err)
				}
				spec = core.UpSpec{
					Name:  orDefault(name, "adhoc"),
					Types: strings.Split(typesCSV, ","),
					Spot:  spot, Location: location, Image: image, TTL: d,
					OSGB: a.Cfg.Defaults.OSGB,
				}
				if volume != "" {
					spec.Volume = &config.VolumeSpec{Name: volume, SizeGB: volSize}
				}
			default:
				s, err := tui.Picker(cmd.Context(), a)
				if err != nil {
					return err
				}
				if s == nil {
					return nil // cancelled
				}
				spec = *s
			}
			if name != "" {
				spec.Name = name
			}
			if cmd.Flags().Changed("spot") {
				spec.Spot = spot
			}
			if location != "" {
				spec.Location = location
			}
			// ad-hoc env files ride the same transport as a profile's (issue 08)
			spec.EnvFiles = append(spec.EnvFiles, envFiles...)
			in, err := a.Up(cmd.Context(), spec)
			if err != nil {
				return err
			}
			short := strings.TrimPrefix(in.Name, core.HostPrefix)
			fmt.Printf("\n%s is up: %s (%s, $%.2f/h%s) in %s\n", short, in.IP, in.Type,
				in.PricePerHour, spotTag(in.Spot), in.Location)
			fmt.Printf("  rig ssh -i %s\n  rig run -i %s -- <cmd>\n", short, short)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "instance name (default: profile name)")
	cmd.Flags().StringVar(&typesCSV, "type", "", "instance type preference list, comma-separated (skips profile/TUI)")
	cmd.Flags().BoolVar(&spot, "spot", false, "use spot pricing")
	cmd.Flags().StringVar(&location, "location", "", "pin datacenter, e.g. FIN-01")
	cmd.Flags().StringVar(&image, "image", "", "OS image (default: provider default for the type)")
	cmd.Flags().StringVar(&ttl, "ttl", "", "self-destruct TTL, e.g. 12h")
	cmd.Flags().StringVar(&volume, "volume", "", "persistent volume name to attach (created if missing)")
	cmd.Flags().IntVar(&volSize, "volume-size", 100, "size GB when creating --volume")
	cmd.Flags().StringArrayVar(&envFiles, "env-file", nil, "KEY=VALUE file to push to the instance, repeatable (stacks on the profile's env_files)")
	return cmd
}

func newDownCmd() *cobra.Command {
	var (
		yes        bool
		delVolumes bool
		all        bool
	)
	cmd := &cobra.Command{
		Use:   "down [name]",
		Short: "Delete instance(s); data volumes are kept unless --delete-volumes",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp()
			if err != nil {
				return err
			}
			var targets []string
			if all {
				instances, err := a.Managed(cmd.Context())
				if err != nil {
					return err
				}
				for _, in := range instances {
					if in.Status != "discontinued" && in.Status != "deleting" {
						targets = append(targets, in.Name)
					}
				}
				if len(targets) == 0 {
					fmt.Println("nothing to tear down")
					return nil
				}
			} else {
				key := instanceFlag
				if len(args) == 1 {
					key = args[0]
				}
				// fresh: a teardown must target the instance as it is now, not as cached
				in, err := a.ResolveFresh(cmd.Context(), key)
				if err != nil {
					return err
				}
				targets = []string{in.Name}
			}
			shorts := make([]string, 0, len(targets))
			for _, name := range targets {
				shorts = append(shorts, strings.TrimPrefix(name, core.HostPrefix))
			}
			verb := "delete instance"
			if delVolumes {
				verb = "delete instance AND ATTACHED DATA VOLUMES"
			}
			if !yes && !confirm(fmt.Sprintf("%s %s?", verb, strings.Join(shorts, ", "))) {
				return fmt.Errorf("aborted")
			}
			for i, name := range targets {
				in, err := a.ResolveFresh(cmd.Context(), name)
				if err != nil {
					return err
				}
				stopTunnelsBestEffort(in.Name)
				ev, err := a.Down(cmd.Context(), in, delVolumes)
				if err != nil {
					return fmt.Errorf("deleting %s: %w", shorts[i], err)
				}
				// exact figure (lifetime x price), so no ~: that marks estimates in rig spend
				fmt.Printf("%s deleted (lived %.1fh, $%.2f)\n", shorts[i],
					float64(ev.LifetimeSecs)/3600, ev.Cost)
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip confirmation")
	cmd.Flags().BoolVar(&delVolumes, "delete-volumes", false, "also destroy attached data volumes")
	cmd.Flags().BoolVar(&all, "all", false, "tear down every rig instance")
	return cmd
}

// parseExtendArgs accepts the duration positionally (rig extend grid422 8h)
// or via --by, mirroring how down/logs/stop take their subject (issue 06).
func parseExtendArgs(args []string, by string, byChanged bool) (key string, d time.Duration, err error) {
	var positional string
	switch len(args) {
	case 2:
		key, positional = args[0], args[1]
	case 1:
		if parsed, perr := time.ParseDuration(args[0]); perr == nil {
			d = parsed
			positional = args[0]
		} else {
			key = args[0]
		}
	}
	if positional != "" {
		if byChanged {
			return "", 0, fmt.Errorf("duration given twice (positional %q and --by %s)", positional, by)
		}
		if d == 0 {
			if d, err = time.ParseDuration(positional); err != nil {
				return "", 0, fmt.Errorf("bad duration %q (e.g. 8h, or use --by)", positional)
			}
		}
		return key, d, nil
	}
	if d, err = time.ParseDuration(by); err != nil {
		return "", 0, fmt.Errorf("bad --by: %w", err)
	}
	return key, d, nil
}

func newExtendCmd() *cobra.Command {
	var by string
	cmd := &cobra.Command{
		Use:   "extend [name] [duration]",
		Short: "Push out the self-destruct deadline",
		Args:  cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp()
			if err != nil {
				return err
			}
			key, d, err := parseExtendArgs(args, by, cmd.Flags().Changed("by"))
			if err != nil {
				return err
			}
			if key == "" {
				key = instanceFlag
			}
			in, err := a.ResolveFresh(cmd.Context(), key) // the ledger event wants live fields
			if err != nil {
				return err
			}
			t := a.Target(in)
			base := time.Now()
			if cur, err := remote.Deadline(t); err == nil && cur.After(base) {
				base = cur
			}
			deadline := base.Add(d)
			if err := remote.SetDeadline(t, deadline); err != nil {
				return err
			}
			if err := ledger.Append(ledger.Path(), ledger.ExtendEvent(in, deadline, time.Now())); err != nil {
				fmt.Fprintf(os.Stderr, "rig: warning: spend ledger: %v\n", err)
			}
			fmt.Printf("%s self-destructs %s (in %s)\n", strings.TrimPrefix(in.Name, core.HostPrefix),
				deadline.Format("Mon 15:04"), time.Until(deadline).Round(time.Minute))
			return nil
		},
	}
	cmd.Flags().StringVar(&by, "by", "6h", "extend by this duration")
	return cmd
}

// statusRow is the existing instance struct plus the computed TTL the human
// table already shows (ADR-0002).
type statusRow struct {
	provider.Instance
	TTL string `json:"ttl"`
}

func newStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "status",
		Aliases: []string{"ls"},
		Short:   "List rig instances, burn rate, TTL",
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp()
			if err != nil {
				return err
			}
			instances, err := a.Managed(cmd.Context())
			if err != nil {
				return err
			}
			rows := make([]statusRow, 0, len(instances))
			for _, in := range instances {
				ttl := "-"
				if in.Status == "running" && in.IP != "" {
					if dl, err := remote.Deadline(a.Target(&in)); err == nil {
						ttl = time.Until(dl).Round(time.Minute).String()
					}
				}
				rows = append(rows, statusRow{Instance: in, TTL: ttl})
			}
			if jsonFlag {
				return renderJSON(rows)
			}
			if len(rows) == 0 {
				fmt.Println("no rig instances")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tSTATUS\tTYPE\tIP\tLOC\t$/H\tAGE\tTTL")
			var burn float64
			for _, r := range rows {
				in := r.Instance
				price := fmt.Sprintf("%.2f%s", in.PricePerHour, spotTag(in.Spot))
				if in.Status == "running" {
					burn += in.PricePerHour
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
					strings.TrimPrefix(in.Name, core.HostPrefix), in.Status, in.Type,
					in.IP, in.Location, price, age(in.CreatedAt), r.TTL)
			}
			w.Flush()
			if burn > 0 {
				fmt.Printf("\nburn rate: $%.2f/h ($%.0f/day)\n", burn, burn*24)
			}
			return nil
		},
	}
	return cmd
}

func spotTag(spot bool) string {
	if spot {
		return " spot"
	}
	return ""
}

func age(t time.Time) string {
	d := time.Since(t)
	switch {
	case d > 48*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d > time.Hour:
		return fmt.Sprintf("%.1fh", d.Hours())
	default:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func confirm(prompt string) bool {
	fmt.Printf("%s [y/N] ", prompt)
	sc := bufio.NewScanner(os.Stdin)
	if !sc.Scan() {
		return false
	}
	ans := strings.ToLower(strings.TrimSpace(sc.Text()))
	return ans == "y" || ans == "yes"
}
