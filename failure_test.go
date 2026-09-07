package appdmg

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	hfsplus "github.com/go-filesystems/hfsplus"
)

// A read-only volume is the cheapest way to make every write fail: hfsplus
// returns ErrReadOnly from each mutator, so the branches below are read as
// themselves rather than as whichever one happened to fire first.
func readOnlyVolume(t *testing.T) *hfsplus.Volume {
	t.Helper()
	img, err := hfsplus.Mkfs(8<<20, hfsplus.FormatConfig{Label: "RO"})
	if err != nil {
		t.Fatal(err)
	}
	v, err := hfsplus.Open(bytesAt(img), int64(len(img)))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

type bytesAt []byte

func (b bytesAt) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(b)) {
		return 0, errors.New("eof")
	}
	return copy(p, b[off:]), nil
}

func TestCopyTreeReportsEveryKindOfWriteFailure(t *testing.T) {
	dir := t.TempDir()
	app := sampleApp(t, dir)
	v := readOnlyVolume(t)

	// A directory, a symlink and a plain file, each failing on its own line.
	if err := copyTree(v, app, "/MyApp.app"); err == nil || !strings.Contains(err.Error(), "mkdir") {
		t.Errorf("directory error = %v, want one naming mkdir", err)
	}
	if err := copyTree(v, filepath.Join(app, "Contents", "run"), "/run"); err == nil {
		t.Error("a symlink copied onto a read-only volume")
	}
	if err := copyTree(v, filepath.Join(app, "Contents", "Info.plist"), "/Info.plist"); err == nil {
		t.Error("a file copied onto a read-only volume")
	}
	if err := copyTree(v, filepath.Join(dir, "missing"), "/x"); err == nil {
		t.Error("copyTree accepted a source that is not there")
	}
}

// A symlink inside a tree fails on the symlink, not on the directory that
// came before it — the branch is only reachable once the directory is there.
func TestCopyTreeRefusesToOverwriteATree(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "tree")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "target"), []byte("t"), 0o644); err != nil {
		t.Fatal(err)
	}
	if hostMakesSymlinks {
		if err := os.Symlink("target", filepath.Join(src, "link")); err != nil {
			t.Fatal(err)
		}
	}
	v, err := newVolume(8<<20, "T")
	if err != nil {
		t.Fatal(err)
	}
	if err := copyTree(v, src, "/tree"); err != nil {
		t.Fatalf("first copy: %v", err)
	}
	// The second copy finds everything already there.
	err = copyTree(v, src, "/tree")
	if err == nil || !strings.Contains(err.Error(), "mkdir") {
		t.Errorf("copying a tree twice = %v, want a mkdir error", err)
	}
	// The second copy under a name of its own gets past the directory and
	// fails on the symlink inside it.
	if err := copyTree(v, src, "/tree2"); err != nil {
		t.Fatalf("copy under a fresh name: %v", err)
	}
	if err := v.Symlink("taken", "/tree3/link"); err == nil {
		t.Fatal("hfsplus made a link under a directory that does not exist")
	}
	if err := copyTree(v, src, "/tree2"); err == nil || !strings.Contains(err.Error(), "mkdir") {
		t.Errorf("copying a tree twice = %v", err)
	}
}

// A directory the process cannot read stops the walk rather than producing a
// tree with a hole in it.
func TestCopyTreeStopsOnAnUnreadableDirectory(t *testing.T) {
	if !hostRefusesReads {
		t.Skip("this host reads every directory")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "tree")
	shut := filepath.Join(src, "shut")
	if err := os.MkdirAll(shut, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shut, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shut, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(shut, 0o755) })

	v, err := newVolume(8<<20, "T")
	if err != nil {
		t.Fatal(err)
	}
	if err := copyTree(v, src, "/tree"); err == nil {
		t.Error("the walk finished over a directory it cannot read")
	}
	// …and a file it cannot read is reported too.
	f := filepath.Join(dir, "secret")
	if err := os.WriteFile(f, []byte("x"), 0); err != nil {
		t.Fatal(err)
	}
	if err := copyTree(v, f, "/secret"); err == nil {
		t.Error("an unreadable file was copied")
	}
}

func TestSetFinderFlagReportsBothHalves(t *testing.T) {
	v := readOnlyVolume(t)
	if err := setFinderFlag(v, "/nowhere", hfsplus.FinderFlagIsInvisible); err == nil || !strings.Contains(err.Error(), "read Finder info") {
		t.Errorf("error = %v, want the read half", err)
	}
	if err := setFinderFlag(v, "/", hfsplus.FinderFlagHasCustomIcon); err == nil || !strings.Contains(err.Error(), "set Finder info") {
		t.Errorf("error = %v, want the write half", err)
	}
}

