//go:build !windows

package app

import (
	"os/exec"
	"path/filepath"
	"runtime"
)

func openLocal(p string, reveal bool) error {
	if reveal {
		p = filepath.Dir(p)
	}
	if runtime.GOOS == "darwin" {
		return exec.Command("open", p).Start()
	}
	return exec.Command("xdg-open", p).Start()
}
