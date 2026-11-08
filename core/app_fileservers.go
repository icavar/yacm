// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package core

import (
	"errors"
	"net"
	"path/filepath"

	"github.com/icavar/yacm/internal/applog"
	"github.com/icavar/yacm/internal/fileserver"
	"github.com/icavar/yacm/internal/store"
)

// ---- file servers (TFTP, HTTP, SCP) ---------------------------------------------

func (a *App) fileServers() *fileserver.Manager {
	a.fsrvOnce.Do(func() {
		key := filepath.Join(filepath.Dir(a.store.Path()), "fileserver_host_key")
		a.fsrv = fileserver.NewManager(key, func(ev fileserver.Event) {
			if ev.State == "done" || ev.State == "error" {
				dir := "sent"
				if ev.Upload {
					dir = "received"
				}
				applog.Infof("file server %s: %s %s %s from %s (%d bytes) %s", ev.Kind, ev.File, dir, ev.State, ev.Remote, ev.Done, ev.Error)
			}
			a.emit("fileserver:event", ev)
		})
		a.fsrv.Stopped = func(kind string) {
			applog.Infof("file server %s switched off: no transfers for a while", kind)
			a.emit("fileserver:idle", kind)
			a.emit("fileserver:status", a.fsrv.Statuses())
		}
	})
	return a.fsrv
}

// FileServerStart starts (or restarts with new settings) a TFTP, HTTP or
// SCP server for the devices.
func (a *App) FileServerStart(cfg fileserver.Config) (fileserver.Status, error) {
	st, err := a.fileServers().Start(cfg)
	if err == nil {
		applog.Infof("file server %s started on port %d, folder %s, uploads %v", st.Kind, st.Port, st.Folder, st.Upload)
		a.emit("fileserver:status", a.fileServers().Statuses())
	}
	return st, err
}

// FileServerAllowPort lets YACM listen on a port below 1024 without running
// as administrator (Linux: the system asks for the password once).
func (a *App) FileServerAllowPort(port int) error {
	err := fileserver.AllowPort(port)
	if err != nil {
		applog.Warnf("allow port %d: %v", port, err)
	} else {
		applog.Infof("ports from %d allowed without administrator rights (%s)", port, fileserver.LowPortsConfName())
	}
	return err
}

// FileServerStop stops a server.
func (a *App) FileServerStop(kind string) {
	a.fileServers().Stop(kind)
	applog.Infof("file server %s stopped", kind)
	a.emit("fileserver:status", a.fileServers().Statuses())
}

// FileServerStatuses lists the servers and whether they run.
func (a *App) FileServerStatuses() []fileserver.Status { return a.fileServers().Statuses() }

// LocalAddresses lists the IP addresses of this computer.
func (a *App) LocalAddresses() []fileserver.LocalAddr { return fileserver.LocalAddresses() }

// DeviceRoute tells which address of this computer a session's device
// reaches, or that it sits behind jump hosts.
type DeviceRoute struct {
	Address string `json:"address"` // this computer's address toward the device ("" unknown)
	Jump    string `json:"jump"`    // the last jump host, when there are jump hosts
	Host    string `json:"host"`
}

// RouteToDevice works out the address a device copies from.
func (a *App) RouteToDevice(nodeID string) (DeviceRoute, error) {
	n, err := a.store.Get(nodeID)
	if err != nil {
		return DeviceRoute{}, err
	}
	if n.Session == nil {
		return DeviceRoute{}, errors.New("not a session")
	}
	r := DeviceRoute{Host: n.Session.Host}
	if eff, err := a.store.Effective(n); err == nil && len(eff.Hops) > 0 {
		j := eff.Hops[len(eff.Hops)-1]
		r.Jump = j.Host
		if j.Name != "" {
			r.Jump = j.Name + " (" + j.Host + ")"
		}
		return r, nil
	}
	if n.Session.Protocol == store.ProtoSerial {
		return r, nil
	}
	if ip := net.ParseIP(n.Session.Host); ip == nil || !ip.IsUnspecified() {
		r.Address = fileserver.AddressToward(n.Session.Host)
	}
	return r, nil
}
