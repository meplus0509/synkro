//go:build windows

package vfs

import (
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func getMeta(p string) (Meta, error) {
	var d windows.Win32FileAttributeData
	name, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return Meta{}, err
	}
	if err := windows.GetFileAttributesEx(name, windows.GetFileExInfoStandard, (*byte)(unsafe.Pointer(&d))); err != nil {
		return Meta{}, err
	}
	return Meta{Created: time.Unix(0, d.CreationTime.Nanoseconds()), Attrs: d.FileAttributes}, nil
}

// attributs recopiés : lecture seule, caché, système, archive
const copiedAttrs = windows.FILE_ATTRIBUTE_READONLY | windows.FILE_ATTRIBUTE_HIDDEN | windows.FILE_ATTRIBUTE_SYSTEM | windows.FILE_ATTRIBUTE_ARCHIVE

func setMeta(p string, m Meta, created, attrs bool) error {
	name, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return err
	}
	if created && !m.Created.IsZero() {
		// lecture seule : à lever le temps de changer la date
		if a, err := windows.GetFileAttributes(name); err == nil && a&windows.FILE_ATTRIBUTE_READONLY != 0 {
			windows.SetFileAttributes(name, a&^windows.FILE_ATTRIBUTE_READONLY)
			defer windows.SetFileAttributes(name, a)
		}
		h, err := windows.CreateFile(name, windows.FILE_WRITE_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
			nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
		if err != nil {
			return err
		}
		ft := windows.NsecToFiletime(m.Created.UnixNano())
		err = windows.SetFileTime(h, &ft, nil, nil)
		windows.CloseHandle(h)
		if err != nil {
			return err
		}
	}
	if attrs {
		cur, err := windows.GetFileAttributes(name)
		if err != nil {
			return err
		}
		n := cur&^copiedAttrs | m.Attrs&copiedAttrs
		if n != cur {
			return windows.SetFileAttributes(name, n)
		}
	}
	return nil
}

var _ = syscall.Errno(0)
