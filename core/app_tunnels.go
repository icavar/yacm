// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/icavar/yacm/internal/applog"
	"github.com/icavar/yacm/internal/sshconn"
	"github.com/icavar/yacm/internal/store"
	"github.com/icavar/yacm/internal/tunnel"
)

// ---- SSH tunnels (port forwarding) and the outbound proxy --------------------

// proxyDial dials the first hop through the session's outbound proxy.
func (a *App) proxyDial(eff *store.Effective) func(context.Context, string, time.Duration) (net.Conn, error) {
	if eff == nil || eff.Proxy == nil {
		return nil
	}
	p := tunnel.Proxy{Type: eff.Proxy.Type, Addr: net.JoinHostPort(eff.Proxy.Host, strconv.Itoa(eff.Proxy.Port)), User: eff.Proxy.User}
	if eff.Proxy.PasswordKey != "" {
		p.Password = a.secret(eff.Proxy.PasswordKey)
	}
	return p.Dial
}

func proxyText(eff *store.Effective) string {
	if eff == nil || eff.Proxy == nil {
		return ""
	}
	s := fmt.Sprintf("through the %s proxy %s:%d", strings.ToUpper(eff.Proxy.Type), eff.Proxy.Host, eff.Proxy.Port)
	if eff.Proxy.From != "" {
		s += " (from the group " + eff.Proxy.From + ")"
	}
	return s
}

// tabTunnels are the running tunnels of a terminal tab.
type tabTunnels struct {
	mu      sync.Mutex
	running map[string]*tunnel.Tunnel
	failed  map[string]string // tunnel ID -> why it did not start
	timer   *time.Timer
}

func (t *tab) tunnels() *tabTunnels {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.tun == nil {
		t.tun = &tabTunnels{running: map[string]*tunnel.Tunnel{}, failed: map[string]string{}}
	}
	return t.tun
}

func specOf(st store.Tunnel) tunnel.Spec {
	return tunnel.Spec{ID: st.ID, Type: st.Type, Name: st.Name, Bind: st.Bind, Port: st.Port, Host: st.Host, HostPort: st.HostPort}
}

// tabSSH returns the device connection of a connected SSH tab.
func (a *App) tabSSH(t *tab) (*sshconn.Conn, error) {
	t.mu.Lock()
	c, state := t.conn, t.state
	t.mu.Unlock()
	sc, ok := c.(*sshconn.Conn)
	if !ok || state != "connected" {
		return nil, errors.New("tunnels need a connected SSH session")
	}
	return sc, nil
}

// startTunnels starts the session's tunnels after connecting. Tunnels of
// team library sessions wait until they are started by hand.
func (a *App) startTunnels(t *tab, sc *sshconn.Conn) {
	ss := t.node.Session
	if ss == nil || len(ss.Tunnels) == 0 {
		return
	}
	for _, st := range ss.Tunnels {
		if st.Off {
			continue
		}
		if t.node.Library != "" {
			a.tabLog(t, "tunnel %s: not started (team library session; start it in Tunnels)", specOf(st).Describe())
			continue
		}
		if err := a.startTunnel(t, sc.SSHClient(), specOf(st)); err != nil {
			a.tabLog(t, "tunnel %s: %v", specOf(st).Describe(), err)
		}
	}
}

func (a *App) startTunnel(t *tab, client *ssh.Client, spec tunnel.Spec) error {
	tt := t.tunnels()
	tt.mu.Lock()
	if old := tt.running[spec.ID]; old != nil {
		tt.mu.Unlock()
		return fmt.Errorf("already running on %s", old.Addr())
	}
	tt.mu.Unlock()
	tn, err := tunnel.Start(client, spec, func() { a.tunnelsChanged(t) })
	tt.mu.Lock()
	if err != nil {
		tt.failed[spec.ID] = err.Error()
	} else {
		delete(tt.failed, spec.ID)
		tt.running[spec.ID] = tn
	}
	tt.mu.Unlock()
	if err == nil {
		a.tabLog(t, "tunnel %s: listening on %s", spec.Describe(), tn.Addr())
		applog.Infof("session %s: tunnel %s listening on %s", t.node.Name, spec.Describe(), tn.Addr())
	}
	a.tunnelsChanged(t)
	return err
}

func (a *App) stopTunnels(t *tab) {
	t.mu.Lock()
	tt := t.tun
	t.mu.Unlock()
	if tt == nil {
		return
	}
	tt.mu.Lock()
	run := tt.running
	tt.running = map[string]*tunnel.Tunnel{}
	tt.failed = map[string]string{}
	tt.mu.Unlock()
	for _, tn := range run {
		_ = tn.Close()
	}
	if len(run) > 0 {
		a.tunnelsChanged(t)
	}
}

// tunnelsChanged emits "tab:tunnels" (at most a few times a second).
func (a *App) tunnelsChanged(t *tab) {
	tt := t.tunnels()
	tt.mu.Lock()
	defer tt.mu.Unlock()
	if tt.timer != nil {
		return
	}
	tt.timer = time.AfterFunc(250*time.Millisecond, func() {
		tt.mu.Lock()
		tt.timer = nil
		tt.mu.Unlock()
		a.emit("tab:tunnels", map[string]any{"tabId": t.id, "tunnels": a.tunnelStatuses(t)})
	})
}

