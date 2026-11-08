// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// Command yacm-demo-device is a small pretend router for trying YACM without
// real hardware. It serves SSH (password, keyboard-interactive) and telnet
// with a Cisco-like prompt, "enable" with a password and a few show commands,
// and it only listens on this computer (127.0.0.1).
//
//	go run ./cmd/yacm-demo-device
//
// Login: admin / cisco123, enable secret: secret. The ports match the
// sessions in demo.yacm. "reload" (after enable) drops every session and
// closes the ports for 20 seconds, like a real reload.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/icavar/yacm/internal/fakedevice"
	"github.com/icavar/yacm/internal/fileserver"
	"github.com/icavar/yacm/internal/xfer"
)

const (
	user     = "admin"
	password = "cisco123"
	enable   = "secret"
)

func main() {
	sshAddr := flag.String("ssh", "127.0.0.1:2222", "SSH listen address (empty = off)")
	legacyAddr := flag.String("ssh-legacy", "127.0.0.1:2224", "SSH address that only offers diffie-hellman-group14-sha1 (empty = off)")
	telnetAddr := flag.String("telnet", "127.0.0.1:2323", "telnet listen address (empty = off)")
	hostname := flag.String("hostname", "R1", "device hostname")
	keyFile := flag.String("hostkey", defaultKeyFile(), "file that keeps the SSH host key between runs (empty = new key every run)")
	flashDir := flag.String("flash", defaultFlashDir(), "folder that holds the device's flash: (file transfers)")
	flag.DurationVar(&reloadFor, "reload-time", reloadFor, "how long \"reload\" keeps the device down")
	flag.BoolVar(&oneChannel, "one-channel", true, "like Cisco IOS: one session channel per SSH connection, and the connection closes with it")
	flag.Parse()
	log.SetFlags(log.Ltime)

	signer := hostKey(*keyFile)
	fs := &fakedevice.FS{Root: *flashDir, Log: log.Printf}
	if *flashDir == "" {
		fs.Root = filepath.Join(os.TempDir(), fmt.Sprintf("yacm-demo-flash-%d", os.Getpid()))
	}
	if err := fs.Seed(*hostname); err != nil {
		fmt.Println("Cannot prepare the flash folder:", err)
	}
	fmt.Println("YACM demo device: a pretend router for trying YACM without real hardware.")
	fmt.Println("It is not needed to use YACM and only accepts connections from this computer.")
	fmt.Println()
	for _, l := range [][2]string{{"SSH", *sshAddr}, {"SSH, old algorithms only", *legacyAddr}, {"Telnet", *telnetAddr}} {
		if l[1] != "" {
			fmt.Printf("  %-26s %s\n", l[0], l[1])
		}
	}
	fmt.Printf("\n  Login %s / %s, enable secret %s\n", user, password, enable)
	fmt.Printf("  SSH host key %s\n", ssh.FingerprintSHA256(signer.PublicKey()))
	fmt.Printf("  flash: is the folder %s (SCP server on)\n\n", fs.Root)
	fmt.Println("Import demo.yacm in YACM to get sessions for it. Press Ctrl+C or close this window to stop.")
	fmt.Println()

	cfg := func(legacy bool) *ssh.ServerConfig {
		c := &ssh.ServerConfig{
			PasswordCallback: func(m ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
				if m.User() == user && string(p) == password {
					return nil, nil
				}
				time.Sleep(300 * time.Millisecond)
				return nil, errors.New("denied")
			},
			KeyboardInteractiveCallback: func(m ssh.ConnMetadata, ch ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
				a, err := ch("", "", []string{"Password: "}, []bool{false})
				if err == nil && m.User() == user && len(a) == 1 && a[0] == password {
					return nil, nil
				}
				return nil, errors.New("denied")
			},
			BannerCallback: func(ssh.ConnMetadata) string { return "Authorized access only.\n" },
		}
		if legacy {
			c.KeyExchanges = []string{ssh.InsecureKeyExchangeDH14SHA1}
		}
		c.AddHostKey(signer)
		return c
	}
	if *sshAddr != "" {
		go serveSSH(*sshAddr, cfg(false), *hostname, fs)
	}
	if *legacyAddr != "" {
		go serveSSH(*legacyAddr, cfg(true), *hostname+"-old", fs)
	}
	if *telnetAddr != "" {
		go serveTelnet(*telnetAddr, *hostname, fs)
	}
	select {}
}

