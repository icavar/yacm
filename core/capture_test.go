// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package core

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yacm-app/yacm/internal/fakedevice"
	"github.com/yacm-app/yacm/internal/store"
)

// runCapture feeds chunks to a capture and returns the file text, what was
// sent to the device and the result.
func runCapture(t *testing.T, prompt string, chunks ...string) (string, string, error) {
	t.Helper()
	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)
	var mu sync.Mutex
	var sent strings.Builder
	cp := newCapture("show tech-support", prompt, w, func(b []byte) error { mu.Lock(); sent.Write(b); mu.Unlock(); return nil }, nil)
	cp.eol = "\n"
	for _, c := range chunks {
		if !cp.feed([]byte(c)) {
			t.Fatalf("chunk %q not taken", c)
		}
		time.Sleep(5 * time.Millisecond)
	}
	var err error
	select {
	case err = <-cp.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the capture did not end at the prompt")
	}
	w.Flush()
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	return buf.String(), sent.String(), err
}

func TestCaptureCiscoPager(t *testing.T) {
	out, sent, err := runCapture(t, "core-sw1#",
		"show tech-support\r\n",
		"\r\n------------------ show version ------------------\r\nCisco IOS XE Software\r\n",
		" --More-- ",
		"\b\b\b\b\b\b\b\b\b\b          \b\b\b\b\b\b\b\b\b\bGigabitEthernet1/0/1 is up\r\n interface Vlan1\r\n",
		"core-sw1#")
	if err != nil {
		t.Fatal(err)
	}
	want := "\n------------------ show version ------------------\nCisco IOS XE Software\nGigabitEthernet1/0/1 is up\n interface Vlan1\n"
	if out != want {
		t.Fatalf("file:\n%q\nwant\n%q", out, want)
	}
	if sent != " " {
		t.Fatalf("sent %q, want one space for the pager", sent)
	}
}

func TestCaptureJunosAndHuaweiPagers(t *testing.T) {
	out, sent, err := runCapture(t, "admin@mx1>",
		"show tech-support \r\nline one\r\n",
		"---(more 45%)---",
		"\r                \rline two\r\n",
		"  ---- More ----",
		"\x1b[42D                                          \x1b[42Dline three\r\n",
		"\r\nadmin@mx1> ")
	if err != nil {
		t.Fatal(err)
	}
	if out != "line one\nline two\nline three\n\n" {
		t.Fatalf("file %q", out)
	}
	if sent != "  " {
		t.Fatalf("sent %q, want two spaces", sent)
	}
}

// A line that looks like the prompt in the middle of the output does not
// end the capture when more output follows at once.
func TestCapturePromptInsideOutput(t *testing.T) {
	out, _, err := runCapture(t, "r1#",
		"show tech-support\r\nbefore\r\nr1#",
		"show clock\r\n12:00:00\r\nr1#")
	if err != nil {
		t.Fatal(err)
	}
	if out != "before\nr1#show clock\n12:00:00\n" {
		t.Fatalf("file %q", out)
	}
}

func TestLooksLikePrompt(t *testing.T) {
	for _, s := range []string{"core-sw1#", "switch>", "lab@srv:~$", "[~HUAWEI]", "<HUAWEI>", "RP/0/RP0/CPU0:xr#", "admin@mx1>"} {
		if !looksLikePrompt(s) {
			t.Errorf("%q is a prompt", s)
		}
	}
	for _, s := range []string{"", "core-sw1#sh ru", "Building configuration..."} {
		if looksLikePrompt(s) {
			t.Errorf("%q is not a prompt", s)
		}
	}
}

// A session to a device whose shell pages show tech-support like IOS: the
// output goes into the file without the pagers, the terminal gets a note
// and the prompt back.
func TestSessionCommandToFile(t *testing.T) {
	a := newTestApp(t)
	var mu sync.Mutex
	final := map[string]TransferEvent{}
	var term strings.Builder
	a.emitter = func(name string, data any) {
		switch name {
		case "prompt":
			go a.AnswerPrompt(data.(map[string]any)["id"].(string), PromptAnswer{Accept: true})
		case "xfer:progress":
			ev := data.(TransferEvent)
			mu.Lock()
			final[ev.ID] = ev
			mu.Unlock()
		case "tab:data":
			b, _ := base64.StdEncoding.DecodeString(data.(map[string]string)["data"])
			mu.Lock()
			term.Write(b)
			mu.Unlock()
		}
	}
	port, _ := startFakeDeviceWith(t, "R1", func(s *fakedevice.Server) {
		s.Shell = func(rw io.ReadWriter) {
			r := bufio.NewReader(rw)
			for {
				io.WriteString(rw, "R1#")
				line, err := r.ReadString('\r')
				if err != nil {
					return
				}
				cmd := strings.TrimSpace(line)
				io.WriteString(rw, cmd+"\r\n")
				if cmd != "show tech-support" {
					continue
				}
				for i := 1; i <= 50; i++ {
					fmt.Fprintf(rw, "line %d\r\n", i)
					if i%20 == 0 {
						io.WriteString(rw, " --More-- ")
						if b, err := r.ReadByte(); err != nil || b != ' ' {
							return
						}
						io.WriteString(rw, "\b\b\b\b\b\b\b\b\b\b          \b\b\b\b\b\b\b\b\b\b")
					}
				}
			}
		}
	})
	n, err := a.SaveNode(store.Node{Name: "R1", Session: &store.Session{Protocol: store.ProtoSSH, Host: "127.0.0.1", Port: port, User: "admin", Auth: store.AuthPassword}}, &SecretInput{Password: sp("pw")})
	if err != nil {
		t.Fatal(err)
	}
	tabID, err := a.Connect(ConnectRequest{NodeID: n.ID, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, a, tabID, "connected")
	tb, _ := a.getTab(tabID)
	for i := 0; i < 100; i++ {
		tb.mu.Lock()
		at := string(tb.watch.line) == "R1#"
		tb.mu.Unlock()
		if at {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	dest := filepath.Join(t.TempDir(), "tech.txt")
	id, err := a.SessionCommandToFile(tabID, "show tech-support", dest)
	if err != nil {
		t.Fatal(err)
	}
	var ev TransferEvent
	for i := 0; i < 250; i++ {
		mu.Lock()
		ev = final[id]
		mu.Unlock()
		if ev.State == "done" || ev.State == "error" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if ev.State != "done" {
		t.Fatalf("transfer %+v", ev)
	}
	b, _ := os.ReadFile(dest)
	got := strings.ReplaceAll(string(b), "\r\n", "\n")
	if strings.Contains(got, "More") || strings.Count(got, "\n") != 50 || !strings.HasPrefix(got, "line 1\n") || !strings.HasSuffix(got, "line 50\n") {
		t.Fatalf("file:\n%s", got)
	}
	mu.Lock()
	shown := term.String()
	mu.Unlock()
	if strings.Contains(shown, "line 7") || !strings.Contains(shown, "[YACM] output saved to") || !strings.HasSuffix(shown, "R1#") {
		t.Fatalf("terminal:\n%q", shown)
	}
}
