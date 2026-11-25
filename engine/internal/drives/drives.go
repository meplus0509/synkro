// Package drives liste les lecteurs branchés (disques, clés USB) et retrouve une
// clé par son numéro de série ou son nom, quelle que soit la lettre qu'elle a reçue.
package drives

import (
	"fmt"
	"strings"
)

// Drive est un lecteur monté.
type Drive struct {
	Root      string `json:"root"`      // ex : "F:\" ou "/media/moi/CLE"
	Label     string `json:"label"`     // nom du volume, ex : "xxx"
	Serial    string `json:"serial"`    // numéro de série du volume, ex : "1A2B-3C4D" ("" si inconnu)
	Removable bool   `json:"removable"` // clé USB, carte SD…
	FS        string `json:"fs"`        // système de fichiers (NTFS, FAT32, exFAT…)
	Total     uint64 `json:"total"`
	Free      uint64 `json:"free"`
}

// Volume identifie une clé ou un disque, indépendamment de sa lettre.
type Volume struct {
	Serial string `json:"serial"`
	Label  string `json:"label"`
}

func (v Volume) String() string {
	switch {
	case v.Label != "" && v.Serial != "":
		return fmt.Sprintf("« %s » (n° %s)", v.Label, v.Serial)
	case v.Label != "":
		return "« " + v.Label + " »"
	default:
		return "n° " + v.Serial
	}
}

// List est remplaçable dans les tests.
var List = list

// Find cherche le lecteur correspondant au volume : d'abord par numéro de série
// (unique pour chaque clé), sinon par nom de volume.
func Find(v Volume) (Drive, error) {
	ds, err := List()
	if err != nil {
		return Drive{}, err
	}
	if v.Serial != "" {
		for _, d := range ds {
			if strings.EqualFold(d.Serial, v.Serial) {
				return d, nil
			}
		}
		// Le numéro ne correspond à aucun lecteur : on ne se rabat pas sur le nom,
		// pour ne jamais synchroniser vers une autre clé qui porterait le même nom.
		return Drive{}, fmt.Errorf("la clé %s n'est pas branchée", v)
	}
	var found []Drive
	for _, d := range ds {
		if v.Label != "" && strings.EqualFold(d.Label, v.Label) {
			found = append(found, d)
		}
	}
	switch len(found) {
	case 0:
		return Drive{}, fmt.Errorf("la clé %s n'est pas branchée", v)
	case 1:
		return found[0], nil
	default:
		return Drive{}, fmt.Errorf("plusieurs lecteurs s'appellent %s : impossible de savoir lequel utiliser", v)
	}
}

// ForPath renvoie le lecteur qui contient le dossier p (celui dont la racine est
// le plus long préfixe du chemin). ok=false si aucun lecteur connu ne le contient.
func ForPath(p string) (Drive, bool) {
	ds, err := List()
	if err != nil {
		return Drive{}, false
	}
	lp := strings.ToLower(strings.ReplaceAll(p, "/", `\`))
	best, n := Drive{}, -1
	for _, d := range ds {
		r := strings.ToLower(strings.ReplaceAll(d.Root, "/", `\`))
		r = strings.TrimRight(r, `\`)
		if r == "" {
			continue
		}
		if (lp == r || strings.HasPrefix(lp, r+`\`)) && len(r) > n {
			best, n = d, len(r)
		}
	}
	return best, n >= 0
}