// fail explains a port that cannot be opened and waits, so the message stays
// readable when the program was started by double-clicking it.
func fail(addr string, err error) {
	fmt.Printf("\nCannot listen on %s: %v\n", addr, err)
	fmt.Println("Another program (maybe a second demo device) already uses this port.")
	fmt.Println("Press Enter to close.")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	os.Exit(1)
}

// defaultKeyFile keeps the host key in the user cache folder, so YACM does
// not warn about a changed host key every time the demo device restarts.
func defaultKeyFile() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "yacm-demo-device", "ssh_host_ed25519_key")
}

// defaultFlashDir keeps the pretend flash: next to the host key.
func defaultFlashDir() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "yacm-demo-device", "flash")
}

func hostKey(path string) ssh.Signer {
	if path != "" {
		if data, err := os.ReadFile(path); err == nil {
			if s, err := ssh.ParsePrivateKey(data); err == nil {
				return s
			}
		}
	}
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	if path != "" {
		if block, err := ssh.MarshalPrivateKey(priv, "yacm-demo-device"); err == nil {
			_ = os.MkdirAll(filepath.Dir(path), 0o700)
			_ = os.WriteFile(path, pem.EncodeToMemory(block), 0o600)
		}
	}
	return signer
}

// oneChannel: Cisco IOS takes one session channel per SSH connection and
// closes the connection when it ends.
var oneChannel = true

func serveSSH(addr string, cfg *ssh.ServerConfig, host string, fs *fakedevice.FS) {
	srv := &fakedevice.Server{
		Config:            cfg,
		FS:                fs,
		Shell:             func(rw io.ReadWriter) { shell(rw, host, fs) },
		RemoteForward:     true,
		MaxSessions:       map[bool]int{true: 1}[oneChannel],
		CloseAfterSession: oneChannel,
		Logf:              log.Printf,
		// "ssh admin@127.0.0.1 -p 2222 show version": commands on an exec
		// channel, as Get show tech and the file browser use them.
		Exec: func(rw io.ReadWriter, cmd string) (int, bool) {
			return 0, runCommand(rw, strings.TrimSpace(cmd), host, true, fs)
		},
	}
	serve(addr, srv.ServeConn)
}

// readLine reads a line with local echo (the device echoes, as a PTY would).
func readLine(r *bufio.Reader, w io.Writer, echo bool) (string, error) {
	var b []byte
	for {
		c, err := r.ReadByte()
		if err != nil {
			return "", err
		}
		switch c {
		case '\r', '\n':
			if c == '\r' {
				if r.Buffered() > 0 {
					if n, _ := r.Peek(1); len(n) == 1 && (n[0] == '\n' || n[0] == 0) {
						r.ReadByte()
					}
				}
			}
			io.WriteString(w, "\r\n")
			return string(b), nil
		case 0x7f, 0x08:
			if len(b) > 0 {
				b = b[:len(b)-1]
				if echo {
					io.WriteString(w, "\b \b")
				}
			}
		case 0x03:
			io.WriteString(w, "^C\r\n")
			return "", nil
		default:
			if c >= 0x20 {
				b = append(b, c)
				if echo {
					w.Write([]byte{c})
				}
			}
		}
	}
}

