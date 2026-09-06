# appdmg

The `.dmg` a Mac application is distributed in — an HFS+ volume carrying the
`.app`, a background picture with the icons arranged on it, and the volume's
own icon — written in pure Go, `CGO_ENABLED=0`, with no `hdiutil` anywhere.

```go
err := appdmg.Build(appdmg.Spec{
    Output:           "MyApp.dmg",
    App:              "MyApp.app",
    Background:       "art/dmg-background.png",
    VolumeIcon:       "art/volume.icns",
    ApplicationsLink: true,
    Positions: map[string]appdmg.Point{
        "MyApp.app":    {X: 160, Y: 220},
        "Applications": {X: 480, Y: 220},
    },
})
```

That is the whole API. The window takes the background picture's own size
unless `Spec.Window` says otherwise, the volume takes the application's name
unless `Spec.VolumeName` does, and the volume is sized from its content unless
`Spec.SizeBytes` does.

## What it composes

This package orders operations; it implements none of them. Each piece was
proven against macOS on its own first:

| | |
|---|---|
| [`go-filesystems/hfsplus`](https://github.com/go-filesystems/hfsplus) | lays out the volume, copies the bundle in, sets Finder flags |
| [`go-macos/dsstore`](https://github.com/go-macos/dsstore) | writes the `.DS_Store` that carries the background and the icon positions |
| [`go-diskimages/dmg`](https://github.com/go-diskimages/dmg) | wraps the volume in UDIF and compresses it |

HFS+ rather than APFS: it compresses far better under UDZO, and the alias
inside the `.DS_Store` names the filesystem type, so the metadata expects an
`H+` volume.

## The two steps that do nothing on their own

Both are Finder flags, and both are why a hand-assembled image looks wrong in
ways no error message explains:

- `.VolumeIcon.icns` is ignored until the volume's ROOT carries
  `kHasCustomIcon`. Writing the file is not enough.
- `.background` is a perfectly ordinary folder until it carries
  `kIsInvisible`, and then the folder holding the picture stops appearing in
  the window the picture is decorating.

## Building the volume

The volume is laid out in memory and written once. Every `hfsplus` mutator
syncs the WHOLE image back to its backing file, so a volume opened on disk
rewrites itself once per file copied in — quadratic in the size of a `.app`,
for a result identical to writing the bytes at the end.

## Licence

BSD-3-Clause.
