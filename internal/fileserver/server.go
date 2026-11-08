// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// Package fileserver runs small TFTP, HTTP and SCP servers on this computer,
// so network devices can copy files from and to a folder here (copy tftp:,
// copy http:, copy scp:). They run only while switched on.
package fileserver

import (
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/icavar/yacm/internal/xfer"
)

// Config is one server's settings.
type Config struct {
	Kind        string `json:"kind"` // tftp, http, scp
	Folder      string `json:"folder"`
	Bind        string `json:"bind"` // "" = all addresses
	Port        int    `json:"port"`
	AllowUpload bool   `json:"allowUpload"`
	Overwrite   bool   `json:"overwrite"`
	// Listing lets the HTTP server list folders (off: only named files are
	// served, so nobody on the network can browse the folder).
	Listing bool `json:"listing"`
	// SCP login (the password is generated when empty).
	User     string `json:"user"`
	Password string `json:"password"`
	// MaxUpload refuses larger uploads (0: no limit).
	MaxUpload int64 `json:"-"`
	// Allow lists the addresses or networks (10.1.0.0/16) that may use the
	// server; empty allows everyone.
	Allow []string `json:"allow"`
	// IdleStop switches the server off after this many minutes without a
	// transfer (0: never).
	IdleStop int `json:"idleStop"`

	allow []*net.IPNet
	touch func()

	events func(Event)
	mu     *sync.Mutex
	last   map[string]time.Time
}

// Event reports what a device did.
type Event struct {
	Kind   string `json:"kind"`
	Remote string `json:"remote"`
	File   string `json:"file"`
	Upload bool   `json:"upload"` // the device sent a file to us
	Done   int64  `json:"done"`
	Total  int64  `json:"total"`
	State  string `json:"state"` // start, progress, done, error, checked (the device read part of the file and stopped: a check before the copy, or a copy cancelled on the device; not a failure)
	Error  string `json:"error,omitempty"`
}

func (c *Config) emit(e Event) {
	if c.touch != nil {
		c.touch()
	}
	if c.events != nil {
		c.events(e)
	}
}

// emitProgress limits progress events to a few per second per transfer.
func (c *Config) emitProgress(e Event) {
	if c.events == nil {
		return
	}
	key := e.Kind + e.Remote + e.File
	c.mu.Lock()
	if time.Since(c.last[key]) < 250*time.Millisecond {
		c.mu.Unlock()
		return
	}
	c.last[key] = time.Now()
	c.mu.Unlock()
	c.events(e)
}

