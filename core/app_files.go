// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package core

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/crypto/ssh"

	"github.com/icavar/yacm/internal/applog"
	"github.com/icavar/yacm/internal/sshconn"
	"github.com/icavar/yacm/internal/store"
	"github.com/icavar/yacm/internal/xfer"
)

// ---- file transfer (SFTP / SCP) ------------------------------------------------

// filesConn is an open file system of a device: a Files tab, or a Send /
// Get file started from a terminal tab.
type filesConn struct {
	id     string
	name   string
	remote xfer.Remote
	link   *xfer.Link // reconnects when the device closed the connection
	mu     sync.Mutex
	own    *sshconn.Client // nil while the connection belongs to a terminal tab
}

// redialFiles gives the file commands a new connection to the device: over
// the same jump hosts when there is one of our own, else a new one (Cisco
// IOS closes the connection when its one channel closes).
func (a *App) redialFiles(f *filesConn, node *store.Node, ownerID string) func(ctx context.Context) (*ssh.Client, error) {
	return func(ctx context.Context) (*ssh.Client, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.own != nil {
			// Over the same jump host login first, at most 10 s; then a
			// whole new connection (some jump hosts hold the second one).
			rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := f.own.Reconnect(rctx)
			cancel()
			if err == nil {
				return f.own.SSH(), nil
			}
			applog.Infof("files %s: reconnecting over the same jump host login failed (%v): new login", node.Name, err)
			f.own.Close()
			f.own = nil
		}
		cl, err := a.dialSession(ctx, node, ownerID)
		if err != nil {
			return nil, err
		}
		f.own = cl
		return cl.SSH(), nil
	}
}

// FilesOpenRequest opens the file system of a session. TabID borrows the
// connection of a connected SSH terminal tab (no second login); OwnerID is
// the tab that shows prompts (host key, password).
type FilesOpenRequest struct {
	NodeID  string `json:"nodeId"`
	TabID   string `json:"tabId"`
	OwnerID string `json:"ownerId"`
	// SCP uses SCP and exec commands even when the device offers SFTP.
	SCP bool `json:"scp"`
}

// FilesInfo describes an opened file system.
type FilesInfo struct {
	ID      string      `json:"id"`
	Name    string      `json:"name"`
	Kind    string      `json:"kind"` // sftp, scp
	Home    string      `json:"home"`
	Roots   []xfer.Root `json:"roots"`
	Summary string      `json:"summary"`
	// Shared is true when the connection of a terminal tab is used.
	Shared bool `json:"shared"`
}

// TransferEvent is emitted as "xfer:progress".
type TransferEvent struct {
	ID      string `json:"id"`
	FilesID string `json:"filesId"`
	Name    string `json:"name"`
	Dir     string `json:"dir"` // up, down, cmd
	Local   string `json:"local"`
	Remote  string `json:"remote"`
	Done    int64  `json:"done"`
	Total   int64  `json:"total"`
	State   string `json:"state"` // running, verifying, done, error, cancelled
	Error   string `json:"error,omitempty"`
	MD5     string `json:"md5,omitempty"`
	// Verified: "" not checked, "ok", "mismatch", "unavailable".
	Verified string `json:"verified,omitempty"`
	// Hint: advice shown while the transfer runs (how to make it faster).
	Hint string `json:"hint,omitempty"`
}

// scpWindowHint is shown with large SCP uploads: on network devices their
// speed is set by the device's SSH window and the round trip time.
const scpWindowHint = "Slow? SCP to a network device is as fast as its SSH window allows: on Cisco IOS-XE, a larger \"ip ssh window-size\" (or \"ip ssh bulk-mode\") speeds it up."

var errNoFiles = errors.New("the file connection is closed; open it again")

func (a *App) filesGet(id string) (*filesConn, error) {
	a.filesMu.Lock()
	defer a.filesMu.Unlock()
	f := a.files[id]
	if f == nil {
		return nil, errNoFiles
	}
	return f, nil
}

// borrowSSH returns the device connection of a connected SSH tab.
// spareChannel checks that the connection takes one more session channel
// (Cisco IOS takes one per connection, and a terminal tab holds it).
func spareChannel(c *ssh.Client) error {
	s, err := c.NewSession()
	if err != nil {
		return err
	}
	_ = s.Close()
	return nil
}

