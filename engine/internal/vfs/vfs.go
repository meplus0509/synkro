// Package vfs définit une abstraction de système de fichiers commune
// aux différents backends (dossier local, SFTP…).
package vfs

import (
	"errors"
	"io"
	"time"
)

// Entry décrit un fichier ou un dossier, chemin relatif à la racine du backend
// avec des séparateurs "/".
type Entry struct {
	Path    string    `json:"path"`
	IsDir   bool      `json:"isDir"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modTime"`
	// Attributs (Windows : attributs « caché » et « système » ; ailleurs : nom en « . »)
	Hidden bool `json:"-"`
	System bool `json:"-"`
}

// FS est l'interface que chaque backend implémente.
// Tous les chemins sont relatifs à la racine du job et utilisent "/".
type FS interface {
	// Walk parcourt récursivement l'arborescence. skipDir permet d'ignorer un dossier.
	Walk(fn func(e Entry) (skipDir bool, err error)) error
	Stat(path string) (Entry, error)
	Open(path string) (io.ReadCloser, error)
	// Create crée (ou tronque) un fichier, en créant les dossiers parents.
	Create(path string) (io.WriteCloser, error)
	MkdirAll(path string) error
	Remove(path string) error             // fichier ou dossier vide
	Rename(oldPath, newPath string) error // écrase la destination si elle existe
	Chtimes(path string, mtime time.Time) error
	// Describe renvoie une description lisible (ex: "sftp://user@host/path").
	Describe() string
	Close() error
}

// ErrNotExist est renvoyée par Stat quand le chemin n'existe pas.
var ErrNotExist = errors.New("n'existe pas")

// Parent renvoie le dossier parent d'un chemin relatif ("" pour la racine).
func Parent(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[:i]
		}
	}
	return ""
}

// Base renvoie le dernier élément d'un chemin relatif.
func Base(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[i+1:]
		}
	}
	return p
}

// Reconnecter est implémenté par les backends réseau capables de rouvrir leur
// connexion après une coupure (SFTP).
type Reconnecter interface {
	Reconnect() error
}
