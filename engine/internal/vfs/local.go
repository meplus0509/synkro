package vfs

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Local est un backend sur un dossier local (disque, clé USB, partage monté).
type Local struct {
	Root string
	// Shadows (Windows) : fichiers verrouillés lus dans un instantané Volume
	// Shadow Copy, créé à la demande. nil = désactivé.
	Shadows *ShadowSet
	// read : racine de lecture quand tout le dossier est lu dans un instantané
	read string
}

// rroot : racine utilisée pour lire (instantané éventuel).
func (l *Local) rroot() string {
	if l.read != "" {
		return l.read
	}
	return l.Root
}

func (l *Local) rabs(p string) string { return filepath.Join(l.rroot(), filepath.FromSlash(p)) }

// Snapshot fait lire tout le dossier dans un instantané (Volume Shadow Copy) pris
// maintenant : fichiers cohérents et jamais verrouillés. Écritures inchangées.
func (l *Local) Snapshot(set *ShadowSet) error {
	p, err := set.Path(l.Root)
	if err != nil {
		return err
	}
	if _, err := os.Stat(p); err != nil {
		return fmt.Errorf("instantané illisible : %w", err)
	}
	l.read = p
	l.Shadows = set
	return nil
}

func NewLocal(root string) (*Local, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, errors.New(abs + " n'est pas un dossier")
	}
	return &Local{Root: abs}, nil
}

func (l *Local) abs(p string) string { return filepath.Join(l.Root, filepath.FromSlash(p)) }

func (l *Local) Walk(fn func(Entry) (bool, error)) error {
	// Racine terminée par un séparateur : si la racine est elle-même une jonction
	// ou un lien vers un dossier, il est suivi (sinon rien ne serait lu).
	root := l.rroot()
	if !os.IsPathSeparator(root[len(root)-1]) {
		root += string(filepath.Separator)
	}
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == root {
				return err
			}
			return nil // élément illisible : on l'ignore
		}
		if path == root {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil // les liens symboliques ne sont pas suivis
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(l.rroot(), path)
		e := Entry{Path: filepath.ToSlash(rel), IsDir: d.IsDir(), ModTime: info.ModTime()}
		e.Hidden, e.System = fileAttrs(info)
		if !d.IsDir() {
			if !info.Mode().IsRegular() {
				return nil
			}
			e.Size = info.Size()
		}
		skip, err := fn(e)
		if err != nil {
			return err
		}
		if skip && d.IsDir() {
			return filepath.SkipDir
		}
		return nil
	})
}

func (l *Local) Stat(p string) (Entry, error) {
	info, err := os.Stat(l.rabs(p))
	if errors.Is(err, fs.ErrNotExist) {
		return Entry{}, ErrNotExist
	}
	if err != nil {
		return Entry{}, err
	}
	e := Entry{Path: p, IsDir: info.IsDir(), ModTime: info.ModTime()}
	if !info.IsDir() {
		e.Size = info.Size()
	}
	return e, nil
}

func (l *Local) Open(p string) (io.ReadCloser, error) {
	f, err := os.Open(l.rabs(p))
	if err != nil && l.Shadows != nil && l.read == "" && isLocked(err) {
		// fichier verrouillé par un autre programme : lu dans un instantané du disque
		sp, e2 := l.Shadows.Path(l.abs(p))
		if e2 != nil {
			return nil, fmt.Errorf("%w (copie des fichiers verrouillés impossible : %v)", err, e2)
		}
		return os.Open(sp)
	}
	return f, err
}

// ErrRootGone : le dossier du job a disparu (clé débranchée, partage déconnecté).
// On ne le recrée jamais : ce serait écrire au mauvais endroit.
var ErrRootGone = errors.New("dossier du job introuvable (clé ou disque débranché ?)")

func (l *Local) mkdirs(dir string) error {
	if _, err := os.Stat(l.Root); err != nil {
		return ErrRootGone
	}
	return os.MkdirAll(dir, 0o755)
}

func (l *Local) Create(p string) (io.WriteCloser, error) {
	if err := l.mkdirs(filepath.Dir(l.abs(p))); err != nil {
		return nil, err
	}
	return os.Create(l.abs(p))
}

func (l *Local) MkdirAll(p string) error { return l.mkdirs(l.abs(p)) }

func (l *Local) Remove(p string) error {
	err := os.Remove(l.abs(p))
	if err != nil && clearReadOnly(l.abs(p)) { // fichier en lecture seule (attributs recopiés)
		err = os.Remove(l.abs(p))
	}
	return err
}

func (l *Local) Rename(o, n string) error {
	if err := l.mkdirs(filepath.Dir(l.abs(n))); err != nil {
		return err
	}
	err := os.Rename(l.abs(o), l.abs(n)) // os.Rename écrase la destination (y compris sous Windows)
	if err != nil && clearReadOnly(l.abs(n)) {
		err = os.Rename(l.abs(o), l.abs(n))
	}
	return err
}

func (l *Local) Chtimes(p string, t time.Time) error { return os.Chtimes(l.abs(p), t, t) }

func (l *Local) Describe() string { return l.Root }

func (l *Local) Close() error {
	if l.Shadows != nil {
		l.Shadows.Close()
	}
	return nil
}

func (l *Local) RemoveAll(p string) error {
	if p == "" {
		return errors.New("refus de supprimer la racine")
	}
	return os.RemoveAll(l.abs(p))
}

// IsLinkedDir indique si un élément de dossier est une jonction ou un lien
// symbolique qui mène à un dossier (Windows signale les jonctions comme des
// éléments « irréguliers », pas comme des dossiers). Les jonctions système
// cachées (« Application Data », « Mes documents »… dans le profil Windows)
// sont écartées : elles ne mènent à rien de lisible.
func IsLinkedDir(dir string, d fs.DirEntry) bool {
	if d.IsDir() || d.Type()&(fs.ModeSymlink|fs.ModeIrregular) == 0 {
		return false
	}
	if info, err := d.Info(); err == nil {
		if h, s := fileAttrs(info); h && s {
			return false
		}
	}
	st, err := os.Stat(filepath.Join(dir, d.Name()))
	return err == nil && st.IsDir()
}
