// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package core

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/icavar/yacm/internal/applog"
	"github.com/icavar/yacm/internal/store"
)

// ---- where the data lives -------------------------------------------------------

// dataLocation says which folder holds sessions, vault and known_hosts.
type dataLocation struct {
	Dir     string // folder in use; "" when the chosen folder is not available
	Default string // the per-user folder (always on this computer)
	Source  string // "portable", "env", "chosen" or "default"
	Wanted  string // the chosen folder, also when it is not available
}

// pointerFile in the default folder names a folder the user chose in
// Settings > Data and backups.
const pointerFile = "location.json"

type pointer struct {
	DataDir string    `json:"dataDir"`
	Changed time.Time `json:"changed"`
}

// dataFiles are what moves with the data folder (logs are handled apart).
var dataFiles = []string{"sessions.json", "vault.json", "known_hosts"}

// resolveDataDir picks the data folder, in this order:
//   - a file named "yacm.portable" next to the executable: "yacm-data" beside
//     it (USB stick use);
//   - the YACM_DATA_DIR environment variable;
//   - the folder chosen in Settings (pointer file in the default folder);
//   - the per-user config folder (%AppData%\YACM, ~/.config/YACM,
//     ~/Library/Application Support/YACM).
//
// Data from the app's earlier name ("Most") is moved over once.
func resolveDataDir() (dataLocation, error) {
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		if _, err := os.Stat(filepath.Join(dir, "most.portable")); err == nil {
			_ = os.Rename(filepath.Join(dir, "most.portable"), filepath.Join(dir, "yacm.portable"))
		}
		if _, err := os.Stat(filepath.Join(dir, "yacm.portable")); err == nil {
			target := filepath.Join(dir, "yacm-data")
			migrate(filepath.Join(dir, "most-data"), target)
			return dataLocation{Dir: target, Default: target, Source: "portable"}, os.MkdirAll(target, 0o700)
		}
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return dataLocation{}, err
	}
	def := filepath.Join(base, appName)
	for _, env := range []string{"YACM_DATA_DIR", "MOST_DATA_DIR"} {
		if d := os.Getenv(env); d != "" {
			return dataLocation{Dir: d, Default: def, Source: "env"}, os.MkdirAll(d, 0o700)
		}
	}
	migrate(filepath.Join(base, "Most"), def)
	if err := os.MkdirAll(def, 0o700); err != nil {
		return dataLocation{}, err
	}
	if p := readPointer(def); p != "" {
		// A chosen folder is never created here: a missing one usually
		// means a disconnected disk or network share.
		if dirExists(p) {
			return dataLocation{Dir: p, Default: def, Source: "chosen", Wanted: p}, nil
		}
		return dataLocation{Default: def, Source: "chosen", Wanted: p}, nil
	}
	return dataLocation{Dir: def, Default: def, Source: "default"}, nil
}

// migrate renames an old data folder to the new one if only the old exists.
func migrate(old, target string) {
	if _, err := os.Stat(target); err == nil {
		return
	}
	if fi, err := os.Stat(old); err == nil && fi.IsDir() {
		if err := os.Rename(old, target); err != nil {
			fmt.Fprintln(os.Stderr, "could not move old data folder:", err)
		}
	}
}

func readPointer(defDir string) string {
	data, err := os.ReadFile(filepath.Join(defDir, pointerFile))
	if err != nil {
		return ""
	}
	var p pointer
	if json.Unmarshal(data, &p) != nil {
		return ""
	}
	return strings.TrimSpace(p.DataDir)
}

func writePointer(defDir, dir string) error {
	path := filepath.Join(defDir, pointerFile)
	if dir == "" || samePath(dir, defDir) {
		err := os.Remove(path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	data, _ := json.MarshalIndent(pointer{DataDir: dir, Changed: time.Now()}, "", "  ")
	return store.WriteFileAtomic(path, data, 0o600)
}

func samePath(a, b string) bool {
	ca, cb := filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.EqualFold(ca, cb)
	}
	return ca == cb
}

// instanceID ties the single-instance lock to the data folder: starting
// YACM again with the same data brings the open window to the front.
func instanceID(dir string) string {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		dir = strings.ToLower(dir)
	}
	sum := sha256.Sum256([]byte(filepath.Clean(dir)))
	return "yacm-" + hex.EncodeToString(sum[:8])
}

// ---- lock file --------------------------------------------------------------------