func (a *App) tunnelStatuses(t *tab) []tunnel.Status {
	tt := t.tunnels()
	tt.mu.Lock()
	defer tt.mu.Unlock()
	out := []tunnel.Status{}
	seen := map[string]bool{}
	for _, tn := range tt.running {
		out = append(out, tn.Status())
		seen[tn.Spec().ID] = true
	}
	if ss := t.node.Session; ss != nil {
		for _, st := range ss.Tunnels {
			if seen[st.ID] {
				continue
			}
			s := tunnel.Status{Spec: specOf(st), State: "off"}
			if why := tt.failed[st.ID]; why != "" {
				s.State, s.Error = "error", why
			}
			out = append(out, s)
			seen[st.ID] = true
		}
	}
	for id, why := range tt.failed {
		if !seen[id] {
			out = append(out, tunnel.Status{Spec: tunnel.Spec{ID: id}, State: "error", Error: why})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// TunnelStatuses lists a tab's tunnels: running, failed and switched off.
func (a *App) TunnelStatuses(tabID string) ([]tunnel.Status, error) {
	t, err := a.getTab(tabID)
	if err != nil {
		return nil, err
	}
	return a.tunnelStatuses(t), nil
}

// TunnelStart starts a tunnel on a connected tab: a saved one (by ID) or a
// new one for this connection only.
func (a *App) TunnelStart(tabID string, spec tunnel.Spec) (tunnel.Status, error) {
	t, err := a.getTab(tabID)
	if err != nil {
		return tunnel.Status{}, err
	}
	sc, err := a.tabSSH(t)
	if err != nil {
		return tunnel.Status{}, err
	}
	if spec.ID == "" {
		spec.ID = "adhoc-" + store.NewID()
	}
	if err := a.startTunnel(t, sc.SSHClient(), spec); err != nil {
		return tunnel.Status{}, err
	}
	tt := t.tunnels()
	tt.mu.Lock()
	defer tt.mu.Unlock()
	return tt.running[spec.ID].Status(), nil
}

// TunnelStop stops a running tunnel.
func (a *App) TunnelStop(tabID, id string) error {
	t, err := a.getTab(tabID)
	if err != nil {
		return err
	}
	tt := t.tunnels()
	tt.mu.Lock()
	tn := tt.running[id]
	delete(tt.running, id)
	delete(tt.failed, id)
	tt.mu.Unlock()
	if tn == nil {
		return errors.New("the tunnel is not running")
	}
	_ = tn.Close()
	a.tabLog(t, "tunnel %s: stopped", tn.Spec().Describe())
	a.tunnelsChanged(t)
	return nil
}

// FileServerTunnel lets a device behind a jump host reach a file server on
// this computer: a remote forward on the last jump host (or on the device
// itself without one) to the server's port.
type FileServerTunnel struct {
	Address   string `json:"address"` // what the device copies from
	Port      int    `json:"port"`
	Via       string `json:"via"`
	Reachable bool   `json:"reachable"`
	Note      string `json:"note,omitempty"`
}

// FileServerViaTunnel opens (or reuses) the tunnel for one server kind.
func (a *App) FileServerViaTunnel(tabID, kind string) (FileServerTunnel, error) {
	t, err := a.getTab(tabID)
	if err != nil {
		return FileServerTunnel{}, err
	}
	sc, err := a.tabSSH(t)
	if err != nil {
		return FileServerTunnel{}, errors.New("the reverse tunnel needs a connected SSH session (telnet devices behind a jump host: open an SSH session to the jump host)")
	}
	if kind == "tftp" {
		return FileServerTunnel{}, errors.New("TFTP uses UDP and cannot go through an SSH tunnel: use HTTP or SCP")
	}
	var port int
	for _, st := range a.fileServers().Statuses() {
		if st.Kind == kind && st.Running {
			port = st.Port
		}
	}
	if port == 0 {
		return FileServerTunnel{}, fmt.Errorf("start the %s server first", strings.ToUpper(kind))
	}
	client, bind, via, address := sc.JumpClient(), "0.0.0.0", "", ""
	t.mu.Lock()
	eff := t.eff
	t.mu.Unlock()
	if client != nil && eff != nil && len(eff.Hops) > 0 {
		h := eff.Hops[len(eff.Hops)-1]
		via, address = h.Host, h.Host
	} else {
		client, bind, via, address = sc.SSHClient(), "127.0.0.1", t.node.Name, "127.0.0.1"
	}
	id := "fileserver-" + kind
	tt := t.tunnels()
	tt.mu.Lock()
	old := tt.running[id]
	tt.mu.Unlock()
	if old == nil {
		spec := tunnel.Spec{ID: id, Type: tunnel.Remote, Name: strings.ToUpper(kind) + " file server", Bind: bind, Port: port, Host: "127.0.0.1", HostPort: port}
		if err := a.startTunnel(t, client, spec); err != nil {
			// The port may be taken there: let the server choose one.
			spec.Port = 0
			if err2 := a.startTunnel(t, client, spec); err2 != nil {
				return FileServerTunnel{}, err
			}
		}
		tt.mu.Lock()
		old = tt.running[id]
		tt.mu.Unlock()
	}
	_, ps, _ := net.SplitHostPort(old.Addr())
	rport, _ := strconv.Atoi(ps)
	if rport == 0 {
		rport = port
	}
	res := FileServerTunnel{Address: address, Port: rport, Via: via}
	// Does the jump host listen where the device can reach it? OpenSSH
	// binds only its loopback unless GatewayPorts allows more.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if c, err := client.DialContext(ctx, "tcp", net.JoinHostPort(address, strconv.Itoa(rport))); err == nil {
		c.Close()
		res.Reachable = true
	} else if bind != "127.0.0.1" {
		res.Note = fmt.Sprintf("%s listens only on its own loopback, so the device cannot reach the tunnel. Its SSH server needs \"GatewayPorts clientspecified\" (or yes) in sshd_config.", via)
	}
	return res, nil
}