// isChannelRefused reports a device that refused to open a channel.
func isChannelRefused(err error) bool {
	var oce *ssh.OpenChannelError
	return errors.As(err, &oce)
}

func (a *App) borrowSSH(tabID string) (*ssh.Client, *tab, error) {
	t, err := a.getTab(tabID)
	if err != nil {
		return nil, nil, err
	}
	t.mu.Lock()
	c, state := t.conn, t.state
	t.mu.Unlock()
	if sc, ok := c.(*sshconn.Conn); ok && state == "connected" {
		if cl := sc.SSHClient(); cl != nil {
			return cl, t, nil
		}
	}
	return nil, t, nil
}

// sshHops builds the SSH hops of a session: jump hosts, then the device.
func (a *App) sshHops(node *store.Node) ([]sshconn.Hop, *store.Effective, error) {
	ss := node.Session
	if ss == nil {
		return nil, nil, errors.New("not a session")
	}
	if !store.KnownProtocol(ss.Protocol) {
		return nil, nil, newerTypeError(node)
	}
	if ss.Protocol != store.ProtoSSH {
		return nil, nil, fmt.Errorf("%s is a %s session: file transfer and tunnels need SSH. For telnet and serial, let the device copy from the file servers (Servers, bottom left)", node.Name, ss.Protocol)
	}
	eff, err := a.store.Effective(node)
	if err != nil {
		return nil, nil, err
	}
	hops := a.jumpHopsQuiet(node, eff)
	hops = append(hops, sshconn.Hop{
		Host: ss.Host, Port: ss.Port, User: eff.User, Auth: eff.Auth, KeyPath: eff.KeyPath, CertPath: eff.CertPath,
		Password:      a.secret(eff.PasswordKey),
		Passphrase:    a.secret(eff.PassphraseKey),
		PasswordKey:   a.savableKey(node, eff.PasswordKey),
		PassphraseKey: a.savableKey(node, eff.PassphraseKey),
		UserKey:       userKey(node, -1),
	})
	return hops, eff, nil
}

// dialSession opens a new SSH connection (no shell) to a session.
func (a *App) dialSession(ctx context.Context, node *store.Node, ownerID string) (*sshconn.Client, error) {
	hops, eff, err := a.sshHops(node)
	if err != nil {
		return nil, err
	}
	ss := node.Session
	return sshconn.DialClient(ctx, sshconn.Config{
		Hops: hops, Profile: eff.Algorithms, ConnectTimeout: time.Duration(ss.ConnectTimeout) * time.Second,
		KeepAlive:  30 * time.Second,
		KnownHosts: a.kh, Prompter: tabPrompter{a, ownerID, node.Name},
		SaveSecret: func(name, v string) error { return a.vault.Set(name, v) },
		SaveUser:   a.saveUser,
		Status:     func(m string) { applog.Infof("files %s: %s", node.Name, m) },
		Dial:       a.proxyDial(eff),
	})
}

