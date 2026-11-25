//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"

	"synkro/internal/app"
)

func init() {
	app.CreateShortcut = createShortcut
	app.ShortcutPath = shortcutPath
}

// shortcutPath : chemin de « Synkro.lnk » sur le bureau ou dans le menu Démarrer.
func shortcutPath(where string) (string, error) {
	id := windows.FOLDERID_Desktop
	if where == "startmenu" {
		id = windows.FOLDERID_Programs
	}
	dir, err := windows.KnownFolderPath(id, windows.KF_FLAG_DEFAULT)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "Synkro.lnk"), nil
}

// createShortcut crée « Synkro.lnk » sur le bureau (dossier Bureau réel, même
// redirigé vers OneDrive) ou dans le menu Démarrer (where = "startmenu").
func createShortcut(where string) (string, error) {
	folder := "Desktop"
	if where == "startmenu" {
		folder = "Programs" // %APPDATA%\Microsoft\Windows\Start Menu\Programs
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	// les chemins passent par l'environnement : aucun souci de guillemets
	script := `$d=[Environment]::GetFolderPath('` + folder + `');$p=Join-Path $d 'Synkro.lnk';` +
		`$s=(New-Object -ComObject WScript.Shell).CreateShortcut($p);$s.TargetPath=$env:SYNKRO_EXE;` +
		`$s.WorkingDirectory=Split-Path $env:SYNKRO_EXE;$s.IconLocation=$env:SYNKRO_EXE+',0';$s.Description='Synkro';$s.Save();Write-Output $p`
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	cmd.Env = append(os.Environ(), "SYNKRO_EXE="+exe)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000 /* CREATE_NO_WINDOW */}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("création du raccourci : %v %s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}