// lockFile marks a data folder as in use, so YACM on another computer (a
// network share or a synced folder) opens it read-only instead of
// overwriting changes.
const lockFile = "yacm.lock"

// A lock whose owner has not refreshed it for this long is considered left
// over from a crash or a computer that went to sleep.
const (
	lockStale     = 3 * time.Minute
	lockHeartbeat = 45 * time.Second
)

// LockInfo describes who holds a data folder.
type LockInfo struct {
	Host    string    `json:"host"`
	User    string    `json:"user"`
	PID     int       `json:"pid"`
	App     string    `json:"app"`
	Started time.Time `json:"started"`
	Seen    time.Time `json:"seen"`
}

type dirLock struct {
	path   string
	info   LockInfo
	stop   chan struct{}
	once   sync.Once
	onLost func(other LockInfo)
}

func thisMachine() (host, usr string) {
	host, _ = os.Hostname()
	if u, err := user.Current(); err == nil {
		usr = u.Username
		if i := strings.LastIndexAny(usr, `\/`); i >= 0 {
			usr = usr[i+1:]
		}
	}
	return host, usr
}

func readLock(path string) (*LockInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var li LockInfo
	if err := json.Unmarshal(data, &li); err != nil {
		return nil, err
	}
	return &li, nil
}

// lockOwnedElsewhere reports whether li still belongs to a running YACM.
func lockOwnedElsewhere(li *LockInfo, host string) bool {
	if li == nil || time.Since(li.Seen) > lockStale {
		return false
	}
	if strings.EqualFold(li.Host, host) {
		return li.PID != os.Getpid() && pidAlive(li.PID)
	}
	return true
}

// acquireLock takes the lock of dir. When another YACM holds it, the lock is
// not taken and its owner is returned (unless force).
func acquireLock(dir string, force bool) (*dirLock, *LockInfo, error) {
	host, usr := thisMachine()
	path := filepath.Join(dir, lockFile)
	if cur, err := readLock(path); err == nil && !force && lockOwnedElsewhere(cur, host) {
		return nil, cur, nil
	}
	now := time.Now()
	l := &dirLock{path: path, stop: make(chan struct{}),
		info: LockInfo{Host: host, User: usr, PID: os.Getpid(), App: appName + " " + appVersion, Started: now, Seen: now}}
	if err := l.write(); err != nil {
		return nil, nil, err
	}
	go l.heartbeat()
	return l, nil, nil
}

func (l *dirLock) write() error {
	data, _ := json.MarshalIndent(l.info, "", "  ")
	return store.WriteFileAtomic(l.path, data, 0o600)
}

func (l *dirLock) mine(li *LockInfo) bool {
	return li != nil && li.PID == l.info.PID && strings.EqualFold(li.Host, l.info.Host) && li.Started.Equal(l.info.Started)
}

func (l *dirLock) heartbeat() {
	t := time.NewTicker(lockHeartbeat)
	defer t.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-t.C:
			cur, err := readLock(l.path)
			if err == nil && !l.mine(cur) {
				// Someone else took the folder over ("Open anyway").
				if l.onLost != nil {
					l.onLost(*cur)
				}
				return
			}
			l.info.Seen = time.Now()
			_ = l.write()
		}
	}
}

func (l *dirLock) release() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		close(l.stop)
		if cur, err := readLock(l.path); err == nil && l.mine(cur) {
			_ = os.Remove(l.path)
		}
	})
}

// ---- folder checks ----------------------------------------------------------------

// DataFolderInfo describes a folder the user wants to keep the data in.
type DataFolderInfo struct {
	Path     string `json:"path"`
	Exists   bool   `json:"exists"`
	Writable bool   `json:"writable"`
	HasData  bool   `json:"hasData"`  // sessions.json or vault.json present
	HasVault bool   `json:"hasVault"` // vault.json present
	Sessions int    `json:"sessions"`
	Cloud    string `json:"cloud,omitempty"` // OneDrive, Dropbox, …
	Network  bool   `json:"network"`
	Current  bool   `json:"current"` // the folder in use now
	Problem  string `json:"problem,omitempty"`
}

