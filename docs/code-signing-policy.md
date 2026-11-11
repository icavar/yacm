# Code signing policy

This page says how YACM release packages are signed, who may change the code
and approve a signed release, and what YACM sends over the network.

## Windows

Free code signing provided by [SignPath.io](https://about.signpath.io),
certificate by [SignPath Foundation](https://signpath.org).

The application to SignPath Foundation is under review. Until it is approved,
the Windows packages are not signed; the first release after that is.

Signed files: `YACM.exe` in `yacm-*-windows-x64.zip` and
`yacm-*-windows-arm64.zip`, and `yacm-demo-device.exe` in
`yacm-demo-device-*-windows.zip`. Only files built from this repository are signed.

## macOS

The app and the demo device are signed with the project's Apple Developer ID
certificate and notarised by Apple.

## Linux

The package repository `pkg.yacm.app` (apt and dnf) is signed with the
project's OpenPGP key "YACM packages <support@yacm.app>", fingerprint
`9C9A 2975 7D17 AFD5 51B6  0BFB 1FA9 9EB2 609B DAAF`. The repository index
and every `.rpm` package are signed with it; apt checks the `.deb` packages
through the signed index. The `.deb` and `.rpm` packages bring the key with
them (`/usr/share/keyrings/yacm-packages.asc`,
`/etc/pki/rpm-gpg/RPM-GPG-KEY-yacm`), and it is published at
<https://pkg.yacm.app/yacm.asc>.

## All systems

Every release is built by GitHub Actions in this repository from a commit on
`main` (`.github/workflows/release.yml`), never on a personal computer. The
release jobs run only after the maintainer approves them. Each release comes
with SHA-256 checksums, a software bill of materials and a build provenance
attestation for every file (`gh attestation verify <file> --repo yacm-app/yacm`;
for releases up to 0.9.2, built before the project moved to the yacm-app
organisation, `--repo icavar/yacm`).

## Team roles

| Role | Who |
|---|---|
| Committers (may change the code without further review) | [icavar](https://github.com/icavar) |
| Reviewers (review every change from other contributors) | [icavar](https://github.com/icavar) |
| Approvers (approve every signing request and release) | [icavar](https://github.com/icavar) |

Contributions from others come as pull requests and are reviewed before they
are merged ([CONTRIBUTING.md](../CONTRIBUTING.md)). All team members use
multi-factor authentication for GitHub and SignPath.

## Privacy

This program will not transfer any information to other networked systems
unless specifically requested by the user or the person installing or
operating it. YACM connects only to the devices, jump hosts, proxies and
services (such as NetBox) that the user configures. The update check is off
unless the user switches it on; it then asks GitHub for the list of releases
and sends nothing else. When the Linux `.deb` or `.rpm` package is installed,
the system's package manager (apt or dnf) asks `pkg.yacm.app` for updates
together with the other repositories. See [SECURITY.md](../SECURITY.md).
