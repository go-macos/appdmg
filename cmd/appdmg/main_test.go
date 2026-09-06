package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	hfsplus "github.com/go-filesystems/hfsplus"
)

func sample(t *testing.T) (dir, app, bg string) {
	t.Helper()
	dir = t.TempDir()
	app = filepath.Join(dir, "MyApp.app")
	if err := os.MkdirAll(filepath.Join(app, "Contents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte("<plist/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 300, 200))
	img.Set(0, 0, color.RGBA{1, 2, 3, 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	bg = filepath.Join(dir, "bg.png")
	if err := os.WriteFile(bg, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, app, bg
}

// The flags a release script actually passes, end to end.
func TestBuildsFromFlags(t *testing.T) {
	dir, app, bg := sample(t)
	extra := filepath.Join(dir, "Read Me.txt")
	if err := os.WriteFile(extra, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "MyApp.dmg")
	var errs bytes.Buffer
	code := run([]string{
		"-o", out, "-background", bg, "-applications", "-format", "UDRW",
		"-at", "MyApp.app=160,220", "-at", "Applications=480,220",
		"-window", "10,20,300,200", "-size", "16m", "-volume", "My App",
		app, extra,
	}, &errs)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs.String())
	}
	st, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() != 16<<20 {
		t.Errorf("the raw volume is %d bytes, want the 16 MiB asked for", st.Size())
	}
	v, err := hfsplus.OpenFile(out)
	if err != nil {
		t.Fatalf("opening the volume: %v", err)
	}
	defer v.Close()
	if v.Label() != "My App" {
		t.Errorf("label = %q", v.Label())
	}
	for _, p := range []string{"/MyApp.app/Contents/Info.plist", "/Read Me.txt", "/.background/bg.png", "/.DS_Store"} {
		if _, err := v.Stat(p); err != nil {
			t.Errorf("%s is missing: %v", p, err)
		}
	}
}

func TestRefusalsAndUsage(t *testing.T) {
	_, app, _ := sample(t)
	for _, tc := range []struct {
		name string
		args []string
		code int
		want string
	}{
		{"no output", []string{app}, 2, "usage:"},
		{"nothing to put in it", []string{"-o", "x.dmg"}, 2, "usage:"},
		{"a flag that is not one", []string{"-nope"}, 2, "flag provided but not defined"},
		{"a position with no comma", []string{"-o", "x.dmg", "-at", "MyApp.app=160", app}, 2, "X,Y"},
		{"a position with no name", []string{"-o", "x.dmg", "-at", "160,220", app}, 2, "NAME=X,Y"},
		{"a position that is not a number", []string{"-o", "x.dmg", "-at", "MyApp.app=a,220", app}, 2, "x:"},
		{"a y that is not a number", []string{"-o", "x.dmg", "-at", "MyApp.app=160,b", app}, 2, "y:"},
		{"a window of three numbers", []string{"-o", "x.dmg", "-window", "1,2,3", app}, 2, "X,Y,W,H"},
		{"a window corner that is not a number", []string{"-o", "x.dmg", "-window", "a,2,3,4", app}, 2, "-window"},
		{"a window size that is not a number", []string{"-o", "x.dmg", "-window", "1,2,c,4", app}, 2, "-window"},
		{"a size that is not a number", []string{"-o", "x.dmg", "-size", "big", app}, 2, "size:"},
		{"an application that is not there", []string{"-o", "x.dmg", "No.app"}, 1, "No.app"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var errs bytes.Buffer
			if code := run(tc.args, &errs); code != tc.code {
				t.Errorf("exit %d, want %d (%s)", code, tc.code, errs.String())
			}
			if !strings.Contains(errs.String(), tc.want) {
				t.Errorf("stderr %q does not mention %q", errs.String(), tc.want)
			}
		})
	}
}

func TestSizeSuffixes(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int64
	}{{"", 0}, {"512", 512}, {"4k", 4 << 10}, {"4K", 4 << 10}, {"7m", 7 << 20}, {"7M", 7 << 20}, {"1g", 1 << 30}, {"1G", 1 << 30}} {
		got, err := size(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("size(%q) = %d, %v; want %d", tc.in, got, err, tc.want)
		}
	}
}

// The flag package prints the repeatable flag's default, so it has to have a
// String method that says nothing rather than crashing.
func TestPlacesStringIsEmpty(t *testing.T) {
	if got := (places{}).String(); got != "" {
		t.Errorf("String() = %q", got)
	}
}
