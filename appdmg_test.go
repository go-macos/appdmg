package appdmg

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"runtime"

	dmg "github.com/go-diskimages/dmg"
	hfsplus "github.com/go-filesystems/hfsplus"
	"github.com/go-macos/dsstore"
	"howett.net/plist"
)

// Two host abilities the tests need and Windows does not grant: a symlink is
// a privilege there, and a mode of 0 does not stop a read.
var (
	hostMakesSymlinks = runtime.GOOS != "windows"
	hostRefusesReads  = runtime.GOOS != "windows" && os.Geteuid() != 0
	// Windows records no executable bit, so there is none to carry over.
	hostRecordsExecutable = runtime.GOOS != "windows"
)

// sampleApp writes a .app with the shapes that matter: a nested directory, an
// executable, and a symlink of the kind a framework leaves behind.
func sampleApp(t *testing.T, dir string) string {
	t.Helper()
	app := filepath.Join(dir, "MyApp.app")
	if err := os.MkdirAll(filepath.Join(app, "Contents", "MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte("<plist/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app, "Contents", "MacOS", "MyApp"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Windows makes a symlink a privilege rather than a file operation, so
	// the bundle there is the same bundle without the link, and the check on
	// it is skipped with it.
	if hostMakesSymlinks {
		if err := os.Symlink("MacOS/MyApp", filepath.Join(app, "Contents", "run")); err != nil {
			t.Fatal(err)
		}
	}
	return app
}

// samplePNG writes a picture of a known size.
func samplePNG(t *testing.T, path string, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{1, 2, 3, 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// opened unwraps a built image and opens the volume inside it, so every
// assertion is made on the image as shipped rather than on the bytes that
// went into it.
func opened(t *testing.T, path string) *hfsplus.Volume {
	t.Helper()
	if !dmg.IsUDIF(path) {
		t.Fatalf("%s is not a UDIF image", path)
	}
	raw, err := dmg.UnpackToTemp(path)
	if err != nil {
		t.Fatalf("unpacking %s: %v", path, err)
	}
	t.Cleanup(func() { os.Remove(raw) })
	v, err := hfsplus.OpenFile(raw)
	if err != nil {
		t.Fatalf("opening the volume in %s: %v", path, err)
	}
	t.Cleanup(func() { v.Close() })
	return v
}

func storeIn(t *testing.T, v *hfsplus.Volume) map[string]dsstore.Record {
	t.Helper()
	b, err := v.ReadFile(dsStoreName)
	if err != nil {
		t.Fatalf("reading %s: %v", dsStoreName, err)
	}
	s, err := dsstore.Parse(b)
	if err != nil {
		t.Fatalf("parsing %s: %v", dsStoreName, err)
	}
	byKey := map[string]dsstore.Record{}
	for _, r := range s.Records() {
		byKey[r.Name+"/"+r.ID] = r
	}
	return byKey
}

func plistIn(t *testing.T, r dsstore.Record) map[string]any {
	t.Helper()
	blob, ok := r.Val.(dsstore.Blob)
	if !ok {
		t.Fatalf("%s/%s is a %T, want a Blob", r.Name, r.ID, r.Val)
	}
	var d map[string]any
	if _, err := plist.Unmarshal([]byte(blob), &d); err != nil {
		t.Fatalf("decoding %s: %v", r.ID, err)
	}
	return d
}

// The whole job, checked through the shipped image: the application is there
// and still executable, the background is hidden, the volume icon is marked,
// the window is the picture's size, and the icons are where they were put.
func TestBuildAnInstallerImage(t *testing.T) {
	dir := t.TempDir()
	app := sampleApp(t, dir)
	bg := samplePNG(t, filepath.Join(dir, "bg.png"), 640, 380)
	icon := filepath.Join(dir, "vol.icns")
	if err := os.WriteFile(icon, []byte("icns\x00\x00\x00\x08"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "MyApp.dmg")

	if err := Build(Spec{
		Output:           out,
		App:              app,
		Background:       bg,
		VolumeIcon:       icon,
		ApplicationsLink: true,
		Positions: map[string]Point{
			"MyApp.app":    {X: 160, Y: 220},
			"Applications": {X: 480, Y: 220},
		},
	}); err != nil {
		t.Fatalf("Build: %v", err)
	}

	v := opened(t, out)
	if got := v.Label(); got != "MyApp" {
		t.Errorf("volume label = %q, want %q", got, "MyApp")
	}
	if _, err := v.Stat("/MyApp.app/Contents/Info.plist"); err != nil {
		t.Errorf("the bundle did not arrive: %v", err)
	}
	st, err := v.Stat("/MyApp.app/Contents/MacOS/MyApp")
	if err != nil {
		t.Fatalf("the program did not arrive: %v", err)
	}
	// A bundle whose program is not executable is an application that will
	// not start, so the mode is part of the deliverable.
	if hostRecordsExecutable && st.Mode()&0o111 == 0 {
		t.Errorf("the program's mode is %#o, want the executable bit", st.Mode())
	}
	if hostMakesSymlinks {
		if target, err := v.ReadLink("/MyApp.app/Contents/run"); err != nil || target != "MacOS/MyApp" {
			t.Errorf("the symlink is %q, %v", target, err)
		}
	}
	if target, err := v.ReadLink("/Applications"); err != nil || target != "/Applications" {
		t.Errorf("/Applications link is %q, %v", target, err)
	}
	if _, err := v.ReadFile("/.background/bg.png"); err != nil {
		t.Errorf("the background did not arrive: %v", err)
	}

	// Two flags that do nothing on their own and everything together with the
	// files they are set on.
	if !flagSet(t, v, backgroundDir, hfsplus.FinderFlagIsInvisible) {
		t.Error(".background is not invisible; the folder shows up in the window it decorates")
	}
	if !flagSet(t, v, "/", hfsplus.FinderFlagHasCustomIcon) {
		t.Error("the volume root has no kHasCustomIcon; .VolumeIcon.icns is never drawn")
	}

	recs := storeIn(t, v)
	icvp := plistIn(t, recs["./icvp"])
	if icvp["backgroundType"] != uint64(2) {
		t.Errorf("backgroundType = %v, want 2 (a picture)", icvp["backgroundType"])
	}
	if _, ok := icvp["backgroundImageAlias"]; !ok {
		t.Error("icvp carries no alias, so it names no picture")
	}
	bwsp := plistIn(t, recs["./bwsp"])
	if got := bwsp["WindowBounds"]; got != "{{100, 100}, {640, 380}}" {
		t.Errorf("WindowBounds = %v, want the picture's own size", got)
	}
	for _, tc := range []struct {
		name string
		x, y uint32
	}{{"MyApp.app", 160, 220}, {"Applications", 480, 220}} {
		blob, ok := recs[tc.name+"/Iloc"].Val.(dsstore.Blob)
		if !ok || len(blob) != 16 {
			t.Errorf("%s has no Iloc", tc.name)
			continue
		}
		if x, y := binary.BigEndian.Uint32(blob[0:4]), binary.BigEndian.Uint32(blob[4:8]); x != tc.x || y != tc.y {
			t.Errorf("%s at (%d,%d), want (%d,%d)", tc.name, x, y, tc.x, tc.y)
		}
	}
}

func flagSet(t *testing.T, v *hfsplus.Volume, path string, flag uint16) bool {
	t.Helper()
	info, err := v.FinderInfo(path)
	if err != nil {
		t.Fatalf("Finder info for %s: %v", path, err)
	}
	return binary.BigEndian.Uint16(info[hfsplus.FinderFlagsOffset:])&flag != 0
}

// UDZO is the default because a mostly empty volume is mostly zeros. The
// check is that the shipped image is smaller than the volume it carries --
// which also says the compression actually ran.
func TestTheDefaultFormatCompresses(t *testing.T) {
	dir := t.TempDir()
	app := sampleApp(t, dir)
	out := filepath.Join(dir, "z.dmg")
	if err := Build(Spec{Output: out, App: app, SizeBytes: 16 << 20}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	st, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() >= 16<<20 {
		t.Errorf("the image is %d bytes for a 16 MiB volume; it was not compressed", st.Size())
	}
	if got, err := dmg.DetectUDIFFormat(out); err != nil || got != "UDZO" {
		t.Errorf("format = %q, %v; want UDZO", got, err)
	}
}

// A writable image is the raw volume and nothing else. hdiutil writes one
// that way -- "raw read/write", exactly the size of the volume, no koly
// trailer -- and an image with a trailer is mounted read-only whatever the
// trailer says, which is the opposite of what was asked for.
func TestUDRWIsTheRawVolume(t *testing.T) {
	dir := t.TempDir()
	app := sampleApp(t, dir)
	out := filepath.Join(dir, "rw.dmg")
	const size = 16 << 20
	if err := Build(Spec{Output: out, App: app, Format: "UDRW", VolumeName: "RW", SizeBytes: size}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if dmg.IsUDIF(out) {
		t.Error("the writable image carries a UDIF trailer, so macOS will mount it read-only")
	}
	st, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() != size {
		t.Errorf("the image is %d bytes for a %d-byte volume", st.Size(), size)
	}
	v, err := hfsplus.OpenFile(out)
	if err != nil {
		t.Fatalf("opening the raw volume: %v", err)
	}
	defer v.Close()
	if v.Label() != "RW" {
		t.Errorf("label = %q, want RW", v.Label())
	}
}

// Extra content lands where the caller put it, and a single file is copied as
// a file rather than walked as a tree.
func TestExtraContent(t *testing.T) {
	dir := t.TempDir()
	readme := filepath.Join(dir, "README.txt")
	if err := os.WriteFile(readme, []byte("read me"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "e.dmg")
	if err := Build(Spec{
		Output:     out,
		VolumeName: "Extra",
		Extra:      map[string]string{"/Read Me.txt": readme},
		Window:     Window{X: 10, Y: 20, Width: 300, Height: 200},
	}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	v := opened(t, out)
	b, err := v.ReadFile("/Read Me.txt")
	if err != nil || string(b) != "read me" {
		t.Errorf("Read Me.txt = %q, %v", b, err)
	}
	if got := plistIn(t, storeIn(t, v)["./bwsp"])["WindowBounds"]; got != "{{10, 20}, {300, 200}}" {
		t.Errorf("WindowBounds = %v, want the caller's", got)
	}
}

// With no background and no window of its own, nothing claims to know how big
// the window should be, and no bwsp is written.
func TestNoWindowRecordWithoutABackground(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "n.dmg")
	if err := Build(Spec{Output: out, VolumeName: "Bare", App: sampleApp(t, dir)}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, ok := storeIn(t, opened(t, out))["./bwsp"]; ok {
		t.Error("a bwsp was written for a window nobody sized")
	}
}

// A background that cannot be measured is not a refusal to use it: the caller
// is told which knob answers the question.
func TestAnUnmeasurableBackgroundNamesTheWayOut(t *testing.T) {
	dir := t.TempDir()
	bg := filepath.Join(dir, "bg.tiff")
	if err := os.WriteFile(bg, []byte("II*\x00 not really"), 0o644); err != nil {
		t.Fatal(err)
	}
	spec := Spec{Output: filepath.Join(dir, "t.dmg"), VolumeName: "T", App: sampleApp(t, dir), Background: bg}
	err := Build(spec)
	if err == nil || !strings.Contains(err.Error(), "Spec.Window") {
		t.Fatalf("Build error = %v, want one naming Spec.Window", err)
	}
	spec.Window = Window{X: 0, Y: 0, Width: 500, Height: 300}
	if err := Build(spec); err != nil {
		t.Fatalf("Build with a window of its own: %v", err)
	}
	if got := plistIn(t, storeIn(t, opened(t, spec.Output))["./bwsp"])["WindowBounds"]; got != "{{0, 0}, {500, 300}}" {
		t.Errorf("WindowBounds = %v", got)
	}
}

// A build that fails must not leave a half-written image behind wearing the
// name of a finished one.
func TestAFailedBuildLeavesNoImage(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "gone.dmg")
	err := Build(Spec{Output: out, VolumeName: "X", Extra: map[string]string{"/a": filepath.Join(dir, "missing")}})
	if err == nil {
		t.Fatal("Build accepted a file that is not there")
	}
	if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s exists after a failed build", out)
	}
}

func TestRefusals(t *testing.T) {
	dir := t.TempDir()
	app := sampleApp(t, dir)
	for _, tc := range []struct {
		name string
		spec Spec
		want string
	}{
		{"no output", Spec{App: app}, "output path"},
		{"nothing to put in it", Spec{Output: filepath.Join(dir, "a.dmg")}, "nothing to put"},
		{"no name to derive", Spec{Output: filepath.Join(dir, "b.dmg"), Extra: map[string]string{"/x": app}}, "VolumeName"},
		{"an app that is not there", Spec{Output: filepath.Join(dir, "c.dmg"), App: filepath.Join(dir, "No.app")}, "No.app"},
		{"a background that is not there", Spec{Output: filepath.Join(dir, "d.dmg"), App: app, Background: filepath.Join(dir, "no.png")}, "no.png"},
		{"an icon that is not there", Spec{Output: filepath.Join(dir, "e.dmg"), App: app, VolumeIcon: filepath.Join(dir, "no.icns")}, "no.icns"},
		{"a format nothing writes", Spec{Output: filepath.Join(dir, "f.dmg"), App: app, Format: "UDBZ"}, "UDBZ"},
		{"a volume too small to format", Spec{Output: filepath.Join(dir, "g.dmg"), App: app, SizeBytes: 512}, "format volume"},
		{"a volume too small for the app", Spec{Output: filepath.Join(dir, "h.dmg"), App: app, SizeBytes: 40 << 10}, "MyApp"},
		{"nowhere to write it", Spec{Output: filepath.Join(dir, "no-such-dir", "i.dmg"), App: app}, "i.dmg"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Build(tc.spec)
			if err == nil {
				t.Fatalf("Build accepted it")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not name %q", err, tc.want)
			}
		})
	}
}

// The size that is not given is measured, and the measurement has to leave
// room for the filesystem's own structures as well as the payload.
func TestTheMeasuredSizeHoldsTheContent(t *testing.T) {
	dir := t.TempDir()
	app := sampleApp(t, dir)
	big := make([]byte, 3<<20)
	for i := range big {
		big[i] = byte(i)
	}
	if err := os.WriteFile(filepath.Join(app, "Contents", "MacOS", "payload"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "m.dmg")
	if err := Build(Spec{Output: out, App: app}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	got, err := opened(t, out).ReadFile("/MyApp.app/Contents/MacOS/payload")
	if err != nil {
		t.Fatalf("reading the payload back: %v", err)
	}
	if !bytes.Equal(got, big) {
		t.Errorf("the payload came back changed (%d bytes of %d)", len(got), len(big))
	}
}

func TestMeasuringWhatIsNotThere(t *testing.T) {
	if _, err := sizeFor(Spec{VolumeIcon: filepath.Join(t.TempDir(), "no.icns")}); err == nil {
		t.Error("sizeFor accepted an icon that is not there")
	}
	if _, err := sizeFor(Spec{Extra: map[string]string{"/x": filepath.Join(t.TempDir(), "no")}}); err == nil {
		t.Error("sizeFor accepted an extra that is not there")
	}
}

func ExampleBuild() {
	err := Build(Spec{
		Output:           "MyApp.dmg",
		App:              "MyApp.app",
		Background:       "art/dmg-background.png",
		VolumeIcon:       "art/volume.icns",
		ApplicationsLink: true,
		Positions: map[string]Point{
			"MyApp.app":    {X: 160, Y: 220},
			"Applications": {X: 480, Y: 220},
		},
	})
	fmt.Println(err != nil)
}
