// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package core

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v2/pkg/options"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/icavar/yacm/internal/applog"
	"github.com/icavar/yacm/internal/backup"
	"github.com/icavar/yacm/internal/expect"
	"github.com/icavar/yacm/internal/fileserver"
	"github.com/icavar/yacm/internal/logmask"
	"github.com/icavar/yacm/internal/sshconn"
	"github.com/icavar/yacm/internal/store"
	"github.com/icavar/yacm/internal/vault"
)

// conn is what every transport provides.
type conn interface {
	io.ReadWriteCloser
	Resize(cols, rows int) error
	SendBreak() error
}

type tab struct {
	id     string
	node   *store.Node
	mu     sync.Mutex
	conn   conn
	engine *expect.Engine
	cancel context.CancelFunc
	logF   *os.File
	strip  expect.Stripper
	state  string
	cols   int
	rows   int
	after  bool // after-commands already handled
	eff    *store.Effective
	stamp  bool            // log timestamps
	midLn  bool            // log is in the middle of a line
	mask   *logmask.Masker // hides secrets in the log file (nil: off)
	watch  promptWatch
	ticket string // change/ticket number for the log file name

	input      inputLine          // what is typed on the command line (reload watch)
	reloadAt   time.Time          // when a reload command was typed
	waitCancel context.CancelFunc // stops waiting for the device
	tun        *tabTunnels        // SSH port forwards
	capture    *capture           // command output going to a file (capture.go)
}

// App is bound to the frontend.
type App struct {
	ctx     context.Context
	dataDir string

	store *store.Store
	vault *vault.Vault
	kh    *sshconn.KnownHosts

	tabsMu sync.Mutex
	tabs   map[string]*tab

	promptMu sync.Mutex
	prompts  map[string]chan PromptAnswer

	filesMu    sync.Mutex
	files      map[string]*filesConn   // open device file systems (Files tabs, Send/Get file)
	scpOnly    map[string]bool         // sessions whose device refused SFTP (no second attempt)
	serialOnly map[string]bool         // sessions whose device takes one channel at a time
	xfers      map[string]*transferJob // running transfers

	fsrvOnce sync.Once
	fsrv     *fileserver.Manager // TFTP, HTTP and SCP servers for devices
	emitter  func(name string, data any)

	shown atomic.Bool // main window has been shown (see window.go)

	loc          dataLocation // where the data folder is (see datadir.go)
	locMu        sync.Mutex
	lock         *dirLock  // lock file of the data folder
	lockedBy     *LockInfo // YACM elsewhere holds the folder: this window is read-only
	lockLost     bool      // another YACM took the folder over while open
	quitting     atomic.Bool
	tabsDone     atomic.Bool
	backups      *backup.Manager // copies of the data files (nil: folder not available)
	teamSeenOnce sync.Once
	teamSeenS    *teamSeen // where team sessions pointed at the last connection
	teamDiffMu   sync.Mutex
	teamDiffs    []TeamDiff     // library changes not shown yet
	ownPublish   map[string]int // library path -> revision you published (under teamMu)
	snapMu       sync.Mutex
	rekeyWG      sync.WaitGroup              // vault copies in the backups being re-encrypted
	snapLast     map[string]time.Time        // last event backup per reason                 // open-tabs.json is final for this run (see opentabs.go)
	restart      func(extra ...string) error // relaunch; replaced in tests

	teamMu sync.Mutex
	teamIO sync.Mutex            // one refresh at a time (teamlib.go)
	team   map[string]*loadedLib // followed team libraries

	importStop atomic.Bool // CancelImport was pressed
	previewMu  sync.Mutex
	preview    *previewCache // last foreign preview, reused by the import
}

// NewApp creates the application on a data folder (tests use this; the
// program starts through newAppFor).
func NewApp(dataDir string) (*App, error) {
	return newApp(dataLocation{Dir: dataDir, Default: dataDir, Source: "default"})
}

