//go:build windows

package app

import (
	"context"
	"unsafe"

	"golang.org/x/sys/windows"
)

// watchDir surveille un dossier et ses sous-dossiers (ReadDirectoryChangesW).
func watchDir(ctx context.Context, dir string, onChange func(name string)) error {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(p, windows.FILE_LIST_DIRECTORY,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { windows.CancelIoEx(h, nil) })
	go func() {
		defer windows.CloseHandle(h)
		defer stop()
		buf := make([]byte, 64*1024)
		filter := uint32(windows.FILE_NOTIFY_CHANGE_FILE_NAME | windows.FILE_NOTIFY_CHANGE_DIR_NAME |
			windows.FILE_NOTIFY_CHANGE_SIZE | windows.FILE_NOTIFY_CHANGE_LAST_WRITE)
		for ctx.Err() == nil {
			var n uint32
			if err := windows.ReadDirectoryChanges(h, &buf[0], uint32(len(buf)), true, filter, &n, nil, 0); err != nil {
				return // dossier disparu (clé débranchée) ou arrêt demandé
			}
			if n == 0 { // trop de changements d'un coup : le tampon a débordé
				onChange("")
				continue
			}
			for off := uint32(0); off < n; {
				info := (*windows.FileNotifyInformation)(unsafe.Pointer(&buf[off]))
				name := windows.UTF16ToString(unsafe.Slice(&info.FileName, info.FileNameLength/2))
				onChange(name)
				if info.NextEntryOffset == 0 {
					break
				}
				off += info.NextEntryOffset
			}
		}
	}()
	return nil
}
