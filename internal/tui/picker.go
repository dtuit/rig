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
)

// Picker walks the user through composing an UpSpec from live availability.
// Returns nil spec (no error) if the user aborts.
func Picker(ctx context.Context, a *core.App) (*core.UpSpec, error) {
	caps := a.Prov.Capabilities()
	spot := false
	// A cloud without spot capacity is not asked which pricing it wants.
	if caps.Spot {
		if err := run(huh.NewSelect[bool]().
			Title("Pricing").
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
		// push the price off the line otherwise.
		label := fmt.Sprintf("%-16s %-14s %-6s $%-7.2f %-14s %s",
			t.Type, clip(gpu, 14), memText(t.MemoryGB), price, clip(t.Description, 14), locsText(locs))
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
		Title(fmt.Sprintf("Instance type (%d available now, / filters)", len(typeOpts))).
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
	summary := fmt.Sprintf("%s in %s, %s, image %s, volume %s, ttl %s", typ, loc, mode, image, volDesc, ttl)
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

// memText renders system RAM for a picker or table column: a catalogue that
// does not quote it (RunPod's) reports 0, which reads as a dash, not 0GB.
func memText(gb int) string {
	if gb == 0 {
		return "-"
	}
	return fmt.Sprintf("%dGB", gb)
}

// clip shortens s to n display columns so one long provider string cannot
// push every column after it off the line.
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
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
