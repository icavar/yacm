// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package fileserver

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// TFTP (RFC 1350) with the blksize, timeout and tsize options (RFC 2347,
// 2348, 2349) and block number rollover for files over 32 MB. Network
// devices use it on lab and console networks where nothing else is set up.

const (
	opRRQ   = 1
	opWRQ   = 2
	opDATA  = 3
	opACK   = 4
	opERROR = 5
	opOACK  = 6

	errNotFound  = 1
	errAccess    = 2
	errDiskFull  = 3
	errIllegal   = 4
	errExists    = 6
	defaultBlock = 512
	maxBlock     = 65464
)

var (
	tftpTimeout = 3 * time.Second
	tftpRetries = 6
)

type tftpRequest struct {
	op       int
	file     string
	mode     string
	options  map[string]string
	optOrder []string
}

func parseRequest(b []byte) (*tftpRequest, error) {
	if len(b) < 4 {
		return nil, errors.New("short packet")
	}
	r := &tftpRequest{op: int(binary.BigEndian.Uint16(b)), options: map[string]string{}}
	if r.op != opRRQ && r.op != opWRQ {
		return nil, fmt.Errorf("unexpected opcode %d", r.op)
	}
	parts := strings.Split(string(b[2:]), "\x00")
	if len(parts) < 2 {
		return nil, errors.New("bad request")
	}
	r.file, r.mode = parts[0], strings.ToLower(parts[1])
	for i := 2; i+1 < len(parts); i += 2 {
		k := strings.ToLower(parts[i])
		if k == "" {
			break
		}
		r.options[k] = parts[i+1]
		r.optOrder = append(r.optOrder, k)
	}
	return r, nil
}

func errorPacket(code int, msg string) []byte {
	b := make([]byte, 4, 5+len(msg))
	binary.BigEndian.PutUint16(b, opERROR)
	binary.BigEndian.PutUint16(b[2:], uint16(code))
	b = append(b, msg...)
	return append(b, 0)
}

func ackPacket(block uint16) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint16(b, opACK)
	binary.BigEndian.PutUint16(b[2:], block)
	return b
}

func oackPacket(opts [][2]string) []byte {
	b := []byte{0, opOACK}
	for _, o := range opts {
		b = append(b, o[0]...)
		b = append(b, 0)
		b = append(b, o[1]...)
		b = append(b, 0)
	}
	return b
}

// TFTP serves one folder over TFTP.
//
// Transfers run on the port the server listens on instead of a new port
// each (the RFC 1350 TID): the device sees its replies come from the
// address it asked, which stateful filters let in. Arista EOS's default
// control-plane ACL, NAT and firewalls drop replies from a new port.
// Transfers are told apart by the device's address and port.
type TFTP struct {
	cfg    Config
	conn   *net.UDPConn
	wg     sync.WaitGroup
	quit   chan struct{}
	mu     sync.Mutex
	active map[string]chan []byte // device address -> its transfer's packets
}

func startTFTP(cfg Config) (*TFTP, error) {
	addr := net.JoinHostPort(cfg.Bind, strconv.Itoa(cfg.Port))
	ua, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, err
	}
	c, err := net.ListenUDP("udp", ua)
	if err != nil {
		return nil, listenError(err, cfg.Port)
	}
	s := &TFTP{cfg: cfg, conn: c, quit: make(chan struct{}), active: map[string]chan []byte{}}
	s.wg.Add(1)
	go s.serve()
	return s, nil
}

// Port is the UDP port the server listens on.
func (s *TFTP) Port() int { return s.conn.LocalAddr().(*net.UDPAddr).Port }

// Close stops the server and running transfers.
func (s *TFTP) Close() error {
	close(s.quit)
	err := s.conn.Close()
	s.wg.Wait()
	return err
}

func (s *TFTP) serve() {
	defer s.wg.Done()
	buf := make([]byte, maxBlock+4)
	for {
		n, from, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		key := from.String()
		s.mu.Lock()
		in, busy := s.active[key]
		s.mu.Unlock()
		if busy {
			select {
			case in <- append([]byte(nil), buf[:n]...):
			default: // the transfer is behind: the device sends again
			}
			continue
		}
		if !s.cfg.allowed(from.IP) {
			_, _ = s.conn.WriteToUDP(errorPacket(errAccess, "not allowed from this address"), from)
			s.cfg.emit(Event{Kind: "tftp", Remote: from.IP.String(), State: "error", Error: "refused: address not allowed"})
			continue
		}
		req, err := parseRequest(buf[:n])
		if err != nil {
			_, _ = s.conn.WriteToUDP(errorPacket(errIllegal, err.Error()), from)
			continue
		}
		in = make(chan []byte, 64)
		s.mu.Lock()
		s.active[key] = in
		s.mu.Unlock()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() {
				s.mu.Lock()
				delete(s.active, key)
				s.mu.Unlock()
			}()
			s.transfer(req, from, in)
		}()
	}
}