func shell(rw io.ReadWriter, host string, fs *fakedevice.FS) {
	r := bufio.NewReader(rw)
	fmt.Fprintf(rw, "\r\n%s uptime is 4 weeks, 2 days\r\n\r\n", host)
	priv := false
	length := 24 // terminal length: a --More-- after each screenful, 0 = none
	for {
		p := ">"
		if priv {
			p = "#"
		}
		fmt.Fprintf(rw, "%s%s", host, p)
		line, err := readLine(r, rw, true)
		if err != nil {
			return
		}
		cmd := strings.TrimSpace(line)
		switch {
		case cmd == "" || strings.HasPrefix(cmd, "!"): // "!" starts a comment, as on IOS
		case cmd == "exit" || cmd == "logout" || cmd == "quit":
			return
		case cmd == "enable" || cmd == "en":
			io.WriteString(rw, "Password: ")
			pw, err := readLine(r, rw, false)
			if err != nil {
				return
			}
			if pw == enable {
				priv = true
			} else {
				io.WriteString(rw, "% Access denied\r\n\r\n")
			}
		case cmd == "disable":
			priv = false
		case cmd == "reload":
			if !priv {
				io.WriteString(rw, "% Invalid input detected at '^' marker.\r\n\r\n")
				break
			}
			io.WriteString(rw, "Proceed with reload? [confirm]")
			ans, err := readLine(r, rw, true)
			if err != nil {
				return
			}
			if a := strings.ToLower(strings.TrimSpace(ans)); a != "" && a != "y" && a != "yes" {
				io.WriteString(rw, "\r\n")
				break
			}
			io.WriteString(rw, reloadMessage(host))
			time.Sleep(300 * time.Millisecond)
			go dev.reload()
			return
		case strings.HasPrefix(cmd, "terminal length"):
			if f := strings.Fields(cmd); len(f) == 3 {
				if n, err := strconv.Atoi(f[2]); err == nil && n >= 0 {
					length = n
				}
			}
		case cmd == "conf t" || cmd == "configure terminal":
			if !priv {
				io.WriteString(rw, "% Invalid input detected at '^' marker.\r\n")
			} else {
				io.WriteString(rw, "Enter configuration commands, one per line.  End with CNTL/Z.\r\n")
			}
		case priv && copyCommand(rw, r, cmd, fs):
		case fileCommand(rw, r, cmd, priv, fs):
		case runPaged(rw, r, cmd, host, priv, fs, length):
		default:
			io.WriteString(rw, "% Invalid input detected at '^' marker.\r\n\r\n")
		}
	}
}

// runPaged runs a show command in the shell and pages its output like IOS:
// " --More-- " after each screenful; space shows the next page, Enter one
// more line, anything else stops.
func runPaged(rw io.ReadWriter, r *bufio.Reader, cmd, host string, priv bool, fs *fakedevice.FS, length int) bool {
	var out strings.Builder
	if !runCommand(&out, cmd, host, priv, fs) {
		return false
	}
	lines := strings.SplitAfter(out.String(), "\n")
	shown := 0
	for i, l := range lines {
		io.WriteString(rw, l)
		shown++
		if length == 0 || shown < length-1 || i == len(lines)-1 {
			continue
		}
		io.WriteString(rw, " --More-- ")
		b, err := r.ReadByte()
		io.WriteString(rw, "\b\b\b\b\b\b\b\b\b\b          \b\b\b\b\b\b\b\b\b\b")
		switch {
		case err != nil:
			return true
		case b == ' ':
			shown = 0
		case b == '\r' || b == '\n':
			shown = length - 2
		default:
			return true
		}
	}
	return true
}

