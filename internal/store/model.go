// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// Package store holds the session tree (groups and sessions) and persists it
// as plain JSON. Secrets never live here: they are kept in the encrypted
// vault and referenced by key.
package store

import (
	"strconv"
	"time"

	"github.com/icavar/yacm/internal/jsonx"
)

// Protocol is the transport a session uses.
type Protocol string

const (
	ProtoSSH    Protocol = "ssh"
	ProtoTelnet Protocol = "telnet"
	ProtoSerial Protocol = "serial"
)

// AuthMethod is how an SSH session authenticates.
type AuthMethod string

const (
	AuthPassword  AuthMethod = "password"  // password / keyboard-interactive
	AuthPublicKey AuthMethod = "publickey" // private key file (+ optional OpenSSH certificate)
	AuthAgent     AuthMethod = "agent"     // ssh-agent / Windows OpenSSH agent
	AuthAsk       AuthMethod = "ask"       // nothing stored, prompt at connect
)

// AlgoProfile selects which SSH algorithms are offered.
type AlgoProfile string

const (
	// AlgoSecure offers only algorithms without known weaknesses
	// (incl. post-quantum mlkem768x25519-sha256). Default for new sessions.
	AlgoSecure AlgoProfile = "secure"
	// AlgoCompatible adds SHA-1 based kex/host keys (ssh-rsa, dh-group14-sha1).
	AlgoCompatible AlgoProfile = "compatible"
	// AlgoLegacy also enables CBC/3DES ciphers and dh-group1 for very old gear.
	AlgoLegacy AlgoProfile = "legacy"
)