// FilesOpen connects to the file system of a session.
func (a *App) FilesOpen(req FilesOpenRequest) (FilesInfo, error) {
	var node *store.Node
	var client *ssh.Client
	var own *sshconn.Client
	summary, note := "", ""
	if req.TabID != "" {
		cl, t, err := a.borrowSSH(req.TabID)
		if err != nil {
			return FilesInfo{}, err
		}
		node = t.node
		if cl != nil {
			switch err := spareChannel(cl); {
			case err == nil:
				client, summary = cl, "using the connection of the open session"
			case isChannelRefused(err):
				// Cisco IOS: one session channel per connection, and the
				// terminal has it.
				note = "the device allows one channel per SSH connection"
				applog.Infof("files %s: %s, own connection", node.Name, note)
			default:
				return FilesInfo{}, err
			}
		}
	} else {
		n, err := a.store.Get(req.NodeID)
		if err != nil {
			return FilesInfo{}, err
		}
		node = n
	}
	dial := func() error {
		if node.IsGroup {
			return errors.New("choose a session, not a group")
		}
		cl, err := a.dialSession(context.Background(), node, req.OwnerID)
		if err != nil {
			return err
		}
		own, client, summary = cl, cl.SSH(), cl.Summary()
		if note != "" {
			summary = "own connection: " + note
		}
		return nil
	}
	if client == nil {
		if err := dial(); err != nil {
			return FilesInfo{}, err
		}
	}
	f := &filesConn{id: store.NewID(), name: node.Name, own: own}
	f.link = xfer.NewLink(client, a.redialFiles(f, node, req.OwnerID))
	f.link.Logf = func(format string, args ...any) {
		applog.Infof("files %s: "+format, append([]any{node.Name}, args...)...)
	}
	f.link.OnSerial = func() {
		a.filesMu.Lock()
		if a.serialOnly == nil {
			a.serialOnly = map[string]bool{}
		}
		a.serialOnly[node.ID] = true
		a.filesMu.Unlock()
	}
	a.filesMu.Lock()
	scp := req.SCP || a.scpOnly[node.ID]
	if a.serialOnly[node.ID] {
		f.link.SetSerial()
	}
	a.filesMu.Unlock()
	if scp {
		f.remote = xfer.OpenSCP(f.link)
	} else {
		start := time.Now()
		r, why := xfer.OpenWhy(f.link)
		f.remote = r
		if why == "" {
			applog.Infof("files %s: SFTP (started in %v)", node.Name, time.Since(start).Round(time.Millisecond))
		} else {
			applog.Infof("files %s: no SFTP (%s, after %v): SCP and device commands", node.Name, why, time.Since(start).Round(time.Millisecond))
		}
		if r.Kind() == "scp" {
			// The device refused SFTP: no second attempt for this session.
			// Cisco IOS XE also closes the connection after it; the next
			// command reconnects.
			a.filesMu.Lock()
			if a.scpOnly == nil {
				a.scpOnly = map[string]bool{}
			}
			a.scpOnly[node.ID] = true
			a.filesMu.Unlock()
		}
	}
	remote := f.remote
	a.filesMu.Lock()
	a.files[f.id] = f
	a.filesMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	info := FilesInfo{ID: f.id, Name: node.Name, Kind: remote.Kind(), Home: remote.Home(ctx), Summary: summary, Shared: own == nil}
	info.Roots = remote.Roots(ctx)
	applog.Infof("files %s: opened (%s)", node.Name, info.Kind)
	return info, nil
}

// FilesClose closes a file system (and its connection unless it belongs
// to a terminal tab). Running transfers of it are cancelled.
func (a *App) FilesClose(id string) {
	a.filesMu.Lock()
	f := a.files[id]
	delete(a.files, id)
	var cancels []context.CancelCauseFunc
	for _, x := range a.xfers {
		if x.filesID == id {
			cancels = append(cancels, x.cancel)
		}
	}
	a.filesMu.Unlock()
	for _, c := range cancels {
		c(errFilesClosed)
	}
	if f != nil {
		_ = f.remote.Close()
		f.mu.Lock()
		own := f.own
		f.own = nil
		f.mu.Unlock()
		if own != nil {
			_ = own.Close()
		}
	}
}

func (a *App) closeAllFiles() {
	a.filesMu.Lock()
	ids := make([]string, 0, len(a.files))
	for id := range a.files {
		ids = append(ids, id)
	}
	a.filesMu.Unlock()
	for _, id := range ids {
		a.FilesClose(id)
	}
}

func opCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 2*time.Minute)
}

// FilesList lists a folder on the device.
func (a *App) FilesList(id, dir string) ([]xfer.Entry, error) {
	f, err := a.filesGet(id)
	if err != nil {
		return nil, err
	}
	ctx, cancel := opCtx()
	defer cancel()
	es, err := f.remote.List(ctx, dir)
	if es == nil && err == nil {
		es = []xfer.Entry{}
	}
	return es, err
}

// FilesFree reports the free space where path lives (-1: unknown).
func (a *App) FilesFree(id, path string) int64 {
	f, err := a.filesGet(id)
	if err != nil {
		return -1
	}
	ctx, cancel := opCtx()
	defer cancel()
	return f.remote.Free(ctx, path)
}