var cloudNames = []struct{ match, name string }{
	{"onedrive", "OneDrive"}, {"dropbox", "Dropbox"}, {"google drive", "Google Drive"}, {"googledrive", "Google Drive"},
	{"my drive", "Google Drive"}, {"icloud", "iCloud Drive"}, {"mobile documents", "iCloud Drive"},
	{"synologydrive", "Synology Drive"}, {"synology drive", "Synology Drive"}, {"nextcloud", "Nextcloud"},
	{"owncloud", "ownCloud"}, {"box sync", "Box"}, {"pcloud", "pCloud"}, {"seafile", "Seafile"}, {"sharepoint", "SharePoint"},
}

func cloudFolder(path string) string {
	l := strings.ToLower(filepath.ToSlash(path))
	for _, c := range cloudNames {
		if strings.Contains(l, c.match) {
			return c.name
		}
	}
	return ""
}

// networkFolder recognises UNC paths, mapped network drives and network
// file systems.
func networkFolder(path string) bool {
	if strings.HasPrefix(path, `\\`) || strings.HasPrefix(path, "//") {
		return true
	}
	return isNetworkDrive(path)
}

// linuxNetworkMount looks the folder up in /proc/mounts.
func linuxNetworkMount(path string) bool {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return false
	}
	defer f.Close()
	best, fstype := "", ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 {
			continue
		}
		mp := fields[1]
		if (path == mp || strings.HasPrefix(path, strings.TrimSuffix(mp, "/")+"/")) && len(mp) > len(best) {
			best, fstype = mp, fields[2]
		}
	}
	switch fstype {
	case "nfs", "nfs4", "cifs", "smb3", "smbfs", "fuse.sshfs", "davfs", "fuse.rclone", "9p":
		return true
	}
	return false
}

func inspectFolder(path, current string) DataFolderInfo {
	info := DataFolderInfo{Path: path, Cloud: cloudFolder(path), Network: networkFolder(path), Current: current != "" && samePath(path, current)}
	fi, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			info.Problem = "The folder does not exist; it will be created."
			parent := filepath.Dir(path)
			for !dirExists(parent) && filepath.Dir(parent) != parent {
				parent = filepath.Dir(parent)
			}
			info.Writable = dirExists(parent) && canWrite(parent)
			if !info.Writable {
				info.Problem = "The folder does not exist and cannot be created here."
			}
			return info
		}
		info.Problem = err.Error()
		return info
	}
	if !fi.IsDir() {
		info.Problem = "This is a file, not a folder."
		return info
	}
	info.Exists = true
	info.Writable = canWrite(path)
	if !info.Writable {
		info.Problem = "YACM cannot write to this folder."
	}
	if _, err := os.Stat(filepath.Join(path, "vault.json")); err == nil {
		info.HasVault, info.HasData = true, true
	}
	if data, err := os.ReadFile(filepath.Join(path, "sessions.json")); err == nil {
		info.HasData = true
		var f struct {
			Nodes []struct {
				IsGroup bool `json:"isGroup"`
			} `json:"nodes"`
		}
		if json.Unmarshal(data, &f) == nil {
			for _, n := range f.Nodes {
				if !n.IsGroup {
					info.Sessions++
				}
			}
		}
	}
	return info
}

func canWrite(dir string) bool {
	f, err := os.CreateTemp(dir, ".yacm-write-test-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	return os.Remove(name) == nil
}

// ---- moving the data ----------------------------------------------------------------

func fileSum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// copyVerified copies src to dst through a temporary file and checks that
// the copy reads back identical.
func copyVerified(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".yacm-copy"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	a, err1 := fileSum(src)
	b, err2 := fileSum(tmp)
	if err1 != nil || err2 != nil || a != b {
		os.Remove(tmp)
		return fmt.Errorf("the copy of %s does not match the original", filepath.Base(src))
	}
	return os.Rename(tmp, dst)
}

// copyTree copies a folder (session logs) and returns how many files.
func copyTree(src, dst string) (int, error) {
	n := 0
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		n++
		return copyVerified(p, target)
	})
	return n, err
}

// moveData copies the data files (and the default logs folder) from one
// folder to another. Nothing is deleted from "from"; the caller removes the
// old files once the new location is recorded.
func moveData(from, to string, withLogs bool) (logs int, err error) {
	if err := os.MkdirAll(to, 0o700); err != nil {
		return 0, err
	}
	for _, f := range dataFiles {
		src := filepath.Join(from, f)
		if _, err := os.Stat(src); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err := copyVerified(src, filepath.Join(to, f)); err != nil {
			return 0, err
		}
	}
	// Backups go along with the data they belong to.
	if dirExists(filepath.Join(from, "backups")) {
		if _, err := copyTree(filepath.Join(from, "backups"), filepath.Join(to, "backups")); err != nil {
			return 0, err
		}
	}
	if withLogs && dirExists(filepath.Join(from, "logs")) {
		return copyTree(filepath.Join(from, "logs"), filepath.Join(to, "logs"))
	}
	return 0, nil
}

