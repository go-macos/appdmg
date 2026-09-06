# What was checked against macOS, and how to check it again

Everything below was run on macOS 26 against an image this package built. The
tests in the repository check the bytes; this file checks the system that
reads them.

## The background picture — with a negative control

AppleScript is no use for this question: `background picture of icon view
options` raises an error even for a window the Finder itself configured with
one, so a `try` block around it reports "no background" for every image ever
made. That is a judge that always answers the same thing, and an early run
here believed it.

The Finder leaves a trace instead. When it resolves the alias in `icvp` it
writes its own bookmark next to it, split across `pBBk` and `pBB0`. So:

1. build the image with `Format: "UDRW"` (writable — the Finder cannot record
   anything on a read-only volume);
2. attach it, `open` the disk in the Finder, wait, `update ... without
   necessity`, close the window, wait, detach;
3. read `.DS_Store` back and list its records.

Two arms, identical but for the picture being where the alias says it is:

| arm | `.DS_Store` after the Finder has looked at it |
|---|---|
| A — the picture is there | `bwsp`, `icvp` (1310 B), **`pBB0`, `pBBk`**, `vSrn`, two `Iloc` |
| B — the picture renamed away | `bwsp`, `icvp` (760 B), `vSrn`, two `Iloc` |

The bookmark appears only in A, and `icvp` shrinks in B as the Finder drops
an alias it could not resolve. The difference is caused by the picture's
presence and nothing else, so the Finder resolved *this package's* alias to
the real file.

## The window's size — and what its coordinates mean

`bounds of container window` is a fair judge here, and it says the mapping is
not the obvious one:

```
bwsp WindowBounds  {{100, 100}, {600, 400}}
Finder bounds      {100, 617, 700, 1017}      (left, top, right, bottom)
desktop bounds     {-96, -1080, 1824, 1117}
```

600 × 400 as asked. The x is the left edge, but **the y is measured from the
BOTTOM of the screen**: 1117 − 1017 = 100. `{{x, y}, {w, h}}` is a Cocoa rect
— origin bottom-left, y growing up — not the top-left corner it looks like.

## The two Finder flags

`GetFileInfo -a` on the mounted volume, where an upper-case letter means the
flag is set:

```
/Volumes/…/.background   aVbstclinmedz    V = invisible
/Volumes/…                avbstClinmedz    C = has a custom icon
```

with `.VolumeIcon.icns` present at 50462 bytes. Those are the flags; whether
the icon is *drawn* is a question for eyes on a screen, and this file does not
claim it.

## Writable means no UDIF at all

`hdiutil create -format UDRW` writes the raw volume and nothing else: no
`koly` trailer, and a file exactly the size of the volume. An image with a
trailer is read-only however the trailer's `imageVariant` is stamped —
`hdiutil imageinfo` calls one built here `UDRO`, and macOS mounts it
read-only. So `Format: "UDRW"` writes the raw volume:

```
Format: UDRW
Format Description: raw read/write
/dev/disk4 on /Volumes/… (hfs, local, nodev, nosuid, noowners)
touch /Volumes/…/it-is-writable   → ok
```

## Detach before believing anything

A stuck `hdiutil` holding an earlier image produced a confident wrong
conclusion once in this work. `hdiutil info | grep image-path` before a run,
and a fresh volume name per run, cost nothing.
