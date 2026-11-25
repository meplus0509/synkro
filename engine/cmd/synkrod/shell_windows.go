//go:build windows

package main

// Intégration au bureau Windows : icône près de l'horloge (zone de notification),
// fermeture = réduire près de l'horloge, icône dans la barre des tâches ou non,
// synchronisation à la fermeture de session.

import (
	"log"
	"sync"
	"syscall"
	"time"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"

	"synkro/internal/app"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	pSetWindowLongPtr  = user32.NewProc("SetWindowLongPtrW")
	pGetWindowLongPtr  = user32.NewProc("GetWindowLongPtrW")
	pCallWindowProc    = user32.NewProc("CallWindowProcW")
	pShowWindow        = user32.NewProc("ShowWindow")
	pIsWindowVisible   = user32.NewProc("IsWindowVisible")
	pIsIconic          = user32.NewProc("IsIconic")
	pSetForeground     = user32.NewProc("SetForegroundWindow")
	pCreatePopupMenu   = user32.NewProc("CreatePopupMenu")
	pAppendMenu        = user32.NewProc("AppendMenuW")
	pTrackPopupMenu    = user32.NewProc("TrackPopupMenu")
	pDestroyMenu       = user32.NewProc("DestroyMenu")
	pGetCursorPos      = user32.NewProc("GetCursorPos")
	pLoadImage         = user32.NewProc("LoadImageW")
	pCreateWindowEx    = user32.NewProc("CreateWindowExW")
	pDestroyWindow     = user32.NewProc("DestroyWindow")
	pPostMessage       = user32.NewProc("PostMessageW")
	pBlockReasonCreate = user32.NewProc("ShutdownBlockReasonCreate")
	pBlockReasonDestr  = user32.NewProc("ShutdownBlockReasonDestroy")
	pShellNotifyIcon   = shell32.NewProc("Shell_NotifyIconW")
	pGetModuleHandle   = kernel32.NewProc("GetModuleHandleW")
)

const (
	gwlpWndProc    = ^uintptr(3) // -4
	gwlpHwndParent = ^uintptr(7) // -8
	wmClose        = 0x0010
	wmQueryEnd     = 0x0011
	wmEndSession   = 0x0016
	wmSysCommand   = 0x0112
	scMinimize     = 0xF020
	wmTray         = 0x8000 + 1 // WM_APP+1
	wmLButtonUp    = 0x0202
	wmLButtonDbl   = 0x0203
	wmRButtonUp    = 0x0205
	nimAdd         = 0
	nimModify      = 1
	nimDelete      = 2
	nifMessage     = 1
	nifIcon        = 2
	nifTip         = 4
	nifInfo        = 0x10
	swHide         = 0
	swShow         = 5
	swRestore      = 9
	menuOpen       = 1
	menuQuit       = 2
)

type notifyIconData struct {
	CbSize           uint32
	HWnd             uintptr
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            uintptr
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         [16]byte
	HBalloonIcon     uintptr
}

type winShell struct {
	w       webview2.WebView
	hwnd    uintptr
	store   *app.Store
	runner  *app.Runner
	oldProc uintptr
	owner   uintptr // fenêtre cachée « propriétaire » : retire Synkro de la barre des tâches

	mu          sync.Mutex
	taskbar     bool
	tray        bool
	closeToTray bool
	trayShown   bool
	quitting    bool
	hintShown   bool
	logoffBusy  bool
}

var theShell *winShell // une seule fenêtre

func newWinShell(w webview2.WebView, store *app.Store, runner *app.Runner) *winShell {
	s := &winShell{w: w, hwnd: uintptr(w.Window()), store: store, runner: runner, taskbar: true}
	theShell = s
	s.oldProc, _, _ = pGetWindowLongPtr.Call(s.hwnd, gwlpWndProc)
	pSetWindowLongPtr.Call(s.hwnd, gwlpWndProc, syscall.NewCallback(shellProc))
	s.apply(store.Settings())
	app.OnSettings = func(m map[string]string) { w.Dispatch(func() { s.apply(m) }) }
	return s
}

func (s *winShell) close() {
	app.OnSettings = nil
	s.setTray(false)
	if s.owner != 0 {
		pDestroyWindow.Call(s.owner)
	}
}

// apply met la fenêtre en accord avec les réglages (sur le fil de la fenêtre).
func (s *winShell) apply(m map[string]string) {
	s.mu.Lock()
	s.taskbar = m["taskbar"] != "off"
	s.closeToTray = m["closeToTray"] == "on"
	// sans icône dans la barre des tâches, l'icône près de l'horloge reste le seul accès
	s.tray = m["tray"] == "on" || s.closeToTray || !s.taskbar
	taskbar, tray := s.taskbar, s.tray
	s.mu.Unlock()
	s.setTray(tray)
	s.setTaskbar(taskbar)
}

