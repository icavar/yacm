# Developing YACM

YACM is written in Go (backend, with [Wails v2](https://wails.io)) and plain
JavaScript with Vite and xterm.js (frontend). Contributions are welcome; see
[CONTRIBUTING.md](../CONTRIBUTING.md) for the rules and the CLA.

## Building

Requirements: Go 1.26 or newer (x/crypto needs it; the release was built with
Go 1.27.3), Node 20+, and on Linux `libgtk-3-dev libwebkit2gtk-4.1-dev`.

```sh
cd frontend && npm install && npm run build && cd ..

# Windows (from any OS; no cgo needed). The rsrc_windows_*.syso files add the
# icon, DPI-aware manifest and version info.
GOOS=windows GOARCH=amd64 go build -tags desktop,production -ldflags "-s -w -H windowsgui" -o YACM.exe .

# Linux
go build -tags desktop,production,webkit2_41 -ldflags "-s -w" -o yacm .

```

**macOS**: on a Mac with the Xcode command line tools (`xcode-select
--install`), Go and Node, run `sh build/darwin/build-macos.sh`. It builds a
universal `YACM.app` (Apple silicon and Intel, macOS 13 Ventura or newer, as
Go requires) and packs it into `dist-macos/YACM-<version>-macos.dmg`, and
the demo device into the separate `dist-macos/yacm-demo-device-<version>-macos.zip`. Built this way the
app is signed ad hoc only: a copy built on your own Mac opens normally, a
copy given to someone else has to be allowed in System Settings > Privacy &
Security > Open Anyway. The releases are built by GitHub Actions
(`.github/workflows/release.yml`), signed with a Developer ID and notarised.

With the Wails CLI installed you can also run `wails build` (and `wails dev`
for live reload). Delete the `rsrc_windows_*.syso` files first, because the
Wails CLI generates its own Windows resources. Regenerate them for plain `go
build` with `go run ./tools/winres`.

### Notes on go.mod

`go.mod` has one `replace`: `git.sr.ht/~jackmordaunt/go-toast/v2 =>
./third_party/go-toast-stub`. Wails links that module only for Windows toast
notifications, which YACM does not use (see
`third_party/go-toast-stub/README.md`). On a machine that cannot reach
`golang.org` or `go.bug.st`, a local, git-ignored `go.work` can replace those
modules with their GitHub mirrors (`github.com/golang/...`,
`github.com/bugst/go-serial`) at the same versions.

### The demo device and the demo file

`go run ./cmd/yacm-demo-device` starts the demo device (see the
[user guide](guide.md#trying-it-without-hardware)), and
`go run ./tools/mkdemo demo.yacm` writes the demo `.yacm` file with credential
profiles (export password `demo-export-pw`), a bastion, a group with
defaults and 35 sessions pointing at the demo device.

## Tests

```sh
go test -race ./...
```

The SSH tests start in-process servers and cover password retry, TOFU and
changed host keys, encrypted keys with a CA-signed user certificate, host CA
trust, keyboard-interactive 2FA, a jump-host chain, algorithm profiles and
hashed known_hosts. File transfers, the file servers and tunnels are tested
end to end against a pretend IOS device (`internal/fakedevice`) through a
jump host and an outbound SOCKS proxy. The importers are tested against
sample configurations whose passwords were encrypted with the original programs' published methods
(`internal/importer/testdata`).

The test lab in `testlab/` (containerlab: two jump host chains, an Arista
cEOS fabric, Linux servers and the demo device) runs end-to-end tests of
jump chains, telnet through jump hosts, file transfer, tunnels, proxies and
file servers: `make -C testlab/clab e2e`.

The user interface has Playwright tests against the built frontend with a
mock backend (`frontend/tests`): `cd frontend && npm run build && npx
playwright test`. Fuzz tests cover every parser that reads files from other
people (`bash build/fuzz.sh`), and `load_test.go` checks 5,000 sessions in
300 groups (import, saving, the state sent to the window, editing all at
once). CI runs all of this on Linux, Windows and macOS, plus govulncheck and
npm audit.