// runCommand prints the output of a show command (shell and exec channel).
// It reports false for a command it does not know.
func runCommand(rw io.Writer, cmd, host string, priv bool, fs *fakedevice.FS) bool {
	// "show … | redirect flash:file" writes the output to flash instead.
	if i := strings.Index(cmd, "| redirect "); i > 0 && fs != nil {
		target := strings.TrimSpace(cmd[i+len("| redirect "):])
		var buf strings.Builder
		if !runCommand(&buf, strings.TrimSpace(cmd[:i]), host, priv, nil) {
			return false
		}
		if err := fs.WriteFile(target, []byte(strings.ReplaceAll(buf.String(), "\r\n", "\n"))); err != nil {
			fmt.Fprintf(rw, "%s\r\n", err)
		}
		return true
	}
	switch {
	case strings.HasPrefix(cmd, "show ver") || strings.HasPrefix(cmd, "sh ver"):
		fmt.Fprintf(rw, "Cisco IOS XE Software, Version 17.12.4\r\n%s uptime is 4 weeks, 2 days\r\nSystem image file is \"bootflash:packages.conf\"\r\n", host)
	case strings.HasPrefix(cmd, "show ip int") || strings.HasPrefix(cmd, "sh ip int"):
		io.WriteString(rw, "Interface              IP-Address      OK? Method Status                Protocol\r\n")
		io.WriteString(rw, "GigabitEthernet1       10.0.0.1        YES NVRAM  up                    up      \r\n")
		io.WriteString(rw, "GigabitEthernet2       192.0.2.1       YES NVRAM  \x1b[31madministratively down\x1b[0m down    \r\n")
		io.WriteString(rw, "Loopback0              10.255.0.1      YES NVRAM  up                    up      \r\n")
	case strings.HasPrefix(cmd, "show ip bgp sum") || strings.HasPrefix(cmd, "sh ip bgp sum"):
		io.WriteString(rw, "Neighbor        V    AS MsgRcvd MsgSent   TblVer  InQ OutQ Up/Down  State/PfxRcd\r\n10.255.0.2      4 65000   48211   48190     9921    0    0 4w2d          812\r\n10.255.0.3      4 65000   48201   48188     9921    0    0 4w2d          811\r\n")
	case strings.HasPrefix(cmd, "show tech") || strings.HasPrefix(cmd, "sh tech"):
		// A long output (about 50,000 lines) for trying scrollback,
		// highlighting and logging under load.
		bw := bufio.NewWriterSize(rw, 64<<10)
		for i := 0; i < 2500; i++ {
			fmt.Fprintf(bw, "GigabitEthernet1/0/%d is up, line protocol is up (connected)\r\n", i%48+1)
			fmt.Fprintf(bw, "  Hardware is Gigabit Ethernet, address is 00a0.c9%02x.%04x (bia 00a0.c9%02x.%04x)\r\n", i%256, i, i%256, i)
			fmt.Fprintf(bw, "  Internet address is 10.%d.%d.1/24\r\n", i/256, i%256)
			bw.WriteString("  MTU 1500 bytes, BW 1000000 Kbit/sec, DLY 10 usec,\r\n")
			bw.WriteString("     reliability 255/255, txload 1/255, rxload 1/255\r\n")
			bw.WriteString("  Encapsulation ARPA, loopback not set\r\n")
			bw.WriteString("  Keepalive set (10 sec)\r\n")
			bw.WriteString("  Full-duplex, 1000Mb/s, media type is 10/100/1000BaseTX\r\n")
			bw.WriteString("  input flow-control is off, output flow-control is unsupported\r\n")
			bw.WriteString("  ARP type: ARPA, ARP Timeout 04:00:00\r\n")
			fmt.Fprintf(bw, "  Last input 00:00:%02d, output 00:00:00, output hang never\r\n", i%60)
			bw.WriteString("  Last clearing of \"show interface\" counters never\r\n")
			bw.WriteString("  Input queue: 0/375/0/0 (size/max/drops/flushes); Total output drops: 0\r\n")
			bw.WriteString("  Queueing strategy: fifo\r\n")
			bw.WriteString("  Output queue: 0/40 (size/max)\r\n")
			fmt.Fprintf(bw, "  5 minute input rate %d000 bits/sec, %d packets/sec\r\n", i%97, i%13)
			fmt.Fprintf(bw, "  5 minute output rate %d000 bits/sec, %d packets/sec\r\n", i%89, i%11)
			fmt.Fprintf(bw, "     %d packets input, %d bytes, 0 no buffer\r\n", i*1000, i*64000)
			if i%50 == 0 {
				fmt.Fprintf(bw, "%%LINK-3-UPDOWN: Interface GigabitEthernet1/0/%d, changed state to down\r\n", i%48+1)
			}
			bw.WriteString("\r\n")
		}
		bw.Flush()
	case strings.HasPrefix(cmd, "show run") || strings.HasPrefix(cmd, "sh run"):
		if !priv {
			io.WriteString(rw, "% Invalid input detected at '^' marker.\r\n\r\n")
			return true
		}
		// Made-up secrets, so the log masking can be seen in a session log.
		fmt.Fprintf(rw, "Building configuration...\r\n\r\nCurrent configuration : 1024 bytes\r\n!\r\nhostname %s\r\n!\r\n"+
			"enable secret 9 $9$demoDEMOdemo$notARealHashJustForTesting1234\r\n"+
			"username admin privilege 15 secret 5 $1$demo$notarealhash000\r\n!\r\n"+
			"interface GigabitEthernet1\r\n ip address 10.0.0.1 255.255.255.0\r\n ip ospf message-digest-key 1 md5 7 0822455D0A16\r\n!\r\n"+
			"router bgp 65000\r\n neighbor 10.255.0.2 remote-as 65000\r\n neighbor 10.255.0.2 password 7 045802150C2E\r\n!\r\n"+
			"snmp-server community demo-community RO\r\ntacacs-server key 7 0822455D0A16\r\n!\r\nend\r\n\r\n", host)
	default:
		return false
	}
	return true
}