func (s *TFTP) transfer(req *tftpRequest, peer *net.UDPAddr, in chan []byte) {
	c := s.conn
	upload := req.op == opWRQ
	ev := Event{Kind: "tftp", Remote: peer.IP.String(), File: req.file, Upload: upload, State: "start"}
	fail := func(code int, msg string) {
		_, _ = c.WriteToUDP(errorPacket(code, msg), peer)
		ev.State, ev.Error = "error", msg
		s.cfg.emit(ev)
	}
	if upload && !s.cfg.AllowUpload {
		fail(errAccess, "uploads are off on this server")
		return
	}
	path, err := resolve(s.cfg.Folder, req.file, upload)
	if err != nil {
		fail(errAccess, err.Error())
		return
	}
	ev.File = displayName(s.cfg.Folder, path)
	blk := defaultBlock
	timeout := tftpTimeout
	var opts [][2]string
	var size int64 = -1
	if !upload {
		st, err := os.Stat(path)
		if err != nil || st.IsDir() {
			fail(errNotFound, "file not found")
			return
		}
		size = st.Size()
	} else if _, err := os.Stat(path); err == nil && !s.cfg.Overwrite {
		fail(errExists, "file already exists")
		return
	}
	for _, k := range req.optOrder {
		v := req.options[k]
		switch k {
		case "blksize":
			if n, err := strconv.Atoi(v); err == nil && n >= 8 {
				blk = min(n, maxBlock, 65464)
				// Stay under a typical MTU path when asked for jumbo blocks.
				opts = append(opts, [2]string{"blksize", strconv.Itoa(blk)})
			}
		case "timeout":
			if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 255 {
				timeout = time.Duration(n) * time.Second
				opts = append(opts, [2]string{"timeout", v})
			}
		case "tsize":
			if upload {
				if n, err := strconv.ParseInt(v, 10, 64); err == nil {
					size = n
					if s.cfg.MaxUpload > 0 && n > s.cfg.MaxUpload {
						fail(errDiskFull, "file too large")
						return
					}
				}
				opts = append(opts, [2]string{"tsize", v})
			} else {
				opts = append(opts, [2]string{"tsize", strconv.FormatInt(size, 10)})
			}
		}
	}
	ev.Total = size
	s.cfg.emit(ev)
	t := &tftpConn{c: c, in: in, peer: peer, timeout: timeout, quit: s.quit}
	var n int64
	if upload {
		n, err = t.receive(path, blk, opts, func(done int64) { ev.Done = done; ev.State = "progress"; s.cfg.emitProgress(ev) })
	} else {
		n, err = t.send(path, blk, opts, func(done int64) { ev.Done = done; ev.State = "progress"; s.cfg.emitProgress(ev) })
	}
	ev.Done = n
	var stopped *stoppedByDevice
	switch {
	case err == nil:
		ev.State = "done"
	case !upload && errors.As(err, &stopped):
		// The device ended the copy itself: Cisco IOS and IOS XE read the
		// start of the file first (does it exist, how large, the image
		// header) and stop with "Session terminated" before the real copy;
		// or the user cancelled on the device. Not a failure of YACM.
		ev.State = "checked"
	default:
		ev.State, ev.Error = "error", err.Error()
	}
	s.cfg.emit(ev)
	t.linger()
}

// linger answers a repeated last block for a moment (our final ACK got lost).
func (t *tftpConn) linger() {
	if t.lastAck == nil {
		return
	}
	timer := time.NewTimer(t.timeout)
	defer timer.Stop()
	for {
		select {
		case p := <-t.in:
			if len(p) >= 4 && binary.BigEndian.Uint16(p) == opDATA {
				_, _ = t.c.WriteToUDP(t.lastAck, t.peer)
			}
		case <-timer.C:
			return
		case <-t.quit:
			return
		}
	}
}

type tftpConn struct {
	lastAck []byte       // final ACK of an upload, sent again if the device repeats its last block
	c       *net.UDPConn // the server's socket: replies come from its port
	in      chan []byte  // packets from peer
	peer    *net.UDPAddr
	timeout time.Duration
	quit    chan struct{}
}

