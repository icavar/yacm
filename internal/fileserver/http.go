// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package fileserver

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// HTTP serves a folder: GET downloads (copy http://… flash:), PUT uploads
// (copy flash:… http://…, curl -T), and a plain listing of folders.
type HTTP struct {
	cfg Config
	ln  net.Listener
	srv *http.Server
}

func startHTTP(cfg Config) (*HTTP, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort(cfg.Bind, strconv.Itoa(cfg.Port)))
	if err != nil {
		return nil, listenError(err, cfg.Port)
	}
	s := &HTTP{cfg: cfg, ln: ln}
	s.srv = &http.Server{Handler: s, ReadHeaderTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute}
	go func() { _ = s.srv.Serve(ln) }()
	return s, nil
}

// Port is the TCP port.
func (s *HTTP) Port() int { return s.ln.Addr().(*net.TCPAddr).Port }

// Close stops the server.
func (s *HTTP) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.srv.Shutdown(ctx); err != nil {
		return s.srv.Close()
	}
	return nil
}

type progressWriter struct {
	w    io.Writer
	n    int64
	tick func(int64)
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.n += int64(n)
	p.tick(p.n)
	return n, err
}

func (s *HTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Server", "YACM")
	remote, _, _ := net.SplitHostPort(r.RemoteAddr)
	if !s.cfg.allowed(net.ParseIP(remote)) {
		s.cfg.emit(Event{Kind: "http", Remote: remote, File: r.URL.Path, State: "error", Error: "refused: address not allowed"})
		http.Error(w, "not allowed from this address", http.StatusForbidden)
		return
	}
	p, err := url.PathUnescape(r.URL.EscapedPath())
	if err != nil {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		s.get(w, r, remote, p)
	case http.MethodPut, http.MethodPost:
		s.put(w, r, remote, p)
	default:
		w.Header().Set("Allow", "GET, HEAD, PUT")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *HTTP) get(w http.ResponseWriter, r *http.Request, remote, p string) {
	full, err := resolve(s.cfg.Folder, p, false)
	if err != nil && strings.Contains(err.Error(), "not a file") {
		// "\" is a separator on Windows: clean it like "/" so "..\" cannot
		// leave the folder.
		full, err = filepath.Join(s.cfg.Folder, filepath.FromSlash(path.Clean("/"+strings.ReplaceAll(p, `\`, "/")))), nil
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	st, err := os.Stat(full)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if st.IsDir() {
		if !s.cfg.Listing {
			http.Error(w, "folder listing is off", http.StatusForbidden)
			return
		}
		s.listing(w, full, p)
		return
	}
	f, err := os.Open(full)
	if err != nil {
		http.Error(w, "cannot read", http.StatusForbidden)
		return
	}
	defer f.Close()
	ev := Event{Kind: "http", Remote: remote, File: displayName(s.cfg.Folder, full), Total: st.Size(), State: "start"}
	if r.Method == http.MethodGet {
		s.cfg.emit(ev)
	}
	pw := &progressWriter{w: w, tick: func(n int64) { ev.Done, ev.State = n, "progress"; s.cfg.emitProgress(ev) }}
	http.ServeContent(&rw{ResponseWriter: w, body: pw}, r, st.Name(), st.ModTime(), f)
	if r.Method == http.MethodGet {
		ev.Done = pw.n
		ev.State = "done"
		if pw.n < st.Size() && r.Header.Get("Range") == "" {
			ev.State, ev.Error = "error", "the device stopped reading"
		}
		s.cfg.emit(ev)
	}
}

// rw counts the body ServeContent writes.
type rw struct {
	http.ResponseWriter
	body io.Writer
}

func (r *rw) Write(b []byte) (int, error) { return r.body.Write(b) }

func (s *HTTP) listing(w http.ResponseWriter, dir, p string) {
	des, err := os.ReadDir(dir)
	if err != nil {
		http.Error(w, "cannot read", http.StatusForbidden)
		return
	}
	sort.Slice(des, func(i, j int) bool { return des[i].Name() < des[j].Name() })
	if !strings.HasSuffix(p, "/") {
		p += "/"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><title>%s</title><h1>%s</h1><pre>\n", html.EscapeString(p), html.EscapeString(p))
	for _, d := range des {
		if strings.HasSuffix(d.Name(), ".part") {
			continue
		}
		name := d.Name()
		if d.IsDir() {
			name += "/"
		}
		size := ""
		if fi, err := d.Info(); err == nil && !d.IsDir() {
			size = strconv.FormatInt(fi.Size(), 10)
		}
		fmt.Fprintf(w, "<a href=\"%s\">%s</a>  %s\n", html.EscapeString(url.PathEscape(name)), html.EscapeString(name), size)
	}
	fmt.Fprint(w, "</pre>")
}

func (s *HTTP) put(w http.ResponseWriter, r *http.Request, remote, p string) {
	if !s.cfg.AllowUpload {
		http.Error(w, "uploads are off on this server", http.StatusForbidden)
		return
	}
	full, err := resolve(s.cfg.Folder, p, true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if _, err := os.Stat(full); err == nil && !s.cfg.Overwrite {
		http.Error(w, "file already exists", http.StatusConflict)
		return
	}
	ev := Event{Kind: "http", Remote: remote, File: displayName(s.cfg.Folder, full), Upload: true, Total: r.ContentLength, State: "start"}
	s.cfg.emit(ev)
	tmp := full + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		http.Error(w, "cannot write", http.StatusInternalServerError)
		return
	}
	var body io.Reader = r.Body
	if s.cfg.MaxUpload > 0 {
		body = io.LimitReader(r.Body, s.cfg.MaxUpload+1)
	}
	pw := &progressWriter{w: f, tick: func(n int64) { ev.Done, ev.State = n, "progress"; s.cfg.emitProgress(ev) }}
	_, err = io.Copy(pw, body)
	cerr := f.Close()
	if err == nil {
		err = cerr
	}
	if err == nil && s.cfg.MaxUpload > 0 && pw.n > s.cfg.MaxUpload {
		err = errors.New("file too large")
	}
	if err == nil && r.ContentLength > 0 && pw.n != r.ContentLength {
		err = fmt.Errorf("got %d of %d bytes", pw.n, r.ContentLength)
	}
	if err == nil {
		_ = os.Remove(full)
		err = os.Rename(tmp, full)
	}
	ev.Done = pw.n
	if err != nil {
		os.Remove(tmp)
		ev.State, ev.Error = "error", err.Error()
		s.cfg.emit(ev)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	ev.State = "done"
	s.cfg.emit(ev)
	w.WriteHeader(http.StatusCreated)
}
