package cli

import (
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/dtuit/rig/internal/ledger"
)

func newSpendCmd() *cobra.Command {
	var since time.Duration
	cmd := &cobra.Command{
		Use:   "spend",
		Short: "Summarise instance spend from the local ledger",
		Long: `Reads the append-only ledger rig writes on up and down. Advisory only —
the provider's billing is authoritative. Instances torn down by the TTL
watchdog have no down event; their cost is estimated through the TTL and
marked with ~.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			events, err := ledger.Read(ledger.Path())
			if err != nil {
				return err
			}
			var cutoff time.Time
			if since > 0 {
				cutoff = time.Now().Add(-since)
			}
			lines, total := ledger.Summarize(events, cutoff, time.Now())
			if jsonFlag {
				return renderJSON(lines)
			}
			if len(lines) == 0 {
				fmt.Println("no spend recorded")
				return nil
			}
			// one cloud in play: today's table; several: each row names
			// its cloud and each cloud gets a subtotal, so a RunPod pod's
			// cost is never read as Verda's. Same rule as `rig status`
			// (severalProviders), so the two tables never disagree.
			subtotals := map[string]float64{}
			provs := make([]string, 0, len(lines))
			for _, l := range lines {
				subtotals[l.Provider] += l.Cost
				provs = append(provs, l.Provider)
			}
			several := severalProviders(configForDisplay(), provs)
			w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
			head := []string{"NAME", "TYPE", "STARTED", "HOURS", "COST"}
			if several {
				head = slices.Insert(head, 1, "PROVIDER")
			}
			fmt.Fprintln(w, strings.Join(head, "\t"))
			estimated := false
			for _, l := range lines {
				marker := ""
				if l.Estimated {
					marker = "~"
					estimated = true
				}
				cells := []string{
					l.Name, l.Type, l.StartedAt.Local().Format("Jan 02 15:04"),
					fmt.Sprintf("%.1f", l.Hours), fmt.Sprintf("%s$%.2f", marker, l.Cost),
				}
				if several {
					cells = slices.Insert(cells, 1, l.Provider)
				}
				fmt.Fprintln(w, strings.Join(cells, "\t"))
			}
			w.Flush()
			// issue 17: explain the ~ marker only when a row carries it.
			legend := ""
			if estimated {
				legend = " (~ = estimated: no down event, likely TTL teardown)"
			}
			fmt.Println()
			if several {
				provs := make([]string, 0, len(subtotals))
				for p := range subtotals {
					provs = append(provs, p)
				}
				sort.Strings(provs)
				for _, p := range provs {
					fmt.Printf("%s: $%.2f\n", p, subtotals[p])
				}
			}
			fmt.Printf("total: $%.2f%s\n", total, legend)
			return nil
		},
	}
	cmd.Flags().DurationVar(&since, "since", 0, "only spend that ended within this window, e.g. 168h (default: all)")
	return cmd
}
