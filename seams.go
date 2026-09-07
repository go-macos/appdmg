// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, the go-macos/appdmg authors

package appdmg

import (
	"fmt"
	"os"

	dmg "github.com/go-diskimages/dmg"
	hfsplus "github.com/go-filesystems/hfsplus"
)

// The steps below cannot fail on the input this package hands them: a volume
// it has just laid out itself, a rename within one directory. They are variables so a test can make them fail
// anyway. An error branch that cannot be reached is an error branch nobody
// has ever read, and the ones that stay unread are the ones that turn out to
// return the wrong thing on the day they fire.

// newVolume lays out an empty HFS+ volume in memory. Nothing is written to
// disk until the whole volume is finished: every hfsplus mutator syncs the
// WHOLE image back to its backing file, so a volume opened on disk rewrites
// itself once per file copied in.
var newVolume = func(size int64, label string) (*hfsplus.Volume, error) {
	img, err := hfsplus.Mkfs(size, hfsplus.FormatConfig{Label: label})
	if err != nil {
		return nil, fmt.Errorf("appdmg: format volume: %w", err)
	}
	v, err := hfsplus.OpenWritable(img, nil)
	if err != nil {
		return nil, fmt.Errorf("appdmg: open volume: %w", err)
	}
	return v, nil
}

var (
	convertUDIF = dmg.ConvertUDIF
	renameFile  = os.Rename
)