func serveTelnet(addr, host string, fs *fakedevice.FS) {
	serve(addr, func(nc net.Conn) {
		{
			defer nc.Close()
			// WILL ECHO, WILL SGA, DO NAWS: like a real device.
			nc.Write([]byte{255, 251, 1, 255, 251, 3, 255, 253, 31})
			fr := &telnetFilter{r: nc}
			r := bufio.NewReader(fr)
			io.WriteString(nc, "\r\nUser Access Verification\r\n\r\nUsername: ")
			u, err := readLine(r, nc, true)
			for tries := 0; err == nil && u == "" && tries < 5; tries++ {
				io.WriteString(nc, "Username: ") // an empty line asks again, as on IOS
				u, err = readLine(r, nc, true)
			}
			if err != nil {
				return
			}
			io.WriteString(nc, "Password: ")
			p, err := readLine(r, nc, false)
			if err != nil {
				return
			}
			if u != user || p != password {
				io.WriteString(nc, "% Login invalid\r\n")
				return
			}
			shell(struct {
				io.Reader
				io.Writer
			}{r, nc}, host, fs)
		}
	})
}

// telnetFilter strips IAC sequences from client input.
type telnetFilter struct {
	r     io.Reader
	state int
}

func (t *telnetFilter) Read(p []byte) (int, error) {
	buf := make([]byte, len(p))
	for {
		n, err := t.r.Read(buf)
		out := 0
		for _, c := range buf[:n] {
			switch t.state {
			case 0:
				if c == 255 {
					t.state = 1
				} else {
					p[out] = c
					out++
				}
			case 1:
				switch {
				case c == 250:
					t.state = 3
				case c >= 251 && c <= 254:
					t.state = 2
				case c == 255:
					p[out] = c
					out++
					t.state = 0
				default:
					t.state = 0
				}
			case 2:
				t.state = 0
			case 3:
				if c == 255 {
					t.state = 4
				}
			case 4:
				if c == 240 {
					t.state = 0
				} else {
					t.state = 3
				}
			}
		}
		if out > 0 || err != nil {
			return out, err
		}
	}
}