func (s *winShell) setTaskbar(on bool) {
	if !on && s.owner == 0 {
		cls, _ := syscall.UTF16PtrFromString("STATIC")
		s.owner, _, _ = pCreateWindowEx.Call(0, uintptr(unsafe.Pointer(cls)), 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
	}
	var owner uintptr
	if !on {
		owner = s.owner
	}
	cur, _, _ := pGetWindowLongPtr.Call(s.hwnd, gwlpHwndParent)
	if cur == owner {
		return
	}
	// une fenêtre « possédée » n'apparaît pas dans la barre des tâches ; il faut
	// la cacher puis la remontrer pour que Windows en tienne compte
	vis, _, _ := pIsWindowVisible.Call(s.hwnd)
	if vis != 0 {
		pShowWindow.Call(s.hwnd, swHide)
	}
	pSetWindowLongPtr.Call(s.hwnd, gwlpHwndParent, owner)
	if vis != 0 {
		pShowWindow.Call(s.hwnd, swShow)
	}
}

func (s *winShell) nid() *notifyIconData {
	n := &notifyIconData{HWnd: s.hwnd, UID: 1}
	n.CbSize = uint32(unsafe.Sizeof(*n))
	return n
}

func (s *winShell) setTray(on bool) {
	if on == s.trayShown {
		return
	}
	n := s.nid()
	if !on {
		pShellNotifyIcon.Call(nimDelete, uintptr(unsafe.Pointer(n)))
		s.trayShown = false
		return
	}
	mod, _, _ := pGetModuleHandle.Call(0)
	n.HIcon, _, _ = pLoadImage.Call(mod, 1 /* icône n° 1 */, 1 /* IMAGE_ICON */, 16, 16, 0)
	n.UFlags = nifMessage | nifIcon | nifTip
	n.UCallbackMessage = wmTray
	copy(n.SzTip[:], syscall.StringToUTF16("Synkro"))
	r, _, _ := pShellNotifyIcon.Call(nimAdd, uintptr(unsafe.Pointer(n)))
	s.trayShown = r != 0
	if !s.trayShown {
		log.Printf("fenêtre : icône près de l'horloge impossible")
	}
}

// bulle d'information près de l'horloge (une fois par session)
func (s *winShell) balloon(title, text string) {
	if !s.trayShown || s.hintShown {
		return
	}
	s.hintShown = true
	n := s.nid()
	n.UFlags = nifInfo
	copy(n.SzInfoTitle[:63], syscall.StringToUTF16(title))
	copy(n.SzInfo[:255], syscall.StringToUTF16(text))
	n.DwInfoFlags = 1 // NIIF_INFO
	pShellNotifyIcon.Call(nimModify, uintptr(unsafe.Pointer(n)))
}

func (s *winShell) showWindow() {
	pShowWindow.Call(s.hwnd, swShow)
	if ic, _, _ := pIsIconic.Call(s.hwnd); ic != 0 {
		pShowWindow.Call(s.hwnd, swRestore)
	}
	pSetForeground.Call(s.hwnd)
}

func (s *winShell) hideToTray() {
	pShowWindow.Call(s.hwnd, swHide)
	s.balloon("Synkro continue de tourner", "Synkro est réduit ici. Clic droit sur l'icône → Quitter pour l'arrêter.")
}

func (s *winShell) quit() {
	s.mu.Lock()
	s.quitting = true
	s.mu.Unlock()
	pPostMessage.Call(s.hwnd, wmClose, 0, 0)
}

func (s *winShell) trayMenu() {
	menu, _, _ := pCreatePopupMenu.Call()
	defer pDestroyMenu.Call(menu)
	open, _ := syscall.UTF16PtrFromString("Ouvrir Synkro")
	quit, _ := syscall.UTF16PtrFromString("Quitter")
	pAppendMenu.Call(menu, 0, menuOpen, uintptr(unsafe.Pointer(open)))
	pAppendMenu.Call(menu, 0x800 /* MF_SEPARATOR */, 0, 0)
	pAppendMenu.Call(menu, 0, menuQuit, uintptr(unsafe.Pointer(quit)))
	var pt struct{ X, Y int32 }
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	pSetForeground.Call(s.hwnd)
	cmd, _, _ := pTrackPopupMenu.Call(menu, 0x0100|0x0002 /* TPM_RETURNCMD|TPM_RIGHTBUTTON */, uintptr(pt.X), uintptr(pt.Y), 0, s.hwnd, 0)
	switch cmd {
	case menuOpen:
		s.showWindow()
	case menuQuit:
		s.quit()
	}
}

func shellProc(hwnd, msg, wp, lp uintptr) uintptr {
	s := theShell
	switch msg {
	case wmTray:
		switch lp & 0xFFFF {
		case wmLButtonUp, wmLButtonDbl:
			s.showWindow()
		case wmRButtonUp:
			s.trayMenu()
		}
		return 0
	case wmClose:
		s.mu.Lock()
		hide := s.closeToTray && !s.quitting && s.trayShown
		s.mu.Unlock()
		if hide {
			s.hideToTray()
			return 0
		}
	case wmSysCommand:
		s.mu.Lock()
		hide := !s.taskbar && s.trayShown
		s.mu.Unlock()
		if wp&0xFFF0 == scMinimize && hide { // pas de bouton dans la barre des tâches : réduire près de l'horloge
			s.hideToTray()
			return 0
		}
	case wmQueryEnd:
		// fermeture de session : on lance les tâches prévues pour ce moment et on
		// demande à Windows d'attendre la fin (message affiché à l'utilisateur)
		if n := s.runner.StartLogoff(); n > 0 {
			log.Printf("fermeture de session : %d tâche(s) lancée(s)", n)
			s.logoffBusy = true
			why, _ := syscall.UTF16PtrFromString("Synkro synchronise vos dossiers avant la fermeture de session…")
			pBlockReasonCreate.Call(hwnd, uintptr(unsafe.Pointer(why)))
		}
		return 1
	case wmEndSession:
		if wp != 0 && s.logoffBusy {
			s.runner.WaitIdle(15 * time.Minute)
			pBlockReasonDestr.Call(hwnd)
			log.Printf("fermeture de session : synchronisations terminées")
		}
		return 0
	}
	r, _, _ := pCallWindowProc.Call(s.oldProc, hwnd, msg, wp, lp)
	return r
}