// FilesRemove deletes a file (or an empty folder) on the device.
func (a *App) FilesRemove(id, path string) error {
	f, err := a.filesGet(id)
	if err != nil {
		return err
	}
	ctx, cancel := opCtx()
	defer cancel()
	err = f.remote.Remove(ctx, path)
	if err == nil {
		applog.Infof("files %s: deleted %s", f.name, path)
	}
	return err
}

// FilesMkdir creates a folder on the device.
func (a *App) FilesMkdir(id, path string) error {
	f, err := a.filesGet(id)
	if err != nil {
		return err
	}
	ctx, cancel := opCtx()
	defer cancel()
	return f.remote.Mkdir(ctx, path)
}

// FilesRename renames or moves a file on the device.
func (a *App) FilesRename(id, from, to string) error {
	f, err := a.filesGet(id)
	if err != nil {
		return err
	}
	ctx, cancel := opCtx()
	defer cancel()
	return f.remote.Rename(ctx, from, to)
}

// FilesMD5 asks the device for the MD5 checksum of a file.
func (a *App) FilesMD5(id, path string) (string, error) {
	f, err := a.filesGet(id)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	return f.link.MD5(ctx, path)
}

// ---- transfers ------------------------------------------------------------------

type transferJob struct {
	filesID string
	cancel  context.CancelCauseFunc
}

// Why a transfer was cancelled, for the log.
var (
	errCancelledByUser = errors.New("by the user")
	errFilesClosed     = errors.New("because its file connection was closed")
)

func (a *App) startTransfer(f *filesConn, ev TransferEvent, work func(ctx context.Context, ev *TransferEvent, tick func(int64)) error) string {
	return a.startJob(f.id, f.name, ev, work)
}

// startJob runs work as a transfer the frontend lists (filesID may be empty
// for a job that uses no file connection, such as command output through a
// session); name is the device for the log.
func (a *App) startJob(filesID, name string, ev TransferEvent, work func(ctx context.Context, ev *TransferEvent, tick func(int64)) error) string {
	ev.ID, ev.FilesID, ev.State = store.NewID(), filesID, "running"
	ctx, cancel := context.WithCancelCause(context.Background())
	a.filesMu.Lock()
	a.xfers[ev.ID] = &transferJob{filesID: filesID, cancel: cancel}
	a.filesMu.Unlock()
	go func() {
		defer applog.Recover("transfer "+ev.Name, nil)
		defer cancel(nil)
		var mu sync.Mutex
		last := time.Time{}
		// A device that takes one channel at a time: the transfer waits
		// for the others ("queued").
		ctx := xfer.WithWaitNotice(ctx, func(waiting bool) {
			mu.Lock()
			defer mu.Unlock()
			switch {
			case waiting && ev.State == "running":
				ev.State = "queued"
			case !waiting && ev.State == "queued":
				ev.State = "running"
			default:
				return
			}
			a.emit("xfer:progress", ev)
		})
		tick := func(n int64) {
			mu.Lock()
			defer mu.Unlock()
			ev.Done = n
			if time.Since(last) > 200*time.Millisecond {
				last = time.Now()
				a.emit("xfer:progress", ev)
			}
		}
		a.emit("xfer:progress", ev)
		started := time.Now()
		err := work(ctx, &ev, tick)
		mu.Lock()
		switch {
		case ctx.Err() != nil:
			ev.State, ev.Error = "cancelled", ""
		case err != nil:
			ev.State, ev.Error = "error", err.Error()
		default:
			ev.State = "done"
		}
		final := ev
		mu.Unlock()
		a.filesMu.Lock()
		delete(a.xfers, ev.ID)
		a.filesMu.Unlock()
		what := map[string]string{"up": "upload", "down": "download", "cmd": "command output"}[ev.Dir]
		took := time.Since(started)
		speed := ""
		if s := took.Seconds(); s >= 0.1 && final.Done > 0 {
			speed = ", " + humanBytes(int64(float64(final.Done)/s)) + "/s"
		}
		if final.State == "done" {
			applog.Infof("files %s: %s %s ↔ %s finished (%d bytes in %v%s%s)", name, what, final.Remote, final.Local, final.Done, roundTook(took), speed, map[string]string{"ok": ", MD5 verified", "mismatch": ", MD5 MISMATCH"}[final.Verified])
		} else {
			reason := final.Error
			if final.State == "cancelled" {
				if c := context.Cause(ctx); c != nil && c != context.Canceled {
					reason = c.Error()
				}
			}
			applog.Warnf("files %s: %s %s ↔ %s %s %s (%d of %d bytes in %v%s)", name, what, final.Remote, final.Local, final.State, reason, final.Done, final.Total, roundTook(took), speed)
		}
		a.emit("xfer:progress", final)
	}()
	return ev.ID
}