// fileCommand runs the flash: commands of the interactive shell (dir,
// show file systems, verify /md5, delete). r reads answers to prompts.
func fileCommand(rw io.Writer, r *bufio.Reader, cmd string, priv bool, fs *fakedevice.FS) bool {
	f := strings.Fields(cmd)
	if len(f) == 0 || fs == nil {
		return false
	}
	out := func(s string) { io.WriteString(rw, strings.ReplaceAll(s, "\n", "\r\n")) }
	switch {
	case f[0] == "dir":
		out(fs.Dir(strings.Join(f[1:], " ")))
	case strings.HasPrefix(cmd, "show file sys") || strings.HasPrefix(cmd, "sh file sys"):
		out(fs.FileSystems())
	case f[0] == "verify" && len(f) >= 3 && f[1] == "/md5":
		out(fs.Verify(strings.Join(f[2:], " ")))
	case f[0] == "delete":
		if !priv {
			out("% Invalid input detected at '^' marker.\n")
			return true
		}
		args := f[1:]
		force := len(args) > 0 && args[0] == "/force"
		if force {
			args = args[1:]
		}
		p := strings.Join(args, " ")
		if !force {
			fmt.Fprintf(rw, "Delete filename [%s]? ", p)
			if ans, err := readLine(r, rw, true); err != nil || (ans != "" && ans != p) {
				return true
			}
			fmt.Fprintf(rw, "Delete %s? [confirm]", p)
			if ans, err := readLine(r, rw, true); err != nil || (ans != "" && !strings.HasPrefix(strings.ToLower(ans), "y")) {
				return true
			}
		}
		if err := fs.Delete(p); err != nil {
			out(err.Error() + "\n")
		}
	default:
		return false
	}
	return true
}

// copyCommand runs "copy <from> <to>" between flash: and a TFTP, HTTP or
// SCP server on this computer (YACM's file servers), like IOS does.
func copyCommand(rw io.Writer, r *bufio.Reader, cmd string, fs *fakedevice.FS) bool {
	f := strings.Fields(cmd)
	if len(f) < 3 || f[0] != "copy" || fs == nil {
		return false
	}
	src, dst := f[1], f[2]
	isURL := func(s string) bool {
		return strings.HasPrefix(s, "tftp://") || strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "scp://")
	}
	out := func(format string, a ...any) {
		io.WriteString(rw, strings.ReplaceAll(fmt.Sprintf(format, a...), "\n", "\r\n"))
	}
	switch {
	case isURL(src) && strings.Contains(dst, ":") && !isURL(dst):
		u, err := url.Parse(src)
		if err != nil {
			out("%%Error parsing %s\n", src)
			return true
		}
		name := path.Base(u.Path)
		target := dst
		if strings.HasSuffix(dst, ":") || strings.HasSuffix(dst, "/") {
			target = dst + name
		}
		out("Destination filename [%s]? ", strings.TrimPrefix(target[strings.Index(target, ":")+1:], "/"))
		if ans, err := readLine(r, rw, true); err != nil {
			return true
		} else if ans != "" {
			target = target[:strings.Index(target, ":")+1] + ans
		}
		var buf bytes.Buffer
		start := time.Now()
		out("Accessing %s...\n", src)
		if err := fetch(rw, r, u, &buf); err != nil {
			out("%%Error opening %s (%v)\n", src, err)
			return true
		}
		if err := fs.WriteFile(target, buf.Bytes()); err != nil {
			out("%s\n", err)
			return true
		}
		out("Loading %s from %s: %s\n[OK - %d bytes]\n\n%d bytes copied in %.3f secs\n", name, u.Hostname(), strings.Repeat("!", min(40, buf.Len()/65536+1)), buf.Len(), buf.Len(), time.Since(start).Seconds())
	case !isURL(src) && isURL(dst):
		u, err := url.Parse(dst)
		if err != nil {
			out("%%Error parsing %s\n", dst)
			return true
		}
		local, err := fsRead(fs, src)
		if err != nil {
			out("%%Error opening %s (No such file or directory)\n", src)
			return true
		}
		name := path.Base(u.Path)
		if name == "/" || name == "." || name == "" {
			name = path.Base(strings.TrimPrefix(src[strings.Index(src, ":")+1:], "/"))
			u.Path = strings.TrimSuffix(u.Path, "/") + "/" + name
		}
		out("Address or name of remote host [%s]? ", u.Hostname())
		if _, err := readLine(r, rw, true); err != nil {
			return true
		}
		out("Destination filename [%s]? ", name)
		if ans, err := readLine(r, rw, true); err != nil {
			return true
		} else if ans != "" {
			u.Path = path.Join(path.Dir(u.Path), ans)
		}
		start := time.Now()
		if err := send(rw, r, u, local); err != nil {
			out("%%Error opening %s (%v)\n", u.String(), err)
			return true
		}
		out("%s\n%d bytes copied in %.3f secs\n", strings.Repeat("!", min(40, len(local)/65536+1)), len(local), time.Since(start).Seconds())
	default:
		out("%% Copy between these is not supported by the demo device.\n")
	}
	return true
}

