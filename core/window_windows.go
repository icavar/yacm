// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

//go:build windows

package core

import (
	"context"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32                       = windows.NewLazySystemDLL("user32.dll")
	procEnumWindows              = user32.NewProc("EnumWindows")
	procGetWindowThreadProcessId = user32.NewProc("GetWindowThreadProcessId")
	procGetClassNameW            = user32.NewProc("GetClassNameW")
	procGetWindowPlacement       = user32.NewProc("GetWindowPlacement")
	procSetWindowPlacement       = user32.NewProc("SetWindowPlacement")
	procMonitorFromWindow        = user32.NewProc("MonitorFromWindow")
	procGetMonitorInfoW          = user32.NewProc("GetMonitorInfoW")
	procGetWindowRect            = user32.NewProc("GetWindowRect")
	procSetWindowPos             = user32.NewProc("SetWindowPos")
	procShowWindow               = user32.NewProc("ShowWindow")
	procFlashWindowEx            = user32.NewProc("FlashWindowEx")
)

type winRect struct{ Left, Top, Right, Bottom int32 }

func (r winRect) w() int32 { return r.Right - r.Left }
func (r winRect) h() int32 { return r.Bottom - r.Top }

type winPoint struct{ X, Y int32 }

type windowPlacement struct {
	Length  uint32
	Flags   uint32
	ShowCmd uint32
	MinPos  winPoint
	MaxPos  winPoint
	Normal  winRect
}

type monitorInfo struct {
	Size    uint32
	Monitor winRect
	Work    winRect
	Flags   uint32
}

const (
	swHide                  = 0
	swShowNormal            = 1
	swShowMinimized         = 2
	swMaximize              = 3
	monitorDefaultToNearest = 2
	swpNoSize               = 0x0001
	swpNoZOrder             = 0x0004
	swpNoActivate           = 0x0010
)

// mainWindow finds the application window of this process by its class.
func mainWindow() windows.HWND {
	var found windows.HWND
	pid := uint32(os.Getpid())
	cb := syscall.NewCallback(func(hwnd windows.HWND, _ uintptr) uintptr {
		var wpid uint32
		procGetWindowThreadProcessId.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&wpid)))
		if wpid != pid {
			return 1
		}
		buf := make([]uint16, 64)
		n, _, _ := procGetClassNameW.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		if n > 0 && windows.UTF16ToString(buf[:n]) == windowClass {
			found = hwnd
			return 0
		}
		return 1
	})
	procEnumWindows.Call(cb, 0)
	return found
}

func workArea(hwnd windows.HWND) (winRect, bool) {
	mon, _, _ := procMonitorFromWindow.Call(uintptr(hwnd), monitorDefaultToNearest)
	if mon == 0 {
		return winRect{}, false
	}
	mi := monitorInfo{Size: uint32(unsafe.Sizeof(monitorInfo{}))}
	ok, _, _ := procGetMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&mi)))
	return mi.Work, ok != 0
}

// fitToWorkArea moves and, if needed, shrinks the window so that its whole
// frame, title bar included, is inside the monitor's work area (the screen
// minus the taskbar). A window that would not fit is maximised.
func fitToWorkArea(hwnd windows.HWND) {
	work, ok := workArea(hwnd)
	if !ok {
		return
	}
	var r winRect
	if ok, _, _ := procGetWindowRect.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&r))); ok == 0 {
		return
	}
	if r.w() > work.w() || r.h() > work.h() {
		procShowWindow.Call(uintptr(hwnd), swMaximize)
		return
	}
	x, y := r.Left, r.Top
	if x < work.Left {
		x = work.Left
	}
	if y < work.Top {
		y = work.Top
	}
	if x+r.w() > work.Right {
		x = work.Right - r.w()
	}
	if y+r.h() > work.Bottom {
		y = work.Bottom - r.h()
	}
	if x != r.Left || y != r.Top {
		procSetWindowPos.Call(uintptr(hwnd), 0, uintptr(x), uintptr(y), 0, 0, swpNoSize|swpNoZOrder|swpNoActivate)
	}
}

func placeWindow(_ context.Context, ws *windowState) {
	hwnd := mainWindow()
	if hwnd == 0 {
		return
	}
	maximised := false
	if ws != nil && len(ws.Placement) == int(unsafe.Sizeof(windowPlacement{})) {
		var wp windowPlacement
		copy(unsafe.Slice((*byte)(unsafe.Pointer(&wp)), unsafe.Sizeof(wp)), ws.Placement)
		wp.Length = uint32(unsafe.Sizeof(wp))
		maximised = wp.ShowCmd == swMaximize
		// Restore hidden; the window is shown by the caller after placing.
		wp.ShowCmd = swHide
		procSetWindowPlacement.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&wp)))
	}
	fitToWorkArea(hwnd)
	if maximised {
		procShowWindow.Call(uintptr(hwnd), swMaximize)
	}
}

func currentWindowState(_ context.Context) *windowState {
	hwnd := mainWindow()
	if hwnd == 0 {
		return nil
	}
	wp := windowPlacement{Length: uint32(unsafe.Sizeof(windowPlacement{}))}
	if ok, _, _ := procGetWindowPlacement.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&wp))); ok == 0 {
		return nil
	}
	if wp.ShowCmd == swShowMinimized {
		wp.ShowCmd = swShowNormal
	}
	raw := make([]byte, unsafe.Sizeof(wp))
	copy(raw, unsafe.Slice((*byte)(unsafe.Pointer(&wp)), unsafe.Sizeof(wp)))
	return &windowState{Placement: raw, Maximised: wp.ShowCmd == swMaximize}
}

type flashWInfo struct {
	Size    uint32
	Hwnd    windows.HWND
	Flags   uint32
	Count   uint32
	Timeout uint32
}

// requestAttention flashes the taskbar button until the window is used
// (output alerts while YACM is in the background).
func requestAttention(_ context.Context) {
	hwnd := mainWindow()
	if hwnd == 0 {
		return
	}
	const flashTray, flashTimerNoFG = 0x2, 0xC
	fi := flashWInfo{Hwnd: hwnd, Flags: flashTray | flashTimerNoFG}
	fi.Size = uint32(unsafe.Sizeof(fi))
	procFlashWindowEx.Call(uintptr(unsafe.Pointer(&fi)))
}
