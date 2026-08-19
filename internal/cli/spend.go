package cli

import (
	"fmt"
	"os"
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
			w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tTYPE\tSTARTED\tHOURS\tCOST")
			for _, l := range lines {
				marker := ""
				if l.Estimated {
					marker = "~"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%.1f\t%s$%.2f\n",
					l.Name, l.Type, l.StartedAt.Local().Format("Jan 02 15:04"), l.Hours, marker, l.Cost)
			}
			w.Flush()
			fmt.Printf("\ntotal: $%.2f (~ = estimated: no down event, likely TTL teardown)\n", total)
			return nil
		},
	}
	cmd.Flags().DurationVar(&since, "since", 0, "only spend that ended within this window, e.g. 168h (default: all)")
	return cmd
}
