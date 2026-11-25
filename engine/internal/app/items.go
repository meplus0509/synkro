package app

import (
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"strings"
	"time"

	"synkro/internal/syncer"
	"synkro/internal/vfs"
)

// cleanItemPath vérifie un chemin relatif reçu de l'interface.
func cleanItemPath(p string) (string, error) {
	p = strings.Trim(strings.ReplaceAll(p, `\`, "/"), "/")
	c := path.Clean(p)
	if p == "" || c == "." || c != p || strings.HasPrefix(c, "../") || c == ".." {
		return "", errors.New("chemin invalide")
	}
	return c, nil
}

type removeAller interface{ RemoveAll(p string) error }

// DeleteItem supprime un fichier ou un dossier d'un côté ("left", "right" ou "both").
// Avec la corbeille activée, l'élément est déplacé dans .synkro-trash/<date>/.
func (r *Runner) DeleteItem(id, p, side string) (string, error) {
	p, err := cleanItemPath(p)
	if err != nil {
		return "", err
	}
	if r.Busy(id) {
		return "", errors.New("le job est en cours : attendez la fin ou annulez-le")
	}
	job, err := r.store.Get(id)
	if err != nil {
		return "", err
	}
	var sides []string
	switch side {
	case "left", "right":
		sides = []string{side}
	case "both":
		sides = []string{"left", "right"}
	default:
		return "", errors.New("côté invalide")
	}
	stamp := time.Now().Format("2006-01-02_15-04-05")
	var done []string
	for _, s := range sides {
		ep, name := job.Left, "gauche"
		if s == "right" {
			ep, name = job.Right, "droite"
		}
		fs, err := r.open(id, ep, s)
		if err != nil {
			return "", fmt.Errorf("côté %s : %w", name, err)
		}
		e, err := fs.Stat(p)
		if errors.Is(err, vfs.ErrNotExist) {
			fs.Close()
			continue
		}
		if err == nil {
			switch {
			case job.Options.UseTrash:
				err = fs.Rename(p, syncer.TrashDir+"/"+stamp+"/"+p)
			case e.IsDir:
				if ra, ok := fs.(removeAller); ok {
					err = ra.RemoveAll(p)
				} else {
					err = errors.New("suppression de dossier non prise en charge")
				}
			default:
				err = fs.Remove(p)
			}
		}
		fs.Close()
		if err != nil {
			return "", fmt.Errorf("côté %s : %w", name, err)
		}
		done = append(done, name)
		where := "supprimé"
		if job.Options.UseTrash {
			where = "mis à la corbeille " + syncer.TrashDir
		}
		r.store.AppendLog(id, fmt.Sprintf("Suppression manuelle (%s) : %s %s", name, p, where))
	}
	if len(done) == 0 {
		return "Rien à supprimer : l'élément n'existe plus.", nil
	}
	return fmt.Sprintf("« %s » supprimé (%s).", p, strings.Join(done, " et ")), nil
}

// AddFilter ajoute un motif d'exclusion ou d'inclusion au job.
func (r *Runner) AddFilter(id, kind, pattern string) error {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return errors.New("motif vide")
	}
	if r.Busy(id) {
		return errors.New("le job est en cours : attendez la fin ou annulez-le")
	}
	if kind != "exclude" && kind != "include" {
		return errors.New("type de filtre invalide")
	}
	if _, err := r.store.Get(id); err != nil {
		return err
	}
	r.store.update(id, func(j *Job) {
		list := &j.Options.Exclude
		if kind == "include" {
			list = &j.Options.Include
		}
		for _, x := range *list {
			if x == pattern {
				return
			}
		}
		*list = append(*list, pattern)
	})
	return nil
}

// TrashSide décrit le contenu de la corbeille .synkro-trash d'un côté.
type TrashSide struct {
	Side  string `json:"side"` // "gauche" ou "droite"
	Files int    `json:"files"`
	Bytes int64  `json:"bytes"`
	Err   string `json:"error,omitempty"` // côté inaccessible (clé débranchée…)
}

// eachSide ouvre tour à tour le côté gauche puis le côté droit du job.
// fs vaut nil si le côté est inaccessible.
func (r *Runner) eachSide(job Job, fn func(name string, fs vfs.FS)) {
	for _, s := range []string{"left", "right"} {
		ep, name := job.Left, "gauche"
		if s == "right" {
			ep, name = job.Right, "droite"
		}
		fs, err := r.open(job.ID, ep, s)
		if err != nil {
			fn(name, nil)
			continue
		}
		fn(name, fs)
		fs.Close()
	}
}

// TrashInfo compte les fichiers gardés dans la corbeille de chaque côté.
func (r *Runner) TrashInfo(id string) ([]TrashSide, error) {
	job, err := r.store.Get(id)
	if err != nil {
		return nil, err
	}
	var out []TrashSide
	r.eachSide(job, func(name string, fs vfs.FS) {
		t := TrashSide{Side: name}
		defer func() { out = append(out, t) }()
		if fs == nil {
			t.Err = "inaccessible"
			return
		}
		if _, err := fs.Stat(syncer.TrashDir); err != nil {
			if !errors.Is(err, vfs.ErrNotExist) {
				t.Err = err.Error()
			}
			return
		}
		err := fs.Walk(func(e vfs.Entry) (bool, error) {
			inTrash := e.Path == syncer.TrashDir || strings.HasPrefix(e.Path, syncer.TrashDir+"/")
			if !inTrash {
				return e.IsDir, nil // on ne descend que dans la corbeille
			}
			if !e.IsDir {
				t.Files++
				t.Bytes += e.Size
			}
			return false, nil
		})
		if err != nil {
			t.Err = err.Error()
		}
	})
	return out, nil
}

// EmptyTrash supprime définitivement la corbeille .synkro-trash des deux côtés.
func (r *Runner) EmptyTrash(id string) (string, error) {
	if r.Busy(id) {
		return "", errors.New("le job est en cours : attendez la fin ou annulez-le")
	}
	job, err := r.store.Get(id)
	if err != nil {
		return "", err
	}
	var done, failed []string
	r.eachSide(job, func(name string, fs vfs.FS) {
		if fs == nil {
			failed = append(failed, name+" (inaccessible)")
			return
		}
		if _, err := fs.Stat(syncer.TrashDir); errors.Is(err, vfs.ErrNotExist) {
			return
		}
		ra, ok := fs.(removeAller)
		if !ok {
			failed = append(failed, name+" (non pris en charge)")
			return
		}
		if err := ra.RemoveAll(syncer.TrashDir); err != nil {
			failed = append(failed, name+" : "+err.Error())
			return
		}
		done = append(done, name)
		r.store.AppendLog(id, fmt.Sprintf("Corbeille %s vidée (côté %s)", syncer.TrashDir, name))
	})
	if len(failed) > 0 {
		return "", fmt.Errorf("corbeille non vidée : %s", strings.Join(failed, ", "))
	}
	if len(done) == 0 {
		return "La corbeille était déjà vide.", nil
	}
	return fmt.Sprintf("Corbeille vidée (%s).", strings.Join(done, " et ")), nil
}

// ViewMax : taille maximale lue pour « Voir le contenu ».
const ViewMax = 2 << 20

// ReadItem lit le début d'un fichier d'un côté ("left" ou "right"), au plus max octets.
func (r *Runner) ReadItem(id, p, side string, max int64) (data []byte, e vfs.Entry, err error) {
	p, err = cleanItemPath(p)
	if err != nil {
		return nil, e, err
	}
	job, err := r.store.Get(id)
	if err != nil {
		return nil, e, err
	}
	ep := job.Left
	switch side {
	case "left":
	case "right":
		ep = job.Right
	default:
		return nil, e, errors.New("côté invalide")
	}
	fs, err := r.open(id, ep, side)
	if err != nil {
		return nil, e, err
	}
	defer fs.Close()
	if e, err = fs.Stat(p); err != nil {
		if errors.Is(err, vfs.ErrNotExist) {
			return nil, e, errors.New("le fichier n'existe pas de ce côté")
		}
		return nil, e, err
	}
	if e.IsDir {
		return nil, e, errors.New("c'est un dossier")
	}
	rc, err := fs.Open(p)
	if err != nil {
		return nil, e, err
	}
	defer rc.Close()
	data, err = io.ReadAll(io.LimitReader(rc, max))
	return data, e, err
}

// LocalItem : chemin complet d'un élément d'un côté local (dossier ou clé USB).
func (r *Runner) LocalItem(id, p, side string) (string, error) {
	p, err := cleanItemPath(p)
	if err != nil {
		return "", err
	}
	job, err := r.store.Get(id)
	if err != nil {
		return "", err
	}
	ep := job.Left
	if side == "right" {
		ep = job.Right
	}
	if ep.Type != "local" && ep.Type != "usb" {
		return "", errors.New("seulement pour un dossier local ou une clé USB")
	}
	root := ep.Path
	if ep.Type == "usb" {
		if root, err = ep.Resolve(); err != nil {
			return "", err
		}
	}
	return filepath.Join(root, filepath.FromSlash(p)), nil
}
