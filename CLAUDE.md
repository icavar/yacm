# YACM: notes for AI coding assistants

YACM (Yet Another Connection Manager) is a cross-platform SSH, telnet and
serial connection manager for network engineers. Go + Wails v2 backend, vanilla JavaScript + Vite + xterm.js frontend.
Repository `github.com/yacm-app/yacm` (organisation `yacm-app`, with the
Homebrew tap and the apt/dnf repository `yacm-packages`).
Maintainer: GitHub `icavar`. Licence: GPL-3.0-or-later; it stays free and
open source. Additional terms under GPLv3 section 7 only (`NOTICE.md`):
liability disclaimer (7a), forks keep "Based on YACM" with a link in About
(7b), say they are modified and use another name (7c), no endorsement with
the authors' names (7d), no rights to the name or logo (7e, `TRADEMARKS.md`).
Nothing beyond section 7. Contributions need the CLA (`CLA.md`; for employers
`CLA-ENTITY.md`; both Harmony).
Privacy: the maintainer's full name appears only in the copyright line of
`NOTICE.md` and as the party in `CLA.md`; elsewhere "The YACM Authors" or the
project link, contact `support@yacm.app`. Every Go and JavaScript source file
starts with the SPDX and copyright header (`core/headers_test.go`); new
dependencies must be permissively licensed (`tools/licensecheck`).

## Conventions

- UI text, documentation, code comments and commit messages are in English,
  short and plain.
- Licence terms for anything built from this code: GPL-3.0-or-later plus
  `NOTICE.md` (keep "Based on YACM" with a link in About, mark modified
  versions and give them another name, no use of the name or logo). Keep
  them when you copy or adapt code.
- Documentation describes what YACM does now; feature lists are ordered by
  value for teams and large networks. Security wording: "helps you meet
  NIS2, ISO 27001, PCI DSS", never "certified" or "compliant" without a
  certificate (`docs/security-compliance.md`).
- Between 0.9.0 and 1.0 only fixes and release work (docs, packaging,
  distribution): no new features or structural changes.
- New features are built in the private repository `yacm-dev`, whose `main`
  shares this history; fixes land here first and are merged there, and the
  features come here with the version that brings them. Commits there follow
  the same rules, since they become public.
- The maintainer's own instructions for AI assistants are kept outside this
  repository.

## Layout

- Module `github.com/yacm-app/yacm`. `main.go` only embeds `frontend/dist` and
  calls `core.Main`; the program is package `core` (importable, so other
  builds can start it with their own `main` and `core.Config`):
  `core/run.go` (data folder, log, Wails options), `core/version.go`
  (`appVersion`). The methods bound to the frontend
  (`window.go.core.App`) live in `core/app*.go`: `app.go` (App, startup, lifecycle),
  `app_vault.go`, `app_tree.go` (sessions, profiles, settings),
  `app_import.go` (foreign and .yacm import, export), `app_lists.go`
  (CSV/Excel, NetBox), `app_sessions.go` (connect, run loop, terminal I/O),
  `app_prompts.go`, `app_misc.go` (dialogs, About), `app_files.go` (file
  transfer: Files tab, Send/Get file, command output to a file),
  `app_fileservers.go` (TFTP/HTTP/SCP servers), `app_tunnels.go` (port
  forwards, outbound proxy, file server tunnel); `datadir*.go`,
  `window*.go`, `idle.go` (anti-idle, prompt watcher, hostname rename),
  `teamlib.go` (team libraries), `reload.go` (reload watch), `recent.go`
  (sessions opened last); `apitypes_test.go` keeps
  `frontend/src/backend.d.ts` in step with the App methods
  (`YACM_UPDATE_TYPES=1 go test -run TestBackendTypes ./core` after adding one).
- `internal/store` sessions, groups, credential profiles, inheritance
  (`Effective`), settings; `internal/vault` Argon2id (or PBKDF2-SHA256,
  read by every build) + AES-GCM secrets;
  `internal/sshconn`, `telnet`, `serialconn`, `expect` (automation rules);
  `internal/importer` (SecureCRT, MobaXterm, mRemoteNG, PuTTY, Xshell, Ásbrú,
  ssh config, CSV/XLSX); `internal/exchange` (.yacm export/import);
  `internal/netbox`; `internal/team`; `internal/ppk`; `internal/jsonx`
  (keeps JSON members a type does not know); `internal/xfer` (SFTP, SCP
  client and server handler, Cisco `dir` / `show file systems` parsing, MD5
  check); `internal/fileserver` (TFTP, HTTP, SCP servers and a TFTP client);
  `internal/tunnel` (-L/-R/-D with SOCKS5/4a and HTTP proxying, outbound
  SOCKS5/HTTP proxy dialer); `internal/fakedevice` (pretend IOS flash, exec
  commands and SSH server for tests and the demo device).
- `testlab/` containerlab test lab (jump hosts, Arista cEOS fabric, Linux
  servers, the demo device) and `core/lab_e2e_test.go` (build tag `clab`):
  `make -C testlab/clab e2e` (FABRIC=linux without cEOS); `tools/mklab`
  writes its sessions file; the Lab workflow runs it on a GitHub runner
  (Linux fabric) and, for cEOS, on the maintainer's computer through a
  self-hosted runner (label `clab`) registered on the private repository
  `yacm-dev` only, which tests the public `main` (`YACM_LAB_SOURCE`).