// TransferCancel stops a running transfer.
func (a *App) TransferCancel(xferID string) {
	a.filesMu.Lock()
	x := a.xfers[xferID]
	a.filesMu.Unlock()
	if x != nil {
		x.cancel(errCancelledByUser)
	}
}

func localMD5(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// verify compares the MD5 of a local and a device file.
func verify(ctx context.Context, f *filesConn, ev *TransferEvent) {
	ev.State = "verifying"
	local, err := localMD5(ev.Local)
	if err != nil {
		ev.Verified = "unavailable"
		return
	}
	ev.MD5 = local
	remote, err := f.link.MD5(ctx, ev.Remote)
	switch {
	case err != nil:
		ev.Verified = "unavailable"
	case remote == local:
		ev.Verified = "ok"
	default:
		ev.Verified = "mismatch"
	}
}

// FilesDownload copies a device file to this computer. size is the size
// shown in the listing (0: unknown); verify compares MD5 checksums after.
func (a *App) FilesDownload(id, remotePath, localPath string, size int64, verifyMD5 bool) (string, error) {
	f, err := a.filesGet(id)
	if err != nil {
		return "", err
	}
	if localPath == "" {
		return "", errors.New("no local file")
	}
	ev := TransferEvent{Name: baseName(remotePath), Dir: "down", Local: localPath, Remote: remotePath, Total: size}
	return a.startTransfer(f, ev, func(ctx context.Context, ev *TransferEvent, tick func(int64)) error {
		tmp := localPath + ".part"
		out, err := os.Create(tmp)
		if err != nil {
			return err
		}
		err = f.remote.Get(ctx, remotePath, out, tick)
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err == nil {
			_ = os.Remove(localPath)
			err = os.Rename(tmp, localPath)
		}
		if err != nil {
			_ = os.Remove(tmp)
			return err
		}
		if verifyMD5 && ctx.Err() == nil {
			verify(ctx, f, ev)
			if ev.Verified == "mismatch" {
				return errors.New("the MD5 checksum of the downloaded file differs from the one on the device")
			}
		}
		return nil
	}), nil
}

// FilesUpload copies a file from this computer to the device and, with
// verify, compares MD5 checksums after (verify /md5 on Cisco).
func (a *App) FilesUpload(id, localPath, remotePath string, verifyMD5 bool) (string, error) {
	f, err := a.filesGet(id)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(localPath)
	if err != nil {
		return "", err
	}
	if st.IsDir() {
		return "", fmt.Errorf("%s is a folder; send single files", localPath)
	}
	if xfer.IsDevicePath(remotePath) {
		if name := baseName(remotePath); name != xfer.DeviceName(name) {
			return "", fmt.Errorf("network devices cut file names at a space: send it as %s", xfer.DeviceName(name))
		}
	}
	ev := TransferEvent{Name: filepath.Base(localPath), Dir: "up", Local: localPath, Remote: remotePath, Total: st.Size()}
	if f.remote.Kind() == "scp" && st.Size() >= 64<<20 {
		ev.Hint = scpWindowHint
	}
	return a.startTransfer(f, ev, func(ctx context.Context, ev *TransferEvent, tick func(int64)) error {
		if free := f.remote.Free(ctx, remotePath); free >= 0 && free < st.Size() {
			return fmt.Errorf("not enough space on the device: %s free, the file needs %s", humanBytes(free), humanBytes(st.Size()))
		}
		in, err := os.Open(localPath)
		if err != nil {
			return err
		}
		defer in.Close()
		if err := f.remote.Put(ctx, remotePath, in, st.Size(), tick); err != nil {
			return err
		}
		if verifyMD5 && ctx.Err() == nil {
			verify(ctx, f, ev)
			if ev.Verified == "mismatch" {
				return errors.New("the MD5 checksum on the device differs from the file sent: delete it and send it again")
			}
		}
		// Without a checksum, check that the device kept the file under
		// this name and size (a device can save it under another name).
		if ev.Verified != "ok" && xfer.IsDevicePath(remotePath) && ctx.Err() == nil {
			if e, err := f.remote.Stat(ctx, remotePath); errors.Is(err, xfer.ErrNoSuchFile) {
				return fmt.Errorf("the device did not save the file as %s: check its file list", baseName(remotePath))
			} else if err == nil && e.Size >= 0 && e.Size != st.Size() {
				return fmt.Errorf("the device has %s with %s instead of %s: delete it and send it again", baseName(remotePath), humanBytes(e.Size), humanBytes(st.Size()))
			}
		}
		return nil
	}), nil
}

// FilesCommandToFile runs a command on the device (its own exec channel,
// no paging) and saves the output to a file: Get show tech, or any command.
func (a *App) FilesCommandToFile(id, command, localPath string) (string, error) {
	f, err := a.filesGet(id)
	if err != nil {
		return "", err
	}
	command = strings.TrimSpace(command)
	if command == "" || strings.ContainsAny(command, "\r\n") {
		return "", errors.New("enter one command")
	}
	ev := TransferEvent{Name: command, Dir: "cmd", Local: localPath, Remote: command}
	return a.startTransfer(f, ev, func(ctx context.Context, ev *TransferEvent, tick func(int64)) error {
		tmp := localPath + ".part"
		out, err := os.Create(tmp)
		if err != nil {
			return err
		}
		var n int64
		err = f.link.Do(ctx, func(c *ssh.Client) error {
			var err error
			n, err = xfer.ExecTo(ctx, c, command, out, tick, 10*time.Minute)
			return err
		})
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err == nil && n == 0 {
			err = errors.New("the device returned no output (does it allow commands over an SSH exec channel?)")
		}
		if err == nil && n < 512 {
			// A short reply may be the device refusing the command.
			if b, rerr := os.ReadFile(tmp); rerr == nil {
				if msg := xfer.DeviceError(string(b)); msg != "" {
					err = fmt.Errorf("the device answered: %s", msg)
				}
			}
		}
		if err == nil {
			_ = os.Remove(localPath)
			err = os.Rename(tmp, localPath)
		} else {
			_ = os.Remove(tmp)
		}
		return err
	}), nil
}

func baseName(p string) string {
	if i := strings.LastIndexAny(p, "/:\\"); i >= 0 {
		return p[i+1:]
	}
	return p
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// ---- this computer --------------------------------------------------------------

// LocalHome is where the local side of the Files tab starts: Downloads
// when it exists, else the home folder.
func (a *App) LocalHome() string {
	home, _ := os.UserHomeDir()
	if d := filepath.Join(home, "Downloads"); dirExists(d) {
		return d
	}
	return home
}

// FileServerFolder is the folder the file servers share unless another one
// is chosen: its own folder, so they never share Downloads or the home
// folder (with .ssh and YACM's data) to the network.
func (a *App) FileServerFolder() (string, error) {
	d := filepath.Join(a.LocalHome(), "YACM file servers")
	if err := os.MkdirAll(d, 0o700); err != nil {
		return "", err
	}
	return d, nil
}

// LocalRoots lists the drives (Windows) or / and the home folder.
func (a *App) LocalRoots() []xfer.Root {
	home, _ := os.UserHomeDir()
	roots := []xfer.Root{{Path: home, Size: -1, Free: -1}}
	if runtime.GOOS == "windows" {
		for c := 'A'; c <= 'Z'; c++ {
			d := string(c) + `:\`
			if dirExists(d) {
				roots = append(roots, xfer.Root{Path: d, Size: -1, Free: -1})
			}
		}
		return roots
	}
	return append(roots, xfer.Root{Path: "/", Size: -1, Free: -1})
}

// LocalList lists a folder on this computer.
func (a *App) LocalList(dir string) ([]xfer.Entry, error) {
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]xfer.Entry, 0, len(des))
	for _, d := range des {
		fi, err := d.Info()
		if err != nil {
			continue
		}
		p := filepath.Join(dir, d.Name())
		isDir := fi.IsDir()
		if fi.Mode()&os.ModeSymlink != 0 {
			if t, err := os.Stat(p); err == nil {
				isDir = t.IsDir()
				fi = t
			}
		}
		out = append(out, xfer.Entry{Name: d.Name(), Path: p, Size: fi.Size(), Dir: isDir, Mod: fi.ModTime()})
	}
	xfer.SortEntries(out)
	return out, nil
}

// LocalParent returns the folder above a local path ("" at the top).
func (a *App) LocalParent(p string) string {
	parent := filepath.Dir(filepath.Clean(p))
	if parent == filepath.Clean(p) {
		return ""
	}
	return parent
}

// LocalJoin joins a local folder and a name.
// The name is one path element: names come from a device, which could
// otherwise send "../../.config/autostart/x" and have a download land
// outside the chosen folder.
func (a *App) LocalJoin(dir, name string) (string, error) {
	if err := checkFileName(name); err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

var windowsDevice = regexp.MustCompile(`(?i)^(?:CON|PRN|AUX|NUL|COM[0-9¹²³]|LPT[0-9¹²³]|CONIN\$|CONOUT\$)(?:\..*)?$`)

// checkFileName refuses a name that is not a single file name on this
// computer.
func checkFileName(name string) error {
	bad := `/\` + "\x00"
	if runtime.GOOS == "windows" {
		bad += ":"
	}
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, bad) {
		return fmt.Errorf("refused the file name %q: it is not a plain file name", name)
	}
	// Windows: CON, NUL, COM1 … name devices, and trailing dots or spaces
	// are dropped, so such a name would not be the file it looks like.
	if runtime.GOOS == "windows" && (windowsDevice.MatchString(name) || strings.TrimRight(name, ". ") != name) {
		return fmt.Errorf("refused the file name %q: Windows reserves it", name)
	}
	return nil
}

// LocalExists reports whether a local file exists (to ask before replacing).
func (a *App) LocalExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// LocalMkdir creates a folder on this computer.
func (a *App) LocalMkdir(p string) error { return os.Mkdir(p, 0o755) }

// RevealFile shows a file in the system file manager.
func (a *App) RevealFile(p string) error {
	dir := p
	if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
		dir = filepath.Dir(p)
	}
	return openInFileManager(dir)
}

// ChooseSaveAs asks where to save a downloaded file, in dir with name.
func (a *App) ChooseSaveAs(title, dir, name string) (string, error) {
	if dir == "" || !dirExists(dir) {
		dir = a.LocalHome()
	}
	if checkFileName(name) != nil {
		name = baseName(strings.ReplaceAll(name, `\`, "/"))
		if checkFileName(name) != nil {
			name = ""
		}
	}
	return wruntime.SaveFileDialog(a.ctx, wruntime.SaveDialogOptions{Title: title, DefaultDirectory: dir, DefaultFilename: name, ShowHiddenFiles: true})
}

// ChooseFiles picks files to send.
func (a *App) ChooseFiles(title, dir string) ([]string, error) {
	if dir == "" || !dirExists(dir) {
		dir = a.LocalHome()
	}
	return wruntime.OpenMultipleFilesDialog(a.ctx, wruntime.OpenDialogOptions{Title: title, DefaultDirectory: dir, ShowHiddenFiles: true})
}

// ChooseFolder picks a folder, starting in dir.
func (a *App) ChooseFolder(title, dir string) (string, error) {
	if dir == "" || !dirExists(dir) {
		dir = a.LocalHome()
	}
	return wruntime.OpenDirectoryDialog(a.ctx, wruntime.OpenDialogOptions{Title: title, DefaultDirectory: dir, CanCreateDirectories: true})
}

// roundTook rounds how long a transfer took for the log: tenths of a second
// below ten seconds, whole seconds above.
func roundTook(d time.Duration) time.Duration {
	if d < 10*time.Second {
		return d.Round(100 * time.Millisecond)
	}
	return d.Round(time.Second)
}
