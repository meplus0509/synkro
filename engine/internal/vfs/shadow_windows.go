//go:build windows

package vfs

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// isLocked : fichier ouvert par un autre programme qui en interdit la lecture.
func isLocked(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}

// splitVolume : « C:\Users\x » → (« C: », « \Users\x »). Disques locaux seulement.
func splitVolume(abs string) (string, string, error) {
	vol := filepath.VolumeName(abs)
	if len(vol) != 2 || vol[1] != ':' {
		return "", "", fmt.Errorf("instantané impossible pour %s (disque local seulement)", abs)
	}
	rest := abs[2:]
	if rest == "" {
		rest = `\`
	}
	return strings.ToUpper(vol), rest, nil
}

func elevated() bool { return windows.GetCurrentProcessToken().IsElevated() }

func powershell(script string, env ...string) (string, error) {
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	cmd.Env = append(os.Environ(), env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	out, err := cmd.CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil {
		return "", fmt.Errorf("%v %s", err, s)
	}
	return s, nil
}

// createShadow crée un instantané du disque (ex. « C: ») et renvoie son
// identifiant et son chemin (\\?\GLOBALROOT\Device\HarddiskVolumeShadowCopyN).
func createShadow(vol string) (string, string, error) {
	if !elevated() {
		return "", "", errors.New("Synkro doit être lancé en tant qu'administrateur pour utiliser Volume Shadow Copy")
	}
	out, err := powershell(`$r=(Get-WmiObject -List Win32_ShadowCopy).Create($env:SYNKRO_VOL+'\','ClientAccessible');`+
		`if($r.ReturnValue -ne 0){Write-Output ('ERREUR '+$r.ReturnValue);exit 1};`+
		`$s=Get-WmiObject Win32_ShadowCopy|Where-Object{$_.ID -eq $r.ShadowID};Write-Output ($s.ID+'|'+$s.DeviceObject)`, "SYNKRO_VOL="+vol)
	if err != nil {
		return "", "", fmt.Errorf("création de l'instantané : %v", err)
	}
	line := out[strings.LastIndex(out, "\n")+1:]
	id, dev, ok := strings.Cut(strings.TrimSpace(line), "|")
	if !ok || dev == "" {
		return "", "", fmt.Errorf("création de l'instantané : réponse inattendue %q", out)
	}
	return id, dev, nil
}

func deleteShadow(id string) error {
	_, err := powershell(`Get-WmiObject Win32_ShadowCopy|Where-Object{$_.ID -eq $env:SYNKRO_ID}|ForEach-Object{$_.Delete()}`, "SYNKRO_ID="+id)
	return err
}

// Elevated : lancé en administrateur ?
func Elevated() bool { return elevated() }