// stoppedByDevice: the device sent an ERROR packet.
type stoppedByDevice struct{ msg string }

func (e *stoppedByDevice) Error() string { return "the device stopped the transfer: " + e.msg }

// exchange sends pkt and waits for a reply accepted by want, resending on
// timeouts. Only packets from peer arrive here (the server sorts them).
func (t *tftpConn) exchange(pkt []byte, _ []byte, want func([]byte) (bool, error)) ([]byte, error) {
	for try := 0; try < tftpRetries; try++ {
		if pkt != nil {
			if _, err := t.c.WriteToUDP(pkt, t.peer); err != nil {
				return nil, err
			}
		}
		timer := time.NewTimer(t.timeout)
	wait:
		for {
			var p []byte
			select {
			case p = <-t.in:
			case <-timer.C:
				break wait // resend
			case <-t.quit:
				timer.Stop()
				return nil, errors.New("stopped")
			}
			if len(p) >= 4 && binary.BigEndian.Uint16(p) == opERROR {
				return nil, &stoppedByDevice{strings.TrimRight(string(p[4:]), "\x00")}
			}
			ok, err := want(p)
			if err != nil {
				return nil, err
			}
			if ok {
				return p, nil
			}
		}
	}
	return nil, errors.New("the device stopped answering")
}

func (t *tftpConn) send(path string, blk int, opts [][2]string, tick func(int64)) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	buf := make([]byte, maxBlock+4)
	isAck := func(block uint16) func([]byte) (bool, error) {
		return func(p []byte) (bool, error) {
			return len(p) >= 4 && binary.BigEndian.Uint16(p) == opACK && binary.BigEndian.Uint16(p[2:]) == block, nil
		}
	}
	if len(opts) > 0 {
		if _, err := t.exchange(oackPacket(opts), buf, isAck(0)); err != nil {
			return 0, err
		}
	}
	data := make([]byte, 4+blk)
	var sent int64
	block := uint16(1)
	last := time.Now()
	for {
		n, rerr := io.ReadFull(f, data[4:])
		if rerr != nil && rerr != io.ErrUnexpectedEOF && rerr != io.EOF {
			_, _ = t.c.WriteToUDP(errorPacket(0, "read error"), t.peer)
			return sent, rerr
		}
		binary.BigEndian.PutUint16(data, opDATA)
		binary.BigEndian.PutUint16(data[2:], block)
		if _, err := t.exchange(data[:4+n], buf, isAck(block)); err != nil {
			return sent, err
		}
		sent += int64(n)
		if time.Since(last) > 200*time.Millisecond {
			last = time.Now()
			tick(sent)
		}
		if n < blk {
			return sent, nil
		}
		block++ // rolls over to 0 after 65535, as most devices expect
	}
}

func (t *tftpConn) receive(path string, blk int, opts [][2]string, tick func(int64)) (int64, error) {
	tmp := path + ".part"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		_, _ = t.c.WriteToUDP(errorPacket(errAccess, "cannot write the file"), t.peer)
		return 0, err
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(tmp)
		}
	}()
	buf := make([]byte, maxBlock+4)
	var pkt []byte
	if len(opts) > 0 {
		pkt = oackPacket(opts)
	} else {
		pkt = ackPacket(0)
	}
	var got int64
	block := uint16(1)
	last := time.Now()
	for {
		p, err := t.exchange(pkt, buf, func(p []byte) (bool, error) {
			if len(p) < 4 || binary.BigEndian.Uint16(p) != opDATA {
				return false, nil
			}
			b := binary.BigEndian.Uint16(p[2:])
			if b == block-1 { // our ACK got lost: send it again
				_, _ = t.c.WriteToUDP(ackPacket(b), t.peer)
				return false, nil
			}
			return b == block, nil
		})
		if err != nil {
			return got, err
		}
		payload := p[4:]
		if _, err := f.Write(payload); err != nil {
			_, _ = t.c.WriteToUDP(errorPacket(errDiskFull, "disk full or write error"), t.peer)
			return got, err
		}
		got += int64(len(payload))
		if time.Since(last) > 200*time.Millisecond {
			last = time.Now()
			tick(got)
		}
		pkt = ackPacket(block)
		if len(payload) < blk {
			// The file is complete and in place before the device hears so.
			if err := f.Close(); err != nil {
				_, _ = t.c.WriteToUDP(errorPacket(errDiskFull, "write error"), t.peer)
				return got, err
			}
			_ = os.Remove(path)
			if err := os.Rename(tmp, path); err != nil {
				_, _ = t.c.WriteToUDP(errorPacket(errAccess, "cannot save the file"), t.peer)
				return got, err
			}
			_, _ = t.c.WriteToUDP(pkt, t.peer)
			ok = true
			t.lastAck = pkt
			return got, nil
		}
		block++
	}
}

