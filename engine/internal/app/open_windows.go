//go:build windows

package app

import (
	"os/exec"
	"syscall"
)

// openLocal ouvre un fichier avec son application (ou le montre dans l'explorateur).
func openLocal(p string, reveal bool) error {
	var cmd *exec.Cmd
	if reveal {
		cmd = exec.Command("explorer.exe")
		// explorer attend « /select,"chemin" » tel quel
		cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer.exe /select,"` + p + `"`}
	} else {
		cmd = exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", p)
	}
	return cmd.Start()
}
