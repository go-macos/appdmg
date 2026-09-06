// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, the go-macos/appdmg authors

// Package appdmg builds the .dmg a Mac application is distributed in — an
// HFS+ volume carrying the .app, a background picture with the icons placed
// on it, and the volume's own icon — in pure Go with CGO_ENABLED=0 and no
// shelling out to hdiutil.
//
// It composes rather than implements. Each piece was proven against macOS on
// its own before this existed:
//
//   - go-filesystems/hfsplus writes the volume and its Finder flags;
//   - go-macos/dsstore writes the window's background and icon positions;
//   - go-diskimages/dmg wraps the result in UDIF and compresses it.
//
// HFS+ rather than APFS: it compresses far better under UDZO, and the alias
// inside the .DS_Store that points at the background names the filesystem
// type, so the metadata expects an "H+" volume.
package appdmg

import (
	"encoding/binary"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	dmg "github.com/go-diskimages/dmg"
	hfsplus "github.com/go-filesystems/hfsplus"
	"github.com/go-macos/dsstore"
)

// Where the conventions live. These are not configurable because they are not
// choices: the Finder looks for the volume icon by this exact name, and the
// background is conventionally hidden in this exact folder.
const (
	backgroundDir  = "/.background"
	volumeIconName = "/.VolumeIcon.icns"
	dsStoreName    = "/.DS_Store"
)

// A Spec describes the image to build.
type Spec struct {
	// Output is the .dmg to write. VolumeName is what the Finder shows and
	// what the background's alias records; it defaults to the .app's name
	// without its extension.
	Output     string
	VolumeName string

	// App is a .app directory copied to the volume root. Extra names more
	// files or directories to copy in, keyed by their path on the volume.
	App   string
	Extra map[string]string

	// Background is a picture copied into /.background and shown behind the
	// window. VolumeIcon is an .icns shown instead of the generic disk —
	// go-macos/appbundle's ICNS writes one.
	Background string
	VolumeIcon string

	// Positions places icons by their name on the volume, in the window's
	// coordinates, measured to the icon's CENTRE. An entry with no position
	// is left where the Finder puts it.
	Positions map[string]Point

	// ApplicationsLink adds the /Applications symlink a drag-to-install
	// window needs.
	ApplicationsLink bool

	// IconSize defaults to 96. Format is the UDIF format, "UDZO" by default —
	// a mostly-empty HFS+ volume compresses to a small fraction of its size.
	IconSize float64
	Format   string

	// SizeBytes is the volume's size. Zero asks for the content's size plus
	// enough slack for the filesystem's own structures.
	SizeBytes int64
}

// A Point is an icon's centre in the window's coordinates.
type Point struct{ X, Y uint32 }

// Build writes the image described by spec.
func Build(spec Spec) error {
	if spec.Output == "" {
		return fmt.Errorf("appdmg: an output path is required")
	}
	if spec.App == "" && len(spec.Extra) == 0 {
		return fmt.Errorf("appdmg: nothing to put in the image")
	}
	if spec.VolumeName == "" {
		spec.VolumeName = strings.TrimSuffix(filepath.Base(spec.App), ".app")
	}
	if spec.VolumeName == "" || spec.VolumeName == "." {
		return fmt.Errorf("appdmg: could not derive a volume name; set VolumeName")
	}
	if spec.Format == "" {
		spec.Format = "UDZO"
	}

	size := spec.SizeBytes
	if size == 0 {
		var err error
		if size, err = sizeFor(spec); err != nil {
			return err
		}
	}

	if _, err := hfsplus.Format(spec.Output, size, hfsplus.FormatConfig{Label: spec.VolumeName}); err != nil {
		return fmt.Errorf("appdmg: format volume: %w", err)
	}
	v, err := hfsplus.OpenFileWritable(spec.Output)
	if err != nil {
		return fmt.Errorf("appdmg: open volume: %w", err)
	}
	if err := fill(v, spec); err != nil {
		v.Close()
		os.Remove(spec.Output)
		return err
	}
	if err := v.Sync(); err != nil {
		v.Close()
		return fmt.Errorf("appdmg: sync: %w", err)
	}
	if err := v.Close(); err != nil {
		return fmt.Errorf("appdmg: close: %w", err)
	}

	if err := dmg.WrapRaw(spec.Output); err != nil {
		return fmt.Errorf("appdmg: wrap: %w", err)
	}
	if spec.Format == "UDRW" {
		return nil
	}
	tmp := spec.Output + ".converting"
	if err := dmg.ConvertUDIF(spec.Output, tmp, spec.Format); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("appdmg: convert to %s: %w", spec.Format, err)
	}
	if err := os.Rename(tmp, spec.Output); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("appdmg: replace %s: %w", spec.Output, err)
	}
	return nil
}