- `cmd/yacm-demo-device` local fake Cisco-like device for testing;
  `tools/mkdemo` writes `demo.yacm`; `tools/winres` regenerates the
  `rsrc_windows_*.syso` resources (run after a version bump).
- `frontend/src`: `main.js`, `tree.js`, `editor.js`, `terms.js`,
  `settings.js`, `transfer.js` (import/export), `teamui.js`, `datafolder.js`,
  `switcher.js` (Go to session), `termcolors.js`, `files.js` (Files tab,
  Send/Get file, transfers), `servers.js` (file servers and the device
  command helper), `tunnels.js` (Tunnels dialog), `activity.js` (the bar on
  the left edge; new tools add an icon with `addActivity`), `picker.js`
  (session picker with search). UI names differ from the
  stored members: Tags = `clusters`, Saved commands = `macros`, Type to many
  = broadcast.

## Compatibility rules (keep these)

YACM files live on in shared folders, team libraries and git, and a team
often runs mixed versions. So:

- Changes to `sessions.json`, `.yacm` and team library files are additive:
  new members, new sections, new connection types. Never change the meaning
  of an existing member; a change older versions cannot read needs a new
  `schemaVersion` / format `Version`, which they refuse to open.
- Every stored type has `Extra jsonx.Unknown` plus the two methods in
  `internal/store/compat.go`; a new stored struct gets them too. Members a
  version does not know are written back unchanged; saves from the UI keep
  them (`keepUnknownNode`, `jsonx.Keep`).
- A session whose `protocol` is unknown (`store.KnownProtocol`) is kept and
  shown greyed ("newer YACM"), cannot be opened, edited, copied from a
  library or imported from a .yacm file, and bulk edits only touch its
  clusters.
- Data from someone else's file (a .yacm import, a copy of a library
  session) never carries unknown members into your own sessions
  (`store.DropUnknown`): a newer version may treat such a member (a local
  command, say) with care this version cannot apply.
- Team libraries record the publishing version in `app`. A library from a
  newer YACM, or one with unknown members or connection types
  (`File.NeedsNewer`), is used but cannot be edited or replaced here.
- A new connection type gets its own settings block on `Session` (like
  `Serial *Serial`: `RDP *RDPOptions`, `VNC *VNCOptions` …) instead of more
  flat fields, and a case in `KnownProtocol`, `Validate` and the connect
  switch in `app_sessions.go`. Existing SSH fields stay where they are.
- Tabs have kinds (`terms.js`): `terminal` (class `Tab`) and `files`
  (`FilesTab` in `files.js`). Another kind (RDP/VNC canvas) builds its pane
  with `paneShell`, adds itself with `addPaneTab` and provides `focus`,
  `fitAndResize`, `paintLabel`, `applyColors`, `dispose`, `renderStatus`,
  `menuItems`, `reconnect` and `busy`.
- Tunnels from someone else's file come switched off (`store.DisarmTunnels`
  next to `DropUnknown`), and team library sessions never start tunnels by
  themselves.

## Build and test

```sh
cd frontend && npm ci && npm run build && npm run typecheck && cd ..   # go:embed needs frontend/dist
gofmt -l . ; go vet ./... ; go test -race ./...
cd frontend && npx playwright test   # UI tests with a mock backend
make -C testlab/clab e2e            # end-to-end against the container lab
bash build/package.sh               # Windows x64/arm64 zips, Linux tar.gz, demo packages into dist/
sh build/darwin/build-macos.sh      # on a Mac: dist-macos/*.dmg and the demo zip
```

Linux desktop builds need `-tags desktop,production,webkit2_41` and
`libgtk-3-dev libwebkit2gtk-4.1-dev`. Windows builds cross-compile without cgo.

## Releases

Bump the version in `core/version.go` (and `wails.json`), run
`go run ./tools/winres` (it takes the numbers from `core/version.go` for the
Windows resources; the macOS build does the same), update the README and the
version line on the website (`docs/index.html`), and push to `main`. What's new (`frontend/src/whatsnew.js`): every version from 0.9.1 on adds
its entry there; it is shown after the update and becomes the release notes. Release
candidates are `0.9.0-rc.1`, `-rc.2` …: a pre-release for testers; the
final version drops the suffix. Releases are made from the browser: Actions > Release >
Run workflow on `main` with the version (`v0.9.0`) builds Windows, Linux
and macOS packages, the SBOM and (in the public repository) build
provenance, the Microsoft Store bundle (`build/windows/msix.sh` on a Windows
runner; the run's artifact `store-msix`, uploaded to Partner Center by hand,
not a release file), and a draft GitHub release with notes from What's new on that
commit (for 0.9.x with the beta line); Publish on the Releases page creates the tag, and the run the tag
starts sees the release and stops (a tag pushed from a computer still
builds a draft as before). The version must match `core/version.go`. Tags
cannot be pushed from Claude's sessions (the proxy refuses them).
GitHub Actions are pinned to commit SHAs (with the version in a comment);
Dependabot updates them after a week's cool-down.
