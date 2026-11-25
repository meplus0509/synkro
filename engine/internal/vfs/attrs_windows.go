//go:build windows

package vfs

import (
	"io/fs"
	"syscall"
)

// fileAttrs lit les attributs Windows « caché » et « système ».
func fileAttrs(info fs.FileInfo) (hidden, system bool) {
	if d, ok := info.Sys().(*syscall.Win32FileAttributeData); ok {
		hidden = d.FileAttributes&syscall.FILE_ATTRIBUTE_HIDDEN != 0
		system = d.FileAttributes&syscall.FILE_ATTRIBUTE_SYSTEM != 0
	}
	return
}

// clearReadOnly enlève l'attribut « lecture seule » ; true si c'était le cas.
func clearReadOnly(p string) bool {
	name, err := syscall.UTF16PtrFromString(p)
	if err != nil {
		return false
	}
	a, err := syscall.GetFileAttributes(name)
	if err != nil || a&syscall.FILE_ATTRIBUTE_READONLY == 0 {
		return false
	}
	return syscall.SetFileAttributes(name, a&^syscall.FILE_ATTRIBUTE_READONLY) == nil
}
