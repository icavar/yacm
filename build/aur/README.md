# AUR package

`yacm-bin/` is the Arch User Repository package
[yacm-bin](https://aur.archlinux.org/packages/yacm-bin): the Linux release
package with a menu entry and icons. After a release, set `pkgver`, reset
`pkgrel` to 1, update the checksums in `PKGBUILD` and `.SRCINFO`, and push
the three files to `ssh://aur@aur.archlinux.org/yacm-bin.git`.
