// Package tui implements the interactive picker (rig up) and dashboard (rig).
package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/huh"

	"github.com/dtuit/rig/internal/config"
	"github.com/dtuit/rig/internal/core"
	"github.com/dtuit/rig/internal/provider"
)

// Picker walks the user through composing an UpSpec from live availability.
// Returns nil spec (no error) if the user aborts.
func Picker(ctx context.Context, a *core.App) (*core.UpSpec, error) {
	caps := a.Prov.Capabilities()
	spot := false
	// Every question that shops the catalogue names the cloud it is
	// shopping — the dashboard's `rig · <provider>` and `rig types`'
	// `provider: <name>` say it too, and `--provider` or a profile pin can
	// make it a different one than the config default.
	// A cloud without spot capacity is not asked which pricing it wants.
	if caps.Spot {
		if err := run(huh.NewSelect[bool]().
			Title("Pricing on "+a.Provider()).
			Options(
				huh.NewOption("on-demand (stable)", false),
				huh.NewOption("spot (~65% cheaper, can be preempted)", true),
			).Value(&spot)); err != nil {
			return nil, abortToNil(err)
		}
	}

	types, err := a.Prov.InstanceTypes(ctx)
	if err != nil {
		return nil, err
	}
	avail, err := a.Prov.Availability(ctx, spot)
	if err != nil {
		return nil, err
	}
	where := map[string][]string{}
	for loc, ts := range avail {
		for _, t := range ts {
			where[t] = append(where[t], loc)
		}
	}

	var typeOpts []huh.Option[string]
	for _, t := range types {
		locs := where[t.Type]
		if len(locs) == 0 {
			continue
		}
		slices.Sort(locs)
		price := t.PricePerHour
		if spot {
			price = t.SpotPrice
		}
		gpu := t.GPU
		if gpu == "" {
			gpu = "CPU only"
		}
		// The columns are clipped, not wrapped: a marketplace catalogue's
		// GPU names, descriptions and location lists are long enough to
		// push the price off the line otherwise. The description gets 20
		// columns because a driver's marker belongs at the end of it —
		// Vast's "2 offers (bid only)", the shape no on-demand rental can
		// have — and clipping to 14 cut exactly that away.
		// A price the cloud did not quote is a dash, not $0.00 (see
		// provider.PriceText): AWS quotes no on-demand price at all for
		// the shapes it sells only through Capacity Blocks.
		priceCell := provider.PriceText(price)
		if price != 0 {
			priceCell = "$" + priceCell
		}
		label := fmt.Sprintf("%-16s %-14s %-6s %-8s %-20s %s",
			t.Type, clip(gpu, 14), t.MemoryText(), priceCell, clip(t.Description, 20), locsText(locs))
		typeOpts = append(typeOpts, huh.NewOption(strings.TrimRight(label, " "), t.Type))
	}
	if len(typeOpts) == 0 {
		return nil, fmt.Errorf("nothing available right now (try the other pricing mode)")
	}
	var typ string
	// Height keeps a 45-type RunPod catalogue (or a 200-shape Vast one) in a
	// scrolling viewport instead of one screenful-and-a-half of options; `/`
	// filters, which is the only sane way through a list that long.
	if err := run(huh.NewSelect[string]().
		Title(fmt.Sprintf("Instance type on %s (%d available now, / filters)", a.Provider(), len(typeOpts))).
		Options(typeOpts...).Value(&typ).Height(18)); err != nil {
		return nil, abortToNil(err)
	}

	locs := where[typ]
	slices.Sort(locs)
	loc := locs[0]
	if len(locs) > 1 {
		var opts []huh.Option[string]
		for _, l := range locs {
			opts = append(opts, huh.NewOption(l, l))
		}
		if err := run(huh.NewSelect[string]().Title("Datacenter").Options(opts...).Value(&loc).Height(14)); err != nil {
			return nil, abortToNil(err)
		}
	}

	imgs, err := a.Prov.Images(ctx, typ)
	if err != nil {
		return nil, err
	}
	image := ""
	if len(imgs) > 0 {
		var opts []huh.Option[string]
		for _, im := range imgs {
			label := im.Display
			if im.Default {
				label += " (default)"
			}
			opts = append(opts, huh.NewOption(label, im.Name))
			if im.Default {
				image = im.Name
			}
		}
		if image == "" {
			image = imgs[0].Name
		}
		sel := image
		if err := run(huh.NewSelect[string]().Title("OS image").Options(opts...).Value(&sel).Height(12)); err != nil {
			return nil, abortToNil(err)
		}
		image = sel
	}

	// A cloud without persistent volumes is not asked for one: its
	// ListVolumes answers ErrUnsupported, which would abort the picker.
	var volSpec *config.VolumeSpec
	if caps.Volumes {
		if volSpec, err = pickVolume(ctx, a, loc); err != nil {
			return nil, err
		}
	}

	name := "adhoc"
	ttlStr := a.Cfg.Defaults.TTL
	if err := run(
		huh.NewInput().Title("Instance name (rig-<name>)").Value(&name).Validate(notEmpty),
		huh.NewInput().Title("Self-destruct TTL").Value(&ttlStr).Validate(validDuration),
	); err != nil {
		return nil, abortToNil(err)
	}
	ttl, _ := time.ParseDuration(ttlStr)

	volDesc := "none"
	if volSpec != nil {
		volDesc = fmt.Sprintf("%s (%dGB)", volSpec.Name, volSpec.SizeGB)
	}
	mode := "on-demand"
	if spot {
		mode = "spot"
	}
	ok := false
	summary := fmt.Sprintf("%s on %s in %s, %s, image %s, volume %s, ttl %s",
		typ, a.Provider(), loc, mode, image, volDesc, ttl)
	if err := run(huh.NewConfirm().Title("Create instance?").Description(summary).Value(&ok)); err != nil {
		return nil, abortToNil(err)
	}
	if !ok {
		return nil, nil
	}
	return &core.UpSpec{
		Name:     name,
		Types:    []string{typ},
		Spot:     spot,
		Location: loc,
		Image:    image,
		Volume:   volSpec,
		TTL:      ttl,
		OSGB:     a.Cfg.Defaults.OSGB,
	}, nil
}