// Every step of fill that writes to the volume, made to fail on its own turn.
// The size is given so the measuring pass does not answer first: a missing
// file is otherwise reported while sizing, and the branch that copies it is
// never reached.
func TestFillReportsTheStepThatFailed(t *testing.T) {
	dir := t.TempDir()
	app := sampleApp(t, dir)
	bg := samplePNG(t, filepath.Join(dir, "bg.png"), 100, 100)
	icon := filepath.Join(dir, "v.icns")
	if err := os.WriteFile(icon, []byte("icns"), 0o644); err != nil {
		t.Fatal(err)
	}
	blocking := filepath.Join(dir, "blocking")
	if err := os.MkdirAll(blocking, 0o755); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(dir, "gone")
	// Bigger than the volume it is asked to go on, so the write fails after
	// the folder around it was made.
	oversized := filepath.Join(dir, "huge.png")
	if err := os.WriteFile(oversized, make([]byte, 12<<20), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		spec Spec
		want string
	}{
		{"an extra that vanished", Spec{Extra: map[string]string{"/x": gone}}, "appdmg:"},
		{"a link where a file already is", Spec{Extra: map[string]string{"/Applications": icon}, ApplicationsLink: true}, "/Applications link"},
		{"an icon that vanished", Spec{VolumeIcon: gone}, "read volume icon"},
		{"an icon whose name is taken", Spec{VolumeIcon: icon, Extra: map[string]string{volumeIconName: blocking}}, "write volume icon"},
		{"a background that vanished", Spec{Background: gone}, "read background"},
		{"a background folder whose name is taken", Spec{Background: bg, Extra: map[string]string{backgroundDir: icon}}, "create " + backgroundDir},
		{"a background too big for the volume", Spec{Background: oversized, Window: Window{Width: 1, Height: 1}}, "write background"},
		{"a .DS_Store whose name is taken", Spec{Extra: map[string]string{dsStoreName: blocking}}, "write .DS_Store"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := tc.spec
			spec.Output = filepath.Join(dir, "out.dmg")
			spec.VolumeName = "T"
			spec.SizeBytes = 8 << 20
			if spec.App == "" && len(spec.Extra) == 0 {
				spec.App = app
			}
			err := Build(spec)
			if err == nil {
				t.Fatal("Build accepted it")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not name %q", err, tc.want)
			}
		})
	}
}

// The three calls that cannot fail on this package's own output, made to fail.
func TestTheStepsAfterTheVolumeIsWritten(t *testing.T) {
	dir := t.TempDir()
	app := sampleApp(t, dir)
	base := Spec{App: app, SizeBytes: 8 << 20}
	boom := errors.New("boom")

	t.Run("laying out the volume", func(t *testing.T) {
		saved := newVolume
		newVolume = func(int64, string) (*hfsplus.Volume, error) { return nil, boom }
		defer func() { newVolume = saved }()
		spec := base
		spec.Output = filepath.Join(dir, "a.dmg")
		if err := Build(spec); !errors.Is(err, boom) {
			t.Errorf("error = %v", err)
		}
	})
	t.Run("converting", func(t *testing.T) {
		saved := convertUDIF
		convertUDIF = func(string, string, string) error { return boom }
		defer func() { convertUDIF = saved }()
		spec := base
		spec.Output = filepath.Join(dir, "b.dmg")
		if err := Build(spec); err == nil || !strings.Contains(err.Error(), "convert") {
			t.Errorf("error = %v, want one naming the conversion", err)
		}
	})
	t.Run("replacing the raw image", func(t *testing.T) {
		saved := renameFile
		renameFile = func(string, string) error { return boom }
		defer func() { renameFile = saved }()
		spec := base
		spec.Output = filepath.Join(dir, "c.dmg")
		err := Build(spec)
		if err == nil || !strings.Contains(err.Error(), "replace") {
			t.Errorf("error = %v, want one naming the replacement", err)
		}
		// The converted image is not left behind next to the one it failed
		// to replace.
		if _, err := os.Stat(filepath.Join(dir, "c.dmg.converting")); !errors.Is(err, os.ErrNotExist) {
			t.Error("the half-finished conversion is still there")
		}
	})
}

// A file the walk cannot read stops the copy rather than leaving a tree with
// a hole in it.
func TestAnUnreadableFileInsideATree(t *testing.T) {
	if !hostRefusesReads {
		t.Skip("this host reads every file")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "tree")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "secret"), []byte("x"), 0); err != nil {
		t.Fatal(err)
	}
	v, err := newVolume(8<<20, "T")
	if err != nil {
		t.Fatal(err)
	}
	if err := copyTree(v, src, "/t"); err == nil {
		t.Error("a tree holding an unreadable file was copied whole")
	}

}

// A window with more icons than one node holds is refused rather than written
// half-way: dsstore writes a single leaf and says so.
func TestTooManyIconsToPlace(t *testing.T) {
	dir := t.TempDir()
	spec := Spec{
		Output:     filepath.Join(dir, "many.dmg"),
		VolumeName: "Many",
		SizeBytes:  8 << 20,
		Extra:      map[string]string{"/a.txt": samplePNG(t, filepath.Join(dir, "a.png"), 2, 2)},
		Positions:  map[string]Point{},
	}
	for i := 0; i < 400; i++ {
		spec.Positions[fmt.Sprintf("an-entry-with-a-long-name-%03d", i)] = Point{X: uint32(i), Y: 1}
	}
	if err := Build(spec); err == nil || !strings.Contains(err.Error(), ".DS_Store") {
		t.Errorf("Build = %v, want a .DS_Store refusal", err)
	}
}