func newApp(loc dataLocation) (*App, error) {
	dir := loc.Dir
	unavailable := dir == ""
	if unavailable {
		// Nothing may be written anywhere until the user decides; an empty,
		// read-only store backs the "folder not available" screen.
		dir = filepath.Join(os.TempDir(), fmt.Sprintf("yacm-unavailable-%d", os.Getpid()))
	}
	st, err := store.Open(filepath.Join(dir, "sessions.json"))
	if err != nil {
		return nil, err
	}
	a := &App{
		dataDir: loc.Dir,
		loc:     loc,
		store:   st,
		vault:   vault.New(filepath.Join(dir, "vault.json")),
		tabs:    map[string]*tab{},
		prompts: map[string]chan PromptAnswer{},
		files:   map[string]*filesConn{},
		xfers:   map[string]*transferJob{},
	}
	a.kh = sshconn.NewKnownHosts(filepath.Join(dir, "known_hosts"), st.Settings().UseSystemKnownHost)
	a.kh.Warn = func(m string) { applog.Warnf("trusted host keys: %s", m) }
	a.restart = a.relaunch
	if unavailable {
		a.setReadOnly()
	} else {
		a.backups = backup.New(dir, appName+" "+appVersion)
		st.SetBeforeSave(func() {
			if err := a.backups.Auto(); err != nil {
				applog.Warnf("automatic backup: %v", err)
			}
		})
	}
	a.refreshTeam(true)
	return a, nil
}

// newAppFor opens the data folder for the program: it takes the folder's
// lock, or opens it read-only when YACM elsewhere has it open.
func newAppFor(loc dataLocation, takeOver bool) (*App, error) {
	a, err := newApp(loc)
	if err != nil || loc.Dir == "" {
		return a, err
	}
	l, other, err := acquireLock(loc.Dir, takeOver)
	switch {
	case err != nil:
		fmt.Fprintln(os.Stderr, "data folder lock:", err) // e.g. a read-only medium; carry on
	case other != nil:
		a.lockedBy = other
		a.setReadOnly()
	default:
		a.lock = l
		l.onLost = a.lostLock
	}
	return a, nil
}

func (a *App) setReadOnly() {
	a.store.SetReadOnly(true)
	a.vault.SetReadOnly(true)
	a.kh.ReadOnly = true
}

// lostLock runs when YACM elsewhere took the data folder over.
func (a *App) lostLock(other LockInfo) {
	a.locMu.Lock()
	a.lockLost, a.lockedBy = true, &other
	a.locMu.Unlock()
	a.setReadOnly()
	a.emit("data:status", a.DataFolderStatus())
}

// secondInstance brings the window forward when YACM is started again with
// the same data folder.
func (a *App) secondInstance(options.SecondInstanceData) {
	if a.ctx == nil {
		return
	}
	wruntime.WindowUnminimise(a.ctx)
	wruntime.WindowShow(a.ctx)
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.emitter = func(name string, data any) { wruntime.EventsEmit(ctx, name, data) }
	a.showFallback(ctx)
	go a.watchTeam()
}

func (a *App) shutdown(ctx context.Context) {
	a.tabsMu.Lock()
	tabs := make([]*tab, 0, len(a.tabs))
	for _, t := range a.tabs {
		tabs = append(tabs, t)
	}
	a.tabsMu.Unlock()
	a.closeOpenTabs()
	a.closeAllFiles()
	if a.fsrv != nil {
		a.fsrv.StopAll()
	}
	for _, t := range tabs {
		a.closeConn(t, false)
	}
	a.vault.Lock()
	a.lock.release()
}

func (a *App) beforeClose(ctx context.Context) bool {
	a.rememberWindow(ctx)
	n := a.openTabCount()
	if n == 0 || a.quitting.Load() {
		return false
	}
	res, err := wruntime.MessageDialog(ctx, wruntime.MessageDialogOptions{
		Type:          wruntime.QuestionDialog,
		Title:         "Quit YACM?",
		Message:       fmt.Sprintf("%d session(s) are still connected. Quit and disconnect them?", n),
		Buttons:       []string{"Quit", "Cancel"},
		DefaultButton: "Cancel",
		CancelButton:  "Cancel",
	})
	if err != nil {
		return false
	}
	return res != "Quit" && res != "Yes" && res != "Ok"
}

func (a *App) emit(name string, data any) {
	if a.emitter != nil {
		a.emitter(name, data)
	}
}

func (a *App) openTabCount() int {
	a.tabsMu.Lock()
	defer a.tabsMu.Unlock()
	n := 0
	for _, t := range a.tabs {
		t.mu.Lock()
		if t.state == "connected" || t.state == "connecting" {
			n++
		}
		t.mu.Unlock()
	}
	return n
}