// PickProvider asks which cloud to launch on, defaulting to dflt. One
// provider needs no question and is returned as it stands; an abort answers
// "" with no error, as the rest of the picker does.
func PickProvider(names []string, dflt string) (string, error) {
	if len(names) == 0 {
		return "", fmt.Errorf("no providers configured")
	}
	if len(names) == 1 {
		return names[0], nil
	}
	choice := dflt
	if !slices.Contains(names, choice) {
		choice = names[0]
	}
	opts := make([]huh.Option[string], 0, len(names))
	for _, n := range names {
		label := n
		if n == dflt {
			label += " (default)"
		}
		opts = append(opts, huh.NewOption(label, n))
	}
	if err := run(huh.NewSelect[string]().
		Title("Which cloud?").
		Options(opts...).Value(&choice).Height(10)); err != nil {
		return "", abortToNil(err)
	}
	return choice, nil
}

func pickVolume(ctx context.Context, a *core.App, loc string) (*config.VolumeSpec, error) {
	vols, err := a.Prov.ListVolumes(ctx)
	if err != nil {
		return nil, err
	}
	opts := []huh.Option[string]{huh.NewOption("none", ""), huh.NewOption("create new ...", "+")}
	for _, v := range vols {
		if v.Location != loc || v.IsAttached() {
			continue
		}
		opts = append(opts, huh.NewOption(fmt.Sprintf("%s (%dGB %s)", v.Name, v.SizeGB, v.Type), v.Name))
	}
	choice := ""
	if err := run(huh.NewSelect[string]().
		Title(fmt.Sprintf("Persistent volume in %s", loc)).
		Options(opts...).Value(&choice).Height(14)); err != nil {
		return nil, abortToNil(err)
	}
	switch choice {
	case "":
		return nil, nil
	case "+":
		name, size := "rig-data", "200"
		if err := run(
			huh.NewInput().Title("Volume name").Value(&name).Validate(notEmpty),
			huh.NewInput().Title("Size (GB)").Value(&size).Validate(isInt),
		); err != nil {
			return nil, abortToNil(err)
		}
		var gb int
		fmt.Sscanf(size, "%d", &gb)
		return &config.VolumeSpec{Name: name, SizeGB: gb}, nil
	default:
		for _, v := range vols {
			if v.Name == choice && v.Location == loc {
				return &config.VolumeSpec{Name: v.Name, SizeGB: v.SizeGB, Type: v.Type}, nil
			}
		}
		return nil, fmt.Errorf("volume %q vanished", choice)
	}
}

// locsText names where a type is available, compactly: the whole list when it
// is short, else the first location and how many more — Vast reports one GPU
// in twenty countries whose names are "United States, US".
func locsText(locs []string) string {
	joined := strings.Join(locs, ",")
	if len(locs) > 1 && len(joined) > 18 {
		joined = fmt.Sprintf("%s +%d", locs[0], len(locs)-1)
	}
	return "[" + clip(joined, 20) + "]"
}

func run(fields ...huh.Field) error {
	return huh.NewForm(huh.NewGroup(fields...)).Run()
}

func abortToNil(err error) error {
	if err == huh.ErrUserAborted {
		return nil
	}
	return err
}

func notEmpty(s string) error {
	if strings.TrimSpace(s) == "" {
		return fmt.Errorf("required")
	}
	return nil
}

func validDuration(s string) error {
	_, err := time.ParseDuration(s)
	return err
}

func isInt(s string) error {
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil || n <= 0 {
		return fmt.Errorf("positive integer required")
	}
	return nil
}
