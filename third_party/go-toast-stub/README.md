Stand-in for `git.sr.ht/~jackmordaunt/go-toast/v2`.

Wails links this package only for Windows toast notifications, which YACM
does not use. The build machine that produced the release could not reach
git.sr.ht, so this stub provides the same API and makes notifications a
no-op. To use the real library, delete the `replace` line for it in
`go.mod` and run `go mod tidy`.
