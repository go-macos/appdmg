// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, the go-macos/appdmg authors

// Command appdmg builds the .dmg a Mac application is shipped in.
//
//	appdmg -o MyApp.dmg -background art/bg.png -icon art/volume.icns \
//	       -applications -at 'MyApp.app=160,220' -at 'Applications=480,220' \
//	       MyApp.app
//
// The first path given is the application; any others are copied to the
// volume root under their own names.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-macos/appdmg"
)

func main() { os.Exit(run(os.Args[1:], os.Stderr)) }

// places collects repeated -at flags.
type places map[string]appdmg.Point

func (p places) String() string { return "" }

func (p places) Set(v string) error {
	name, coords, ok := strings.Cut(v, "=")
	if !ok {
		return fmt.Errorf("want NAME=X,Y")
	}
	x, y, err := pair(coords)
	if err != nil {
		return err
	}
	p[name] = appdmg.Point{X: uint32(x), Y: uint32(y)}
	return nil
}

func pair(s string) (int, int, error) {
	a, b, ok := strings.Cut(s, ",")
	if !ok {
		return 0, 0, fmt.Errorf("want X,Y")
	}
	x, err := strconv.Atoi(strings.TrimSpace(a))
	if err != nil {
		return 0, 0, fmt.Errorf("x: %w", err)
	}
	y, err := strconv.Atoi(strings.TrimSpace(b))
	if err != nil {
		return 0, 0, fmt.Errorf("y: %w", err)
	}
	return x, y, nil
}

// size accepts a plain byte count or one with a k, m or g suffix.
func size(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	mul := int64(1)
	switch last := s[len(s)-1]; last {
	case 'k', 'K':
		mul, s = 1<<10, s[:len(s)-1]
	case 'm', 'M':
		mul, s = 1<<20, s[:len(s)-1]
	case 'g', 'G':
		mul, s = 1<<30, s[:len(s)-1]
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("size: %w", err)
	}
	return n * mul, nil
}

func run(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("appdmg", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		out    = fs.String("o", "", "the .dmg to write (required)")
		volume = fs.String("volume", "", "the volume's name (default: the application's, without .app)")
		bg     = fs.String("background", "", "a picture to show behind the icons")
		icon   = fs.String("icon", "", "an .icns to use as the volume's icon")
		link   = fs.Bool("applications", false, "add the /Applications shortcut")
		window = fs.String("window", "", "X,Y,W,H — the window's bottom-left corner and its size (default: the background's size)")
		format = fs.String("format", "UDZO", "UDZO to compress, UDRW for a writable raw volume")
		sizeS  = fs.String("size", "", "the volume's size, e.g. 200m (default: measured from the content)")
		at     = places{}
	)
	fs.Var(at, "at", "NAME=X,Y — where to put one icon, measured to its centre (repeatable)")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: appdmg -o out.dmg [flags] App.app [more files...]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *out == "" || fs.NArg() == 0 {
		fs.Usage()
		return 2
	}

	spec := appdmg.Spec{
		Output:           *out,
		VolumeName:       *volume,
		App:              fs.Arg(0),
		Background:       *bg,
		VolumeIcon:       *icon,
		ApplicationsLink: *link,
		Format:           *format,
		Positions:        at,
	}
	if rest := fs.Args()[1:]; len(rest) > 0 {
		spec.Extra = map[string]string{}
		for _, p := range rest {
			spec.Extra["/"+filepath.Base(p)] = p
		}
	}
	if *window != "" {
		parts := strings.Split(*window, ",")
		if len(parts) != 4 {
			fmt.Fprintln(stderr, "appdmg: -window wants X,Y,W,H")
			return 2
		}
		x, y, err := pair(parts[0] + "," + parts[1])
		if err != nil {
			fmt.Fprintf(stderr, "appdmg: -window: %v\n", err)
			return 2
		}
		w, h, err := pair(parts[2] + "," + parts[3])
		if err != nil {
			fmt.Fprintf(stderr, "appdmg: -window: %v\n", err)
			return 2
		}
		spec.Window = appdmg.Window{X: x, Y: y, Width: w, Height: h}
	}
	n, err := size(*sizeS)
	if err != nil {
		fmt.Fprintf(stderr, "appdmg: -%v\n", err)
		return 2
	}
	spec.SizeBytes = n

	if err := appdmg.Build(spec); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
