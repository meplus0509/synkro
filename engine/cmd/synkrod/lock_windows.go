//go:build windows

package main

import (
	"path/filepath"
	"syscall"
	"unsafe"
)

// lockDataDir réserve le dossier de données pour cette instance : un fichier
// ouvert sans partage, gardé ouvert tant que Synkro tourne (Windows le libère
// tout seul si Synkro s'arrête, même brutalement). false = déjà utilisé.
func lockDataDir(dir string) bool {
	p, _ := syscall.UTF16PtrFromString(filepath.Join(dir, ".synkro.lock"))
	h, err := syscall.CreateFile(p, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0 /* aucun partage */, nil,
		syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_HIDDEN, 0)
	if err != nil {
		return false
	}
	lockHandle = h // gardé ouvert jusqu'à l'arrêt
	return true
}

var lockHandle syscall.Handle

// focusExisting met au premier plan la fenêtre Synkro déjà ouverte.
func focusExisting() bool {
	u := syscall.NewLazyDLL("user32.dll")
	t, _ := syscall.UTF16PtrFromString("Synkro")
	hwnd, _, _ := u.NewProc("FindWindowW").Call(0, uintptr(unsafe.Pointer(t)))
	if hwnd == 0 {
		return false
	}
	u.NewProc("ShowWindow").Call(hwnd, 9 /* SW_RESTORE */)
	u.NewProc("SetForegroundWindow").Call(hwnd)
	return true
}