// backupData moves YACM files already in a folder aside before they are
// replaced.
func backupData(dir string) (string, error) {
	bak := filepath.Join(dir, "backup-"+time.Now().Format("2006-01-02-150405"))
	moved := false
	for _, f := range dataFiles {
		src := filepath.Join(dir, f)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if err := os.MkdirAll(bak, 0o700); err != nil {
			return "", err
		}
		if err := os.Rename(src, filepath.Join(bak, f)); err != nil {
			return "", err
		}
		moved = true
	}
	if !moved {
		return "", nil
	}
	return bak, nil
}

// removeOldData deletes the data files that were moved, and leaves a note
// for anyone who looks into the old folder.
func removeOldData(from, to string, withLogs bool) {
	for _, f := range dataFiles {
		_ = os.Remove(filepath.Join(from, f))
	}
	_ = os.RemoveAll(filepath.Join(from, "backups"))
	if withLogs {
		_ = os.RemoveAll(filepath.Join(from, "logs"))
	}
	note := fmt.Sprintf("YACM keeps its sessions, passwords and trusted host keys in another folder since %s:\r\n\r\n%s\r\n\r\nChange it in YACM under Settings > Data and backups.\r\n", time.Now().Format("2006-01-02 15:04"), to)
	_ = os.WriteFile(filepath.Join(from, "DATA-MOVED.txt"), []byte(note), 0o600)
}

// ---- restarting -------------------------------------------------------------------------

// waitForPID is used by a restarted YACM: it waits until the old process
// has released the data folder and the single-instance lock.
func waitForPID(args []string) []string {
	var rest []string
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, "--wait-pid="); ok {
			if pid, err := strconv.Atoi(v); err == nil {
				for i := 0; i < 100 && pidAlive(pid); i++ {
					time.Sleep(100 * time.Millisecond)
				}
			}
			continue
		}
		rest = append(rest, a)
	}
	return rest
}

func hasArg(args []string, name string) bool {
	for _, a := range args {
		if a == name {
			return true
		}
	}
	return false
}

// relaunch starts YACM again and quits this instance; used after the data
// folder changed, so every part of the app opens the new folder cleanly.
func (a *App) relaunch(extra ...string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	var args []string
	for _, x := range os.Args[1:] {
		if !strings.HasPrefix(x, "--wait-pid=") && x != "--take-over" {
			args = append(args, x)
		}
	}
	args = append(args, extra...)
	args = append(args, "--wait-pid="+strconv.Itoa(os.Getpid()))
	cmd := exec.Command(exe, args...)
	cmd.Env = os.Environ()
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = cmd.Process.Release()
	a.quitting.Store(true)
	go func() {
		time.Sleep(150 * time.Millisecond)
		if a.ctx != nil {
			wruntime.Quit(a.ctx)
		}
	}()
	return nil
}

// ---- API for the frontend ---------------------------------------------------------------

// DataFolderStatus tells the UI where the data is and whether it can be used.
type DataFolderStatus struct {
	Path        string    `json:"path"`
	Default     string    `json:"default"`
	Source      string    `json:"source"` // portable, env, chosen, default
	Unavailable bool      `json:"unavailable"`
	Wanted      string    `json:"wanted,omitempty"`
	ReadOnly    bool      `json:"readOnly"`
	LockedBy    *LockInfo `json:"lockedBy,omitempty"`
	LockLost    bool      `json:"lockLost"`
	Cloud       string    `json:"cloud,omitempty"`
	Network     bool      `json:"network"`
}

func (a *App) DataFolderStatus() DataFolderStatus {
	a.locMu.Lock()
	defer a.locMu.Unlock()
	st := DataFolderStatus{Path: a.loc.Dir, Default: a.loc.Default, Source: a.loc.Source, Wanted: a.loc.Wanted,
		Unavailable: a.loc.Dir == "", ReadOnly: a.store.ReadOnly(), LockedBy: a.lockedBy, LockLost: a.lockLost}
	if a.loc.Dir != "" {
		st.Cloud, st.Network = cloudFolder(a.loc.Dir), networkFolder(a.loc.Dir)
	}
	return st
}