// ---- client (the demo device and tests use it) -------------------------------

// TFTPGet downloads file from a TFTP server at addr (host:port).
func TFTPGet(addr, file string, w io.Writer, blksize int) (int64, error) {
	return tftpClient(addr, file, opRRQ, w, nil, 0, blksize)
}

// TFTPPut uploads size bytes from r as file.
func TFTPPut(addr, file string, r io.Reader, size int64, blksize int) (int64, error) {
	return tftpClient(addr, file, opWRQ, nil, r, size, blksize)
}

func tftpClient(addr, file string, op int, w io.Writer, r io.Reader, size int64, blksize int) (int64, error) {
	ra, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return 0, err
	}
	c, err := net.ListenUDP("udp", nil)
	if err != nil {
		return 0, err
	}
	defer c.Close()
	req := []byte{0, byte(op)}
	req = append(req, file...)
	req = append(req, 0)
	req = append(req, "octet\x00"...)
	blk := defaultBlock
	if blksize > 0 && blksize != defaultBlock {
		req = append(req, fmt.Sprintf("blksize\x00%d\x00", blksize)...)
	}
	if op == opWRQ {
		req = append(req, fmt.Sprintf("tsize\x00%d\x00", size)...)
	} else {
		req = append(req, "tsize\x000\x00"...)
	}
	buf := make([]byte, maxBlock+4)
	var peer *net.UDPAddr
	var pkt = req
	var total int64
	block := uint16(1)
	data := make([]byte, 4+maxBlock)
	sendingDone := false
	for {
		var p []byte
		for try := 0; ; try++ {
			if try == tftpRetries {
				return total, errors.New("the TFTP server stopped answering")
			}
			if peer == nil {
				_, err = c.WriteToUDP(pkt, ra)
			} else {
				_, err = c.WriteToUDP(pkt, peer)
			}
			if err != nil {
				return total, err
			}
			_ = c.SetReadDeadline(time.Now().Add(tftpTimeout))
			n, from, err := c.ReadFromUDP(buf)
			if err != nil {
				continue
			}
			if peer == nil {
				peer = from
			} else if from.Port != peer.Port {
				continue
			}
			p = buf[:n]
			break
		}
		if len(p) < 2 {
			continue
		}
		switch binary.BigEndian.Uint16(p) {
		case opERROR:
			return total, fmt.Errorf("TFTP error: %s", strings.TrimRight(string(p[4:]), "\x00"))
		case opOACK:
			req, _ := parseRequest(append([]byte{0, opRRQ, 'x', 0, 'o', 0}, p[2:]...))
			if req != nil {
				if v, err := strconv.Atoi(req.options["blksize"]); err == nil {
					blk = v
				}
			}
			if op == opRRQ {
				pkt = ackPacket(0)
				continue
			}
			block = 1
		case opACK:
			if op != opWRQ {
				continue
			}
			ack := binary.BigEndian.Uint16(p[2:])
			if ack != block-1 && !(ack == 0 && block == 1) {
				continue
			}
			if sendingDone && ack == block-1 {
				return total, nil
			}
		case opDATA:
			if op != opRRQ {
				continue
			}
			b := binary.BigEndian.Uint16(p[2:])
			if b != block {
				pkt = ackPacket(b)
				continue
			}
			if _, err := w.Write(p[4:]); err != nil {
				return total, err
			}
			total += int64(len(p) - 4)
			pkt = ackPacket(block)
			block++
			if len(p)-4 < blk {
				_, _ = c.WriteToUDP(pkt, peer)
				return total, nil
			}
			continue
		}
		if op == opWRQ {
			n, rerr := io.ReadFull(r, data[4:4+blk])
			if rerr != nil && rerr != io.EOF && rerr != io.ErrUnexpectedEOF {
				return total, rerr
			}
			binary.BigEndian.PutUint16(data, opDATA)
			binary.BigEndian.PutUint16(data[2:], block)
			pkt = append([]byte(nil), data[:4+n]...)
			total += int64(n)
			block++
			if n < blk {
				sendingDone = true
			}
		}
	}
}