// Node is one entry in the tree: either a group or a session.
type Node struct {
	ID          string `json:"id"`
	ParentID    string `json:"parentId"` // "" = root
	IsGroup     bool   `json:"isGroup"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Order       int    `json:"order"`
	Expanded    bool   `json:"expanded,omitempty"`
	// Color marks the tab and tree row ("red", "green", ...). Empty inherits
	// the nearest group colour; "none" stops inheritance.
	Color    string         `json:"color,omitempty"`
	Session  *Session       `json:"session,omitempty"`
	Defaults *GroupDefaults `json:"defaults,omitempty"` // groups only
	Updated  time.Time      `json:"updated"`
	// Library is set (at run time only) on nodes that come from a team
	// library; such nodes are read-only and never saved with your sessions.
	Library string `json:"library,omitempty"`

	// Extra keeps members written by a newer YACM (see internal/jsonx).
	Extra jsonx.Unknown `json:"-"`
}

// Inherit is the value of a session field that takes the setting from the
// nearest group that defines it.
const Inherit = "inherit"

// GroupDefaults are settings that sessions in a group (and its subgroups)
// can inherit. Empty fields define nothing.
type GroupDefaults struct {
	CredentialID string      `json:"credentialId,omitempty"`
	JumpVia      string      `json:"jumpVia,omitempty"` // ID of an SSH session used as jump host
	Algorithms   AlgoProfile `json:"algorithms,omitempty"`
	AntiIdle     int         `json:"antiIdle,omitempty"`     // seconds; 0 = not set here, -1 = off
	AntiIdleText string      `json:"antiIdleText,omitempty"` // text sent by anti-idle; empty = not set here
	// Terminal colours for the group's sessions (see Session.TermText).
	TermText       string `json:"termText,omitempty"`
	TermBackground string `json:"termBackground,omitempty"`
	// LogName is a log file name template for the group's sessions (used
	// when Settings allow groups their own template).
	LogName string `json:"logName,omitempty"`
	// AskTicket: ask for a change/ticket number when its sessions connect
	// with logging on (when Settings ask "only in groups that want it").
	AskTicket bool `json:"askTicket,omitempty"`
	// Proxy reaches the first hop of the group's SSH and telnet sessions
	// (the device or its first SSH jump host) through an outbound SOCKS5 or
	// HTTP proxy.
	Proxy *Proxy `json:"proxy,omitempty"`

	// Extra keeps members written by a newer YACM (see internal/jsonx).
	Extra jsonx.Unknown `json:"-"`
}

// IsEmpty reports whether the defaults define nothing.
func (d *GroupDefaults) IsEmpty() bool {
	return d == nil || (d.CredentialID == "" && d.JumpVia == "" && d.Algorithms == "" && d.AntiIdle == 0 && d.AntiIdleText == "" &&
		d.TermText == "" && d.TermBackground == "" && d.LogName == "" && !d.AskTicket && d.Proxy == nil)
}

// DefaultAntiIdleText is sent when nothing else is set: a comment line on
// Cisco IOS, IOS XE, NX-OS and ASA, so it changes nothing on the device.
const DefaultAntiIdleText = "!"

// Credential is a reusable login: sessions refer to it by ID, so changing
// the password here changes it for all of them. Secrets live in the vault
// under SecretCredPassword / SecretCredPassphrase.
type Credential struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	User        string     `json:"user"`
	Auth        AuthMethod `json:"auth"` // for SSH; telnet and serial use the password through <PASS>
	KeyPath     string     `json:"keyPath,omitempty"`
	CertPath    string     `json:"certPath,omitempty"`
	Updated     time.Time  `json:"updated"`

	// Extra keeps members written by a newer YACM (see internal/jsonx).
	Extra jsonx.Unknown `json:"-"`
}

// HighlightRule colours matching text in terminal output.
type HighlightRule struct {
	ID         string `json:"id"`
	Enabled    bool   `json:"enabled"`
	Pattern    string `json:"pattern"` // JavaScript-compatible regular expression
	Color      string `json:"color"`   // red, green, yellow, blue, magenta, cyan (bright- prefix allowed)
	Bold       bool   `json:"bold"`
	Background bool   `json:"background"` // colour the background instead of the text
	IgnoreCase bool   `json:"ignoreCase"`
	// Alert: a match in a session you are not looking at marks its tab and
	// shows a message (Settings > Highlighting).
	Alert bool `json:"alert,omitempty"`

	// Extra keeps members written by a newer YACM (see internal/jsonx).
	Extra jsonx.Unknown `json:"-"`
}

// Snippet is a command in the global command library. {name} placeholders
// are asked for when it is sent.
type Snippet struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Folder string `json:"folder,omitempty"`
	Text   string `json:"text"`
	Return bool   `json:"return"`

	// Extra keeps members written by a newer YACM (see internal/jsonx).
	Extra jsonx.Unknown `json:"-"`
}

// JumpHost is one hop of a ProxyJump chain.
type JumpHost struct {
	Host         string     `json:"host"`
	Port         int        `json:"port"`
	User         string     `json:"user"`
	Auth         AuthMethod `json:"auth"`
	KeyPath      string     `json:"keyPath,omitempty"`
	CredentialID string     `json:"credentialId,omitempty"` // login from a credential profile

	// Extra keeps members written by a newer YACM (see internal/jsonx).
	Extra jsonx.Unknown `json:"-"`
}

// ExpectRule waits for Expect (a regular expression) in the output and then
// sends Send. Rules run in order once after the session is up.
type ExpectRule struct {
	ID      string `json:"id"`
	Active  bool   `json:"active"`
	Expect  string `json:"expect"`
	Send    string `json:"send,omitempty"` // empty when Hidden: value is in the vault
	Hidden  bool   `json:"hidden"`
	Return  bool   `json:"return"`  // append Enter after Send
	Timeout int    `json:"timeout"` // seconds, 0 = session default
	OnFail  string `json:"onFail"`  // "continue" or "stop"

	// Extra keeps members written by a newer YACM (see internal/jsonx).
	Extra jsonx.Unknown `json:"-"`
}

// LocalCommand runs on this computer before connecting or after disconnect.
type LocalCommand struct {
	Command string `json:"command"`
	Confirm bool   `json:"confirm"` // ask before running

	// Extra keeps members written by a newer YACM (see internal/jsonx).
	Extra jsonx.Unknown `json:"-"`
}

// Macro is a saved snippet that can be sent to the session from the UI.
type Macro struct {
	Name   string `json:"name"`
	Text   string `json:"text"`
	Return bool   `json:"return"`

	// Extra keeps members written by a newer YACM (see internal/jsonx).
	Extra jsonx.Unknown `json:"-"`
}

// Tunnel is an SSH port forward of a session (see internal/tunnel): Local
// (-L) listens here, Remote (-R) on the SSH server, Dynamic (-D) is a
// SOCKS / HTTP proxy here. It starts when the session connects unless Off.
type Tunnel struct {
	ID       string `json:"id"`
	Type     string `json:"type"` // local, remote, dynamic
	Name     string `json:"name,omitempty"`
	Bind     string `json:"bind,omitempty"` // listen address; "" = 127.0.0.1
	Port     int    `json:"port"`
	Host     string `json:"host,omitempty"`
	HostPort int    `json:"hostPort,omitempty"`
	Off      bool   `json:"off,omitempty"`

	// Extra keeps members written by a newer YACM (see internal/jsonx).
	Extra jsonx.Unknown `json:"-"`
}

// Proxy is an outbound proxy for the first hop of an SSH or telnet session
// (the device or its first SSH jump host). Type "none" turns off
// a proxy the group sets. The login comes from a credential profile.
type Proxy struct {
	Type         string `json:"type"` // socks5, http, none
	Host         string `json:"host,omitempty"`
	Port         int    `json:"port,omitempty"`
	CredentialID string `json:"credentialId,omitempty"`

	// Extra keeps members written by a newer YACM (see internal/jsonx).
	Extra jsonx.Unknown `json:"-"`
}

// Serial holds serial-line parameters.
type Serial struct {
	Device   string `json:"device"` // COM3, /dev/ttyUSB0, /dev/cu.usbserial-...
	Baud     int    `json:"baud"`
	DataBits int    `json:"dataBits"`
	Parity   string `json:"parity"`   // none, even, odd, mark, space
	StopBits string `json:"stopBits"` // 1, 1.5, 2

	// Extra keeps members written by a newer YACM (see internal/jsonx).
	Extra jsonx.Unknown `json:"-"`
}

// Session is the connection definition of a non-group node.
type Session struct {
	Protocol Protocol `json:"protocol"`
	Host     string   `json:"host,omitempty"`
	Port     int      `json:"port,omitempty"`
	User     string   `json:"user,omitempty"`

	// CredentialID uses a credential profile instead of User/Auth/KeyPath
	// and the session's own secrets. "inherit" takes it from the group.
	CredentialID string `json:"credentialId,omitempty"`

	// SSH
	Auth           AuthMethod  `json:"auth,omitempty"`
	KeyPath        string      `json:"keyPath,omitempty"`
	CertPath       string      `json:"certPath,omitempty"` // empty = auto (<key>-cert.pub)
	Jump           []JumpHost  `json:"jump,omitempty"`
	JumpVia        string      `json:"jumpVia,omitempty"`    // saved SSH session used as jump host (replaces Jump); "inherit"
	Algorithms     AlgoProfile `json:"algorithms,omitempty"` // or "inherit"
	KeepAlive      int         `json:"keepAlive,omitempty"`  // seconds, 0 = off
	AgentForward   bool        `json:"agentForward,omitempty"`
	ConnectTimeout int         `json:"connectTimeout,omitempty"` // seconds

	// Anti-idle types AntiIdleText after AntiIdle seconds without typing, only
	// at a CLI prompt; no Enter unless the text contains {enter}.
	// 0 = from the group, -1 = off.
	AntiIdle     int    `json:"antiIdle,omitempty"`
	AntiIdleText string `json:"antiIdleText,omitempty"` // empty = from the group, else "!"
	// KeepName: never offer to rename the session after the device hostname.
	KeepName bool `json:"keepName,omitempty"`

	Serial *Serial `json:"serial,omitempty"`

	Expect        []ExpectRule   `json:"expect,omitempty"`
	ExpectTimeout int            `json:"expectTimeout,omitempty"` // default per rule, seconds
	InitialReturn bool           `json:"initialReturn,omitempty"` // press Enter once right after connecting
	Before        []LocalCommand `json:"before,omitempty"`
	After         []LocalCommand `json:"after,omitempty"`
	Clusters      []string       `json:"clusters,omitempty"`
	Macros        []Macro        `json:"macros,omitempty"`
	// SSH port forwards and the outbound proxy (nil: the group's).
	Tunnels []Tunnel `json:"tunnels,omitempty"`
	Proxy   *Proxy   `json:"proxy,omitempty"`

	LogToFile     bool   `json:"logToFile,omitempty"`
	LogDir        string `json:"logDir,omitempty"`
	LogTimestamps bool   `json:"logTimestamps,omitempty"` // date and time at the start of every log line
	LogName       string `json:"logName,omitempty"`       // own log file name template (see Settings.LogNameOwn)

	FontSize    int  `json:"fontSize,omitempty"`
	Scrollback  int  `json:"scrollback,omitempty"`
	PasteDelay  int  `json:"pasteDelay,omitempty"` // ms between pasted lines; 0 = app default, -1 = none
	NoHighlight bool `json:"noHighlight,omitempty"`
	NoAlerts    bool `json:"noAlerts,omitempty"` // no output alerts from this session
	// TermText and TermBackground are the terminal's base text and
	// background colours: "#rrggbb", "theme" for the colours of the theme,
	// or empty to take them from the group or Settings. Colours the device
	// sends and keyword highlighting are not changed.
	TermText       string `json:"termText,omitempty"`
	TermBackground string `json:"termBackground,omitempty"`

	// Extra keeps members written by a newer YACM (see internal/jsonx).
	Extra jsonx.Unknown `json:"-"`
}

// TermColorTheme is the terminal colour value that means "the theme's own".
const TermColorTheme = "theme"

// ValidTermColor reports whether c is a terminal colour value: empty,
// "theme" or "#rrggbb".
func ValidTermColor(c string) bool {
	if c == "" || c == TermColorTheme {
		return true
	}
	if len(c) != 7 || c[0] != '#' {
		return false
	}
	for _, r := range c[1:] {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}

// Settings are application-wide preferences.
type Settings struct {
	FontFamily string `json:"fontFamily"`
	FontSize   int    `json:"fontSize"`
	// FontWeight of the terminal: "" (regular), "light" or "medium"; bold
	// text from the device is three steps heavier.
	FontWeight string `json:"fontWeight,omitempty"`
	// Default terminal colours for sessions and groups that set none
	// ("" or "theme" = the theme's colours, else "#rrggbb").
	TermText       string `json:"termText,omitempty"`
	TermBackground string `json:"termBackground,omitempty"`
	Scrollback     int    `json:"scrollback"`
	Theme          string `json:"theme"` // dark, light
	CopyOnSelect   bool   `json:"copyOnSelect"`
	RightClick     string `json:"rightClick,omitempty"` // "" or "menu": context menu; "paste": paste at once (PuTTY)
	// PageKeys: "" or "scroll": Page Up/Down scroll the terminal unless a
	// full-screen program (less, vi, top) runs; "device": always sent to it.
	PageKeys string `json:"pageKeys,omitempty"`
	// HostnameRename: when the device prompt shows a hostname different
	// from the session name: "ask" (default), "always" rename, or "off".
	HostnameRename string `json:"hostnameRename,omitempty"`
	// LastSeenVersion is the YACM version whose "What's new" was shown.
	LastSeenVersion string `json:"lastSeenVersion,omitempty"`
	// UpdateCheck: "" not asked yet, "daily" ask GitHub once a day whether
	// a newer YACM is out, "off" never.
	UpdateCheck string `json:"updateCheck,omitempty"`
	// ReopenSessions opens the sessions that were open when YACM closed.
	ReopenSessions     bool   `json:"reopenSessions,omitempty"`
	UseSystemKnownHost bool   `json:"useSystemKnownHosts"` // also trust ~/.ssh/known_hosts
	DefaultLogDir      string `json:"defaultLogDir"`
	// Log file names: a template instead of <name>_<date-time>.log, own
	// templates for groups and sessions, and a change/ticket number asked
	// for when connecting: "" = never, "all" = every session with logging,
	// "groups" = sessions in groups that turn it on.
	// After a reload/reboot command drops the session, wait for the device
	// and reconnect (on unless NoReloadReconnect); stop after
	// ReloadWaitMinutes (0 = 30).
	NoReloadReconnect bool `json:"noReloadReconnect,omitempty"`
	// Output alerts (Alert on highlight rules): on unless NoAlerts;
	// AlertSound plays a short sound. AlertRulesAdded records that the
	// standard alert rules were added to an older rule list once;
	// LinkAlertQuiet that the former link-down alert rule (al-link) was
	// turned into a plain colour rule once.
	NoAlerts          bool   `json:"noAlerts,omitempty"`
	AlertSound        bool   `json:"alertSound,omitempty"`
	AlertRulesAdded   bool   `json:"alertRulesAdded,omitempty"`
	LinkAlertQuiet    bool   `json:"linkAlertQuiet,omitempty"`
	ReloadWaitMinutes int    `json:"reloadWaitMinutes,omitempty"`
	LogNameTemplateOn bool   `json:"logNameTemplateOn,omitempty"`
	LogNameTemplate   string `json:"logNameTemplate,omitempty"`
	LogNameOwn        bool   `json:"logNameOwn,omitempty"`
	AskTicket         string `json:"askTicket,omitempty"`
	// LogNewOff: new and imported sessions do not get "log to file" (by
	// default they do).
	LogNewOff bool `json:"logNewOff,omitempty"`
	// LogSecretsInClear turns off hiding passwords and keys in session logs.
	LogSecretsInClear bool `json:"logSecretsInClear,omitempty"`
	AutoLockMinutes   int  `json:"autoLockMinutes"`

	SidebarWidth   int              `json:"sidebarWidth,omitempty"`
	SidebarHidden  bool             `json:"sidebarHidden,omitempty"` // session list folded away (the Sessions icon)
	PasteDelay     int              `json:"pasteDelay"`              // ms between lines of a multi-line paste
	Highlight      bool             `json:"highlight"`
	HighlightRules []HighlightRule  `json:"highlightRules"`
	Snippets       []Snippet        `json:"snippets"`
	NetBoxURL      string           `json:"netboxUrl,omitempty"`
	TeamLibraries  []TeamLibraryRef `json:"teamLibraries,omitempty"`

	// Extra keeps members written by a newer YACM (see internal/jsonx).
	Extra jsonx.Unknown `json:"-"`
}

// TeamLibraryRef is a team library this user follows (Settings > Team
// libraries).
type TeamLibraryRef struct {
	ID     string            `json:"id"`
	Path   string            `json:"path"`
	Logins map[string]string `json:"logins,omitempty"` // library login ID -> your credential profile ID
	// Source is your group the library is published from while you edit
	// it; Revision is the library revision that copy started from.
	Source   string `json:"source,omitempty"`
	Revision int    `json:"revision,omitempty"`

	// Extra keeps members written by a newer YACM (see internal/jsonx).
	Extra jsonx.Unknown `json:"-"`
}

// DefaultSettings returns sane defaults.
func DefaultSettings() Settings {
	return Settings{
		FontFamily:         "Cascadia Mono, JetBrains Mono, Consolas, Menlo, monospace",
		FontSize:           14,
		Scrollback:         10000,
		Theme:              "dark",
		CopyOnSelect:       true,
		RightClick:         "menu",
		HostnameRename:     "ask",
		UseSystemKnownHost: true,
		AutoLockMinutes:    15, // secure default (0.9.0); existing settings keep theirs
		Highlight:          true,
		HighlightRules:     DefaultHighlightRules(),
		AlertRulesAdded:    true,
		LinkAlertQuiet:     true,
		Snippets:           DefaultSnippets(),
	}
}

// DefaultHighlightRules are the keyword colours a new installation starts with.
func DefaultHighlightRules() []HighlightRule {
	r := func(id, pat, color string, bold, bg, ic bool) HighlightRule {
		return HighlightRule{ID: id, Enabled: true, Pattern: pat, Color: color, Bold: bold, Background: bg, IgnoreCase: ic}
	}
	return append(DefaultAlertRules(), []HighlightRule{
		r("hl-bad", `\b(?:down|err-disabled|errdisable|failed|failure|error|errors|denied|invalid|unreachable|timed? ?out|critical|blocking|notconnect|disabled)\b`, "red", true, false, true),
		r("hl-good", `\b(?:up|connected|established|full|forwarding|active|ok|success(?:ful)?)\b`, "green", false, false, true),
		r("hl-syslog", `%[A-Z0-9_]+-[0-7]-[A-Z0-9_]+`, "yellow", true, false, false),
		r("hl-ipv4", `\b(?:25[0-5]|2[0-4]\d|1?\d?\d)(?:\.(?:25[0-5]|2[0-4]\d|1?\d?\d)){3}(?:/\d{1,2})?\b`, "cyan", false, false, false),
		r("hl-mac", `\b(?:[0-9a-f]{4}\.[0-9a-f]{4}\.[0-9a-f]{4}|(?:[0-9a-f]{2}[:-]){5}[0-9a-f]{2})\b`, "magenta", false, false, true),
		r("hl-intf", `\b(?:(?:Gi|Te|Tw|Fa|Fo|Hu|Twe|Fi|Eth?|Po|Vl|Lo|Tu|Se|Mg)[a-zA-Z-]*\d+(?:/\d+)*(?:\.\d+)?|(?:ge|xe|et|ae)-\d+(?:/\d+)*(?:\.\d+)?)\b`, "blue", false, false, false),
	}...)
}

// DefaultAlertRules are syslog messages worth an alert from a session in the
// background: neighbours going down, err-disabled ports, reloads and duplex
// mismatches. They come first, so their colour wins. Links going down are
// left out: a reload of one device takes down links in every session to its
// neighbours, and the alerts would pile up (hl-bad still colours them).
func DefaultAlertRules() []HighlightRule {
	a := func(id, pat string) HighlightRule {
		return HighlightRule{ID: id, Enabled: true, Pattern: pat, Color: "bright-red", Bold: true, Alert: true}
	}
	return []HighlightRule{
		a("al-neighbor", `%(?:BGP-5-ADJCHANGE:.*\bDown\b|OSPF(?:V3)?-5-ADJCHG:.*\bto DOWN\b|DUAL-5-NBRCHANGE:.*\bis down\b|CLNS-5-ADJCHANGE:.*\bDown\b|BFD-6-BFD_SESS_DESTROYED)|RPD_BGP_NEIGHBOR_STATE_CHANGED:.*\bIdle\b`),
		a("al-errdisable", `%PM-\d-ERR_DISABLE:|%ETHPORT-\d-IF_DOWN_ERROR_DISABLED|err-disabled state`),
		a("al-reload", `%SYS-5-RELOAD|%CDP-4-DUPLEX_MISMATCH|%SYS-2-MALLOCFAIL|%PLATFORM-\d-ELEMENT_CRITICAL`),
	}
}

// oldLinkAlert is the link-down alert rule installations before 0.9.0-rc.2
// started with; quietLinkAlert takes its Alert off once.
const oldLinkAlertID, oldLinkAlertPattern = "al-link", `%(?:LINK|LINEPROTO)-\d-UPDOWN:.*changed state to down|SNMP_TRAP_LINK_DOWN`

// quietLinkAlert keeps the old link-down rule as a colour rule without an
// alert, unless the user changed its pattern.
func quietLinkAlert(rules []HighlightRule) []HighlightRule {
	for i := range rules {
		if rules[i].ID == oldLinkAlertID && rules[i].Pattern == oldLinkAlertPattern {
			rules[i].Alert = false
		}
	}
	return rules
}

// DefaultSnippets seed the command library with a few examples.
func DefaultSnippets() []Snippet {
	return []Snippet{
		{ID: "sn-int-brief", Name: "Interfaces, brief", Folder: "Cisco IOS", Text: "show ip interface brief", Return: true},
		{ID: "sn-int", Name: "One interface", Folder: "Cisco IOS", Text: "show interfaces {interface}", Return: true},
		{ID: "sn-run-int", Name: "Interface configuration", Folder: "Cisco IOS", Text: "show running-config interface {interface}", Return: true},
		{ID: "sn-log", Name: "Search the log", Folder: "Cisco IOS", Text: "show logging | include {text}", Return: true},
		{ID: "sn-bgp", Name: "BGP neighbours", Folder: "Cisco IOS", Text: "show ip bgp summary", Return: true},
	}
}

// Secret keys used in the vault.
func SecretPassword(nodeID string) string   { return "node/" + nodeID + "/password" }
func SecretPassphrase(nodeID string) string { return "node/" + nodeID + "/passphrase" }
func SecretJumpPassword(nodeID string, hop int) string {
	return "node/" + nodeID + "/jump/" + itoa(hop) + "/password"
}
func SecretExpect(nodeID, ruleID string) string {
	return "node/" + nodeID + "/expect/" + ruleID
}

// Vault keys of a credential profile.
func SecretCredPassword(credID string) string   { return "cred/" + credID + "/password" }
func SecretCredPassphrase(credID string) string { return "cred/" + credID + "/passphrase" }
func SecretCredPrefix(credID string) string     { return "cred/" + credID + "/" }

// Colors are the colour names a node can carry.
var Colors = []string{"red", "orange", "yellow", "green", "teal", "blue", "purple", "gray"}

// ValidColor reports whether c is empty, "none" or a known colour.
func ValidColor(c string) bool {
	if c == "" || c == "none" {
		return true
	}
	for _, k := range Colors {
		if c == k {
			return true
		}
	}
	return false
}

// SecretPrefix is the prefix of every secret belonging to a node.
func SecretPrefix(nodeID string) string { return "node/" + nodeID + "/" }

func itoa(i int) string { return strconv.Itoa(i) }

// DefaultPort returns the conventional port for a protocol.
func DefaultPort(p Protocol) int {
	switch p {
	case ProtoSSH:
		return 22
	case ProtoTelnet:
		return 23
	}
	return 0
}