// InspectDataFolder checks a folder before the data moves there.
func (a *App) InspectDataFolder(path string) (DataFolderInfo, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return DataFolderInfo{}, errors.New("choose a folder")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return DataFolderInfo{}, err
	}
	return inspectFolder(abs, a.loc.Dir), nil
}

// ChangeDataFolder makes YACM use another folder and restarts it.
// mode "move" copies the current data there (verified), then removes it
// from the old folder; "replace" does the same but first moves YACM data
// already in the target aside into a backup folder; "use" switches to the
// data already in the target (or starts empty) and leaves the old data.
func (a *App) ChangeDataFolder(path, mode string) (err error) {
	defer logOp("change data folder ("+mode+")", &err)
	if a.loc.Source == "portable" || a.loc.Source == "env" {
		return errors.New("the data folder is set by " + map[string]string{"portable": "portable mode (yacm.portable next to the program)", "env": "the YACM_DATA_DIR environment variable"}[a.loc.Source] + "; change it there")
	}
	if n := a.openTabCount(); n > 0 {
		return fmt.Errorf("close the %d open session(s) first: YACM restarts to use the new folder", n)
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("choose a folder")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if a.loc.Dir != "" && samePath(abs, a.loc.Dir) {
		return errors.New("YACM already uses this folder")
	}
	if a.loc.Dir != "" && strings.HasPrefix(strings.ToLower(abs)+string(filepath.Separator), strings.ToLower(filepath.Clean(a.loc.Dir))+string(filepath.Separator)) {
		return errors.New("choose a folder outside the current data folder")
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return fmt.Errorf("cannot create the folder: %w", err)
	}
	if !canWrite(abs) {
		return errors.New("YACM cannot write to this folder")
	}
	if li, err := readLock(filepath.Join(abs, lockFile)); err == nil {
		host, _ := thisMachine()
		if lockOwnedElsewhere(li, host) {
			return fmt.Errorf("YACM on %s (%s) is using that folder right now", li.Host, li.User)
		}
	}
	target := inspectFolder(abs, a.loc.Dir)
	withLogs := a.store.Settings().DefaultLogDir == ""
	switch mode {
	case "move", "replace":
		if a.loc.Dir == "" {
			return errors.New("the current data folder is not available, so there is nothing to move")
		}
		if target.HasData && mode == "move" {
			return errors.New("the folder already has YACM data: choose to use it or to replace it")
		}
		if target.HasData {
			if _, err := backupData(abs); err != nil {
				return fmt.Errorf("could not set the existing data aside: %w", err)
			}
		}
		if _, err := moveData(a.loc.Dir, abs, withLogs); err != nil {
			return fmt.Errorf("copying the data failed, nothing was changed: %w", err)
		}
	case "use":
	default:
		return fmt.Errorf("unknown mode %q", mode)
	}
	if err := writePointer(a.loc.Default, abs); err != nil {
		return fmt.Errorf("could not record the new folder: %w", err)
	}
	if mode != "use" {
		removeOldData(a.loc.Dir, abs, withLogs)
	}
	return a.restart()
}

// ResetDataFolder goes back to the default folder ("move" brings the data
// along, "use" opens what is there).
func (a *App) ResetDataFolder(mode string) error {
	return a.ChangeDataFolder(a.loc.Default, mode)
}

// RetryDataFolder restarts YACM to look for an unavailable or busy folder
// again.
func (a *App) RetryDataFolder() error { return a.restart() }

// TakeOverDataFolder opens a folder that YACM elsewhere seems to use.
func (a *App) TakeOverDataFolder() error {
	if n := a.openTabCount(); n > 0 {
		return fmt.Errorf("close the %d open session(s) first", n)
	}
	return a.restart("--take-over")
}

// OpenDataFolder shows the data folder in the file manager.
func (a *App) OpenDataFolder() error {
	if a.loc.Dir == "" {
		return errors.New("the data folder is not available")
	}
	return openInFileManager(a.loc.Dir)
}

// OpenLogFolder shows the folder with yacm.log (on this computer).
func (a *App) OpenLogFolder() error {
	d := applog.Dir()
	if d == "" {
		return errors.New("no log folder")
	}
	return openInFileManager(d)
}

func openInFileManager(dir string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("explorer", dir)
	case "darwin":
		cmd = exec.Command("open", dir)
	default:
		cmd = exec.Command("xdg-open", dir)
	}
	return cmd.Start()
}
