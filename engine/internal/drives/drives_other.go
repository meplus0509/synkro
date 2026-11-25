//go:build !windows

package drives

import (
	"os"
	"os/user"
	"path/filepath"
	"syscall"
)

// Hors Windows, les clés sont montées dans /media/<utilisateur>, /run/media/<utilisateur>
// ou /Volumes (macOS). Le numéro de série n'est pas lu : la clé est reconnue par son nom.
func list() ([]Drive, error) {
	var bases []string
	if u, err := user.Current(); err == nil {
		bases = append(bases, filepath.Join("/media", u.Username), filepath.Join("/run/media", u.Username))
	}
	bases = append(bases, "/Volumes")
	var out []Drive
	for _, b := range bases {
		ents, err := os.ReadDir(b)
		if err != nil {
			continue
		}
		for _, e := range ents {
			root := filepath.Join(b, e.Name())
			if st, err := os.Stat(root); err != nil || !st.IsDir() {
				continue
			}
			d := Drive{Root: root, Label: e.Name(), Removable: b != "/Volumes" || e.Name() != "Macintosh HD"}
			var fs syscall.Statfs_t
			if syscall.Statfs(root, &fs) == nil {
				d.Total = uint64(fs.Blocks) * uint64(fs.Bsize)
				d.Free = uint64(fs.Bavail) * uint64(fs.Bsize)
			}
			out = append(out, d)
		}
	}
	return out, nil
}