func fsRead(fs *fakedevice.FS, p string) ([]byte, error) {
	var buf bytes.Buffer
	h := fs.SCP()
	local, err := h.Resolve(p, "", false)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(local)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	_, err = io.Copy(&buf, f)
	return buf.Bytes(), err
}

// allowedHost: the demo device only copies from and to this computer.
func allowedHost(host string) bool {
	ip := net.ParseIP(host)
	if host == "localhost" || (ip != nil && ip.IsLoopback()) {
		return true
	}
	for _, a := range fileserver.LocalAddresses() {
		if a.IP == host {
			return true
		}
	}
	return false
}

func hostPort(u *url.URL, def string) string {
	if u.Port() != "" {
		return u.Host
	}
	return net.JoinHostPort(u.Hostname(), def)
}

func scpClient(rw io.Writer, r *bufio.Reader, u *url.URL) (*ssh.Client, error) {
	pw, _ := u.User.Password()
	if pw == "" {
		io.WriteString(rw, "Password: ")
		p, err := readLine(r, rw, false)
		if err != nil {
			return nil, err
		}
		pw = p
	}
	return ssh.Dial("tcp", hostPort(u, "22"), &ssh.ClientConfig{
		User: u.User.Username(), Auth: []ssh.AuthMethod{ssh.Password(pw)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 10 * time.Second,
	})
}

func fetch(rw io.Writer, r *bufio.Reader, u *url.URL, w io.Writer) error {
	if !allowedHost(u.Hostname()) {
		return errors.New("the demo device only copies from this computer")
	}
	switch u.Scheme {
	case "tftp":
		_, err := fileserver.TFTPGet(hostPort(u, "69"), strings.TrimPrefix(u.Path, "/"), w, 1468)
		return err
	case "http":
		resp, err := http.Get(u.String())
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return errors.New(resp.Status)
		}
		_, err = io.Copy(w, resp.Body)
		return err
	default:
		c, err := scpClient(rw, r, u)
		if err != nil {
			return err
		}
		defer c.Close()
		return xfer.SCPGet(context.Background(), c, strings.TrimPrefix(u.Path, "/"), w, nil)
	}
}

func send(rw io.Writer, r *bufio.Reader, u *url.URL, data []byte) error {
	if !allowedHost(u.Hostname()) {
		return errors.New("the demo device only copies to this computer")
	}
	switch u.Scheme {
	case "tftp":
		_, err := fileserver.TFTPPut(hostPort(u, "69"), strings.TrimPrefix(u.Path, "/"), bytes.NewReader(data), int64(len(data)), 1468)
		return err
	case "http":
		req, _ := http.NewRequest(http.MethodPut, u.String(), bytes.NewReader(data))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			return errors.New(resp.Status)
		}
		return nil
	default:
		c, err := scpClient(rw, r, u)
		if err != nil {
			return err
		}
		defer c.Close()
		return xfer.SCPPut(context.Background(), c, strings.TrimPrefix(u.Path, "/"), bytes.NewReader(data), int64(len(data)), nil)
	}
}
