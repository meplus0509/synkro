//go:build windows

package drives

import (
	"fmt"

	"golang.org/x/sys/windows"
)

func list() ([]Drive, error) {
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return nil, err
	}
	var out []Drive
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		root := string(rune('A'+i)) + `:\`
		p, _ := windows.UTF16PtrFromString(root)
		typ := windows.GetDriveType(p)
		if typ != windows.DRIVE_REMOVABLE && typ != windows.DRIVE_FIXED {
			continue // lecteurs réseau, CD/DVD, RAM : ignorés
		}
		label := make([]uint16, windows.MAX_PATH+1)
		fsName := make([]uint16, windows.MAX_PATH+1)
		var serial, maxLen, flags uint32
		// échoue si aucun média n'est inséré (lecteur de carte vide…) : on l'ignore
		if err := windows.GetVolumeInformation(p, &label[0], uint32(len(label)), &serial, &maxLen, &flags, &fsName[0], uint32(len(fsName))); err != nil {
			continue
		}
		d := Drive{
			Root:      root,
			Label:     windows.UTF16ToString(label),
			Serial:    fmt.Sprintf("%04X-%04X", serial>>16, serial&0xFFFF),
			Removable: typ == windows.DRIVE_REMOVABLE,
			FS:        windows.UTF16ToString(fsName),
		}
		var free, total, totalFree uint64
		if windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree) == nil {
			d.Total, d.Free = total, free
		}
		out = append(out, d)
	}
	return out, nil
}
