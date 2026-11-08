// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// Package applog writes YACM's own log, yacm.log: start and stop, failed
// connections and imports, errors and crashes. It never receives passwords,
// passphrases, tokens or terminal contents; callers pass host names and
// error messages only.
//
// The file is rotated at 1 MB, keeping yacm.log plus four older files.
package applog

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

const (
	maxSize  = 1 << 20
	keep     = 4 // yacm.1.log … yacm.4.log
	fileName = "yacm.log"
)

// setCrashOutput is debug.SetCrashOutput; tests replace it, because Windows
// cannot delete a temporary folder while the runtime holds crash.log open.
var setCrashOutput = debug.SetCrashOutput

var (
	mu   sync.Mutex
	dir  string
	f    *os.File
	size int64
)

// Open starts logging into dir/yacm.log. Fatal Go runtime errors (crashes
// outside a recover) go to dir/crash.log.
func Open(d string) error {
	mu.Lock()
	defer mu.Unlock()
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	dir = d
	if err := openLocked(); err != nil {
		return err
	}
	if cf, err := os.OpenFile(filepath.Join(d, "crash.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		_ = setCrashOutput(cf, debug.CrashOptions{})
		cf.Close() // SetCrashOutput keeps its own copy of the descriptor
	}
	return nil
}

func openLocked() error {
	p := filepath.Join(dir, fileName)
	if fi, err := os.Stat(p); err == nil && fi.Size() >= maxSize {
		rotateLocked()
	}
	nf, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if f != nil {
		f.Close()
	}
	f = nf
	size = 0
	if fi, err := nf.Stat(); err == nil {
		size = fi.Size()
	}
	return nil
}

func rotateLocked() {
	name := func(i int) string {
		if i == 0 {
			return filepath.Join(dir, fileName)
		}
		return filepath.Join(dir, fmt.Sprintf("yacm.%d.log", i))
	}
	_ = os.Remove(name(keep))
	for i := keep - 1; i >= 0; i-- {
		_ = os.Rename(name(i), name(i+1))
	}
}

// Close stops logging.
func Close() {
	mu.Lock()
	defer mu.Unlock()
	if f != nil {
		f.Close()
		f = nil
	}
}

func write(level, msg string) {
	mu.Lock()
	defer mu.Unlock()
	if f == nil {
		return
	}
	line := time.Now().Format("2006-01-02 15:04:05.000") + " " + level + " " + strings.TrimRight(msg, "\n") + "\n"
	n, _ := f.WriteString(line)
	size += int64(n)
	if size >= maxSize {
		f.Close()
		f = nil
		rotateLocked()
		_ = openLocked()
	}
}

// Infof logs an event.
func Infof(format string, args ...any) { write("INFO ", fmt.Sprintf(format, oneLine(args)...)) }

// oneLine keeps values (session names, hosts, messages from devices) on one
// line, so a name with a line break cannot forge entries in the log.
func oneLine(args []any) []any {
	out := make([]any, len(args))
	for i, a := range args {
		switch v := a.(type) {
		case string:
			out[i] = lineEscaper.Replace(v)
		case error:
			out[i] = lineEscaper.Replace(v.Error())
		case fmt.Stringer:
			out[i] = lineEscaper.Replace(v.String())
		default:
			out[i] = a
		}
	}
	return out
}

var lineEscaper = strings.NewReplacer("\r", `\r`, "\n", `\n`)

// Warnf logs something that went wrong but was handled.
func Warnf(format string, args ...any) { write("WARN ", fmt.Sprintf(format, oneLine(args)...)) }

// Errorf logs an error.
func Errorf(format string, args ...any) { write("ERROR", fmt.Sprintf(format, oneLine(args)...)) }

// Recover logs a panic with its stack. Use as `defer applog.Recover("where", nil)`;
// after logging, onPanic (if any) runs, and the panic does not end the program.
func Recover(where string, onPanic func(v any)) {
	if v := recover(); v != nil {
		write("PANIC", fmt.Sprintf("%s: %v\n%s", where, v, debug.Stack()))
		if onPanic != nil {
			onPanic(v)
		}
	}
}

// Tail returns the last n lines of the current log file.
func Tail(n int) string {
	mu.Lock()
	d := dir
	mu.Unlock()
	if d == "" {
		return ""
	}
	p := filepath.Join(d, fileName)
	data, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	var lines []string
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
		if len(lines) > 4*n {
			lines = append([]string(nil), lines[len(lines)-n:]...)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// Dir is the folder of the log files.
func Dir() string {
	mu.Lock()
	defer mu.Unlock()
	return dir
}
