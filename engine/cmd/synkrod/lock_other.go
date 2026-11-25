//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"syscall"
)

var lockFile *os.File

// lockDataDir réserve le dossier de données pour cette instance (verrou
// libéré par le système à l'arrêt). false = déjà utilisé.
func lockDataDir(dir string) bool {
	f, err := os.OpenFile(filepath.Join(dir, ".synkro.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false
	}
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		f.Close()
		return false
	}
	lockFile = f
	return true
}

func focusExisting() bool { return false }