// allowed reports whether a device at ip may use the server.
func (c *Config) allowed(ip net.IP) bool {
	if len(c.allow) == 0 {
		return true
	}
	for _, n := range c.allow {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func parseAllow(list []string) ([]*net.IPNet, error) {
	var out []*net.IPNet
	for _, a := range list {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if !strings.Contains(a, "/") {
			if ip := net.ParseIP(a); ip != nil {
				bits := 32
				if ip.To4() == nil {
					bits = 128
				}
				a = fmt.Sprintf("%s/%d", ip, bits)
			}
		}
		_, n, err := net.ParseCIDR(a)
		if err != nil {
			return nil, fmt.Errorf("%q is not an IP address or network (like 10.1.0.0/16)", a)
		}
		out = append(out, n)
	}
	return out, nil
}

func resolve(folder, p string, write bool) (string, error) {
	if folder == "" {
		return "", errors.New("no folder")
	}
	name := ""
	if write {
		name = filepath.Base(filepath.Clean("/" + strings.ReplaceAll(p, `\`, "/")))
	}
	full, err := xfer.InRoot(folder, p, name)
	if err != nil {
		return "", err
	}
	if strings.HasSuffix(full, ".part") {
		return "", errors.New("not allowed")
	}
	if !write {
		full = xfer.SpaceAlias(full)
	}
	return full, nil
}

func displayName(folder, full string) string {
	rel, err := filepath.Rel(folder, full)
	if err != nil {
		return filepath.Base(full)
	}
	return filepath.ToSlash(rel)
}

func listenError(err error, port int) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "permission denied") || strings.Contains(msg, "access permissions"):
		if port < 1024 && runtime.GOOS == "linux" {
			return fmt.Errorf("port %d needs administrator rights on this system: allow it once (YACM asks for the password), or choose a port above 1024 (for example %d) and give it in the device command", port, suggestPort(port))
		}
		if port < 1024 && runtime.GOOS != "windows" {
			return fmt.Errorf("port %d needs administrator rights on this system: choose a port above 1024 (for example %d) and give it in the device command", port, suggestPort(port))
		}
		return fmt.Errorf("port %d is not allowed: %v", port, err)
	case strings.Contains(msg, "address already in use") || strings.Contains(msg, "Only one usage"):
		return fmt.Errorf("port %d is already used by another program (another TFTP or web server?)", port)
	}
	return err
}

// Status is a running server.
type Status struct {
	Kind     string   `json:"kind"`
	Running  bool     `json:"running"`
	Port     int      `json:"port"`
	Bind     string   `json:"bind"`
	Folder   string   `json:"folder"`
	Upload   bool     `json:"allowUpload"`
	User     string   `json:"user,omitempty"`
	Password string   `json:"password,omitempty"`
	HostKey  string   `json:"hostKey,omitempty"` // SCP: SHA256 fingerprint
	Allow    []string `json:"allow,omitempty"`
	IdleStop int      `json:"idleStop,omitempty"`
	StopsAt  string   `json:"stopsAt,omitempty"` // RFC 3339, with IdleStop
}

type server interface {
	Close() error
	Port() int
}

// Manager runs at most one server of each kind.
type Manager struct {
	mu      sync.Mutex
	servers map[string]server
	cfgs    map[string]Config
	lastUse map[string]time.Time
	timers  map[string]*time.Timer
	// Stopped is told when a server switches itself off (idle).
	Stopped func(kind string)
	keyFile string // SCP host keys
	events  func(Event)
}

// NewManager keeps the SCP host key in keyFile and reports events.
func NewManager(keyFile string, events func(Event)) *Manager {
	return &Manager{servers: map[string]server{}, cfgs: map[string]Config{}, lastUse: map[string]time.Time{}, timers: map[string]*time.Timer{}, keyFile: keyFile, events: events}
}

// Start starts (or restarts) the server of cfg.Kind.
func (m *Manager) Start(cfg Config) (Status, error) {
	if cfg.Folder == "" {
		return Status{}, errors.New("choose the folder to share")
	}
	if fi, err := osStat(cfg.Folder); err != nil || !fi.IsDir() {
		return Status{}, fmt.Errorf("the folder %s does not exist", cfg.Folder)
	}
	if cfg.Bind != "" && net.ParseIP(cfg.Bind) == nil {
		return Status{}, fmt.Errorf("%s is not an IP address of this computer", cfg.Bind)
	}
	allow, err := parseAllow(cfg.Allow)
	if err != nil {
		return Status{}, err
	}
	cfg.allow = allow
	m.Stop(cfg.Kind)
	cfg.events = m.events
	kind := cfg.Kind
	cfg.touch = func() { m.touch(kind) }
	cfg.mu = &sync.Mutex{}
	cfg.last = map[string]time.Time{}
	var s server
	switch cfg.Kind {
	case "tftp":
		if cfg.Port == 0 {
			cfg.Port = 69
		}
		s, err = startTFTP(cfg)
	case "http":
		if cfg.Port == 0 {
			cfg.Port = 8080
		}
		s, err = startHTTP(cfg)
	case "scp":
		if cfg.Port == 0 {
			cfg.Port = 2022
		}
		if cfg.User == "" {
			cfg.User = "yacm"
		}
		if cfg.Password == "" {
			cfg.Password = randomPassword()
		}
		s, err = startSCP(cfg, m.keyFile)
	default:
		return Status{}, fmt.Errorf("unknown server %q", cfg.Kind)
	}
	if err != nil {
		return Status{}, err
	}
	cfg.Port = s.Port()
	m.mu.Lock()
	m.servers[cfg.Kind] = s
	m.cfgs[cfg.Kind] = cfg
	m.lastUse[cfg.Kind] = time.Now()
	m.mu.Unlock()
	m.armIdle(cfg.Kind)
	return m.status(cfg.Kind), nil
}

// touch notes a transfer: the idle timer starts over.
func (m *Manager) touch(kind string) {
	m.mu.Lock()
	m.lastUse[kind] = time.Now()
	m.mu.Unlock()
	m.armIdle(kind)
}

func (m *Manager) armIdle(kind string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cfg, ok := m.cfgs[kind]
	if t := m.timers[kind]; t != nil {
		t.Stop()
		delete(m.timers, kind)
	}
	if !ok || cfg.IdleStop <= 0 {
		return
	}
	s := m.servers[kind]
	m.timers[kind] = time.AfterFunc(time.Duration(cfg.IdleStop)*time.Minute, func() {
		m.mu.Lock()
		same := m.servers[kind] == s
		m.mu.Unlock()
		if !same {
			return
		}
		m.Stop(kind)
		if m.Stopped != nil {
			m.Stopped(kind)
		}
	})
}

// Stop stops the server of kind.
func (m *Manager) Stop(kind string) {
	m.mu.Lock()
	s := m.servers[kind]
	delete(m.servers, kind)
	delete(m.cfgs, kind)
	if t := m.timers[kind]; t != nil {
		t.Stop()
		delete(m.timers, kind)
	}
	m.mu.Unlock()
	if s != nil {
		_ = s.Close()
	}
}

// StopAll stops every server (YACM closes).
func (m *Manager) StopAll() {
	for _, k := range []string{"tftp", "http", "scp"} {
		m.Stop(k)
	}
}

func (m *Manager) status(kind string) Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, cfg := m.servers[kind], m.cfgs[kind]
	if s == nil {
		return Status{Kind: kind}
	}
	st := Status{Kind: kind, Running: true, Port: s.Port(), Bind: cfg.Bind, Folder: cfg.Folder, Upload: cfg.AllowUpload, Allow: cfg.Allow, IdleStop: cfg.IdleStop}
	if cfg.IdleStop > 0 {
		st.StopsAt = m.lastUse[kind].Add(time.Duration(cfg.IdleStop) * time.Minute).Format(time.RFC3339)
	}
	if sc, ok := s.(*SCP); ok {
		st.User, st.Password, st.HostKey = cfg.User, cfg.Password, sc.Fingerprint()
	}
	return st
}

// Statuses lists the three servers.
func (m *Manager) Statuses() []Status {
	return []Status{m.status("tftp"), m.status("http"), m.status("scp")}
}

// LocalAddr is an IP address of this computer.
type LocalAddr struct {
	IP        string `json:"ip"`
	Interface string `json:"interface"`
}

// LocalAddresses lists the IPv4 (then IPv6) addresses of interfaces that
// are up, without loopback and link-local ones.
func LocalAddresses() []LocalAddr {
	var v4, v6 []LocalAddr
	ifs, _ := net.Interfaces()
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.IsLinkLocalUnicast() || ipn.IP.IsLoopback() {
				continue
			}
			la := LocalAddr{IP: ipn.IP.String(), Interface: ifc.Name}
			if ipn.IP.To4() != nil {
				v4 = append(v4, la)
			} else {
				v6 = append(v6, la)
			}
		}
	}
	return append(v4, v6...)
}

// AddressToward returns the address of this computer that packets to host
// leave from (the address a device on that path should copy from).
func AddressToward(host string) string {
	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		return ""
	}
	c, err := net.Dial("udp", net.JoinHostPort(ips[0].String(), "9"))
	if err != nil {
		return ""
	}
	defer c.Close()
	ip := c.LocalAddr().(*net.UDPAddr).IP
	if ip.IsLoopback() && !ips[0].IsLoopback() {
		return ""
	}
	return ip.String()
}

func suggestPort(port int) int {
	switch port {
	case 69:
		return 6969
	case 80:
		return 8080
	case 22:
		return 2022
	}
	return port + 8000
}