// fill writes everything the volume carries, in the order the Finder needs:
// content first, then the metadata that refers to it.
func fill(v *hfsplus.Volume, spec Spec) error {
	if spec.App != "" {
		if err := copyTree(v, spec.App, "/"+filepath.Base(spec.App)); err != nil {
			return err
		}
	}
	for dst, src := range spec.Extra {
		if err := copyTree(v, src, dst); err != nil {
			return err
		}
	}
	if spec.ApplicationsLink {
		if err := v.Symlink("/Applications", "/Applications"); err != nil {
			return fmt.Errorf("appdmg: /Applications link: %w", err)
		}
	}
	if spec.VolumeIcon != "" {
		b, err := os.ReadFile(spec.VolumeIcon)
		if err != nil {
			return fmt.Errorf("appdmg: read volume icon: %w", err)
		}
		if err := v.WriteFile(volumeIconName, b, 0o644); err != nil {
			return fmt.Errorf("appdmg: write volume icon: %w", err)
		}
		// Writing the file does NOTHING on its own: the Finder draws it only
		// when the volume root carries kHasCustomIcon.
		if err := setFinderFlag(v, "/", hfsplus.FinderFlagHasCustomIcon); err != nil {
			return err
		}
	}

	var store dsstore.Store
	view := dsstore.IconView{VolumeName: spec.VolumeName, IconSize: spec.IconSize}
	if spec.Background != "" {
		b, err := os.ReadFile(spec.Background)
		if err != nil {
			return fmt.Errorf("appdmg: read background: %w", err)
		}
		if err := v.MkDir(backgroundDir, 0o755); err != nil {
			return fmt.Errorf("appdmg: create %s: %w", backgroundDir, err)
		}
		name := backgroundDir + "/" + filepath.Base(spec.Background)
		if err := v.WriteFile(name, b, 0o644); err != nil {
			return fmt.Errorf("appdmg: write background: %w", err)
		}
		// Hidden, or the folder holding the picture appears in the window the
		// picture is decorating.
		if err := setFinderFlag(v, backgroundDir, hfsplus.FinderFlagIsInvisible); err != nil {
			return err
		}
		view.Background = name
	}
	if err := store.SetIconView(view); err != nil {
		return fmt.Errorf("appdmg: icon view: %w", err)
	}
	for name, p := range spec.Positions {
		store.SetIconPosition(name, p.X, p.Y)
	}
	raw, err := store.Bytes()
	if err != nil {
		return fmt.Errorf("appdmg: .DS_Store: %w", err)
	}
	if err := v.WriteFile(dsStoreName, raw, 0o644); err != nil {
		return fmt.Errorf("appdmg: write .DS_Store: %w", err)
	}
	return nil
}

// setFinderFlag turns one bit on in an entry's Finder flags, leaving the rest
// of the 32 bytes alone.
func setFinderFlag(v *hfsplus.Volume, path string, flag uint16) error {
	info, err := v.FinderInfo(path)
	if err != nil {
		return fmt.Errorf("appdmg: read Finder info for %s: %w", path, err)
	}
	cur := binary.BigEndian.Uint16(info[hfsplus.FinderFlagsOffset:])
	binary.BigEndian.PutUint16(info[hfsplus.FinderFlagsOffset:], cur|flag)
	if err := v.SetFinderInfo(path, info); err != nil {
		return fmt.Errorf("appdmg: set Finder info for %s: %w", path, err)
	}
	return nil
}

// copyTree copies a file or directory from the host into the volume.
func copyTree(v *hfsplus.Volume, src, dst string) error {
	st, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("appdmg: %w", err)
	}
	if !st.IsDir() {
		b, err := os.ReadFile(src)
		if err != nil {
			return fmt.Errorf("appdmg: %w", err)
		}
		return v.WriteFile(dst, b, st.Mode().Perm())
	}
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := dst
		if rel != "." {
			target = path.Join(dst, filepath.ToSlash(rel))
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			if err := v.MkDir(target, info.Mode().Perm()); err != nil {
				return fmt.Errorf("appdmg: mkdir %s: %w", target, err)
			}
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			if err := v.Symlink(link, target); err != nil {
				return fmt.Errorf("appdmg: symlink %s: %w", target, err)
			}
		default:
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			// The executable bit matters: a bundle whose program is not
			// executable is an application that will not start.
			if err := v.WriteFile(target, b, info.Mode().Perm()); err != nil {
				return fmt.Errorf("appdmg: write %s: %w", target, err)
			}
		}
		return nil
	})
}

// sizeFor measures the content and adds room for the filesystem's own
// structures, rounded up to a whole number of megabytes.
//
// HFS+ needs space for its catalog and extents trees and its allocation
// bitmap; a volume sized to exactly the payload has nowhere to put them, and
// the failure is a write error partway through rather than a refusal at the
// start.
func sizeFor(spec Spec) (int64, error) {
	var total int64
	add := func(p string) error {
		if p == "" {
			return nil
		}
		return filepath.WalkDir(p, func(_ string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if info, err := d.Info(); err == nil && !d.IsDir() {
				total += info.Size()
			}
			return nil
		})
	}
	if err := add(spec.App); err != nil {
		return 0, fmt.Errorf("appdmg: measure %s: %w", spec.App, err)
	}
	for _, src := range spec.Extra {
		if err := add(src); err != nil {
			return 0, fmt.Errorf("appdmg: measure %s: %w", src, err)
		}
	}
	if err := add(spec.Background); err != nil {
		return 0, fmt.Errorf("appdmg: measure background: %w", err)
	}
	if err := add(spec.VolumeIcon); err != nil {
		return 0, fmt.Errorf("appdmg: measure volume icon: %w", err)
	}
	const (
		overhead = 8 << 20 // trees, bitmap, and the Finder metadata
		mib      = 1 << 20
	)
	size := total + overhead
	return (size + mib - 1) / mib * mib, nil
}
