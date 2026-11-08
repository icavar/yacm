// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package main

import (
	"fmt"
	"log"
	"net"
	"sync"
	"time"
)

// The pretend router can "reload": every session drops and the ports stay
// closed for a while, as on a real device, so YACM's reload watch can be
// tried.

var reloadFor = 20 * time.Second

type device struct {
	mu        sync.Mutex
	conns     map[net.Conn]struct{}
	listeners map[net.Listener]struct{}
	upAt      time.Time
	upCond    *sync.Cond
}

var dev = func() *device {
	d := &device{conns: map[net.Conn]struct{}{}, listeners: map[net.Listener]struct{}{}}
	d.upCond = sync.NewCond(&d.mu)
	return d
}()

func (d *device) track(c net.Conn) {
	d.mu.Lock()
	d.conns[c] = struct{}{}
	d.mu.Unlock()
}

func (d *device) untrack(c net.Conn) {
	d.mu.Lock()
	delete(d.conns, c)
	d.mu.Unlock()
}

// reload drops every connection and closes the ports for reloadFor.
func (d *device) reload() {
	d.mu.Lock()
	if time.Now().Before(d.upAt) {
		d.mu.Unlock()
		return
	}
	d.upAt = time.Now().Add(reloadFor)
	for c := range d.conns {
		c.Close()
	}
	for l := range d.listeners {
		l.Close()
	}
	d.mu.Unlock()
	log.Printf("reloading: ports closed for %s", reloadFor)
	time.Sleep(reloadFor)
	d.mu.Lock()
	d.upCond.Broadcast()
	d.mu.Unlock()
	log.Printf("reload done: ports open again")
}

// serve listens on addr and hands every connection to handle; after a
// reload it listens again.
func serve(addr string, handle func(net.Conn)) {
	for {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			fail(addr, err)
		}
		dev.mu.Lock()
		dev.listeners[ln] = struct{}{}
		dev.mu.Unlock()
		for {
			nc, err := ln.Accept()
			if err != nil {
				break
			}
			dev.track(nc)
			go func() {
				defer dev.untrack(nc)
				handle(nc)
			}()
		}
		dev.mu.Lock()
		delete(dev.listeners, ln)
		for time.Now().Before(dev.upAt) {
			dev.upCond.Wait()
		}
		dev.mu.Unlock()
	}
}

func reloadMessage(host string) string {
	return fmt.Sprintf("\r\n%%SYS-5-RELOAD: Reload requested by admin on vty0. Reload Reason: Reload Command.\r\n%s is reloading, back in about %d seconds.\r\n", host, int(reloadFor/time.Second))
}
