package vfs

import "time"

// Meta : date de création et attributs d'un fichier (Windows).
type Meta struct {
	Created time.Time
	Attrs   uint32
}

// MetaFS : backends capables de lire et d'écrire date de création et attributs.
type MetaFS interface {
	GetMeta(path string) (Meta, error)
	SetMeta(path string, m Meta, created, attrs bool) error
}

func (l *Local) GetMeta(p string) (Meta, error) { return getMeta(l.rabs(p)) }
func (l *Local) SetMeta(p string, m Meta, created, attrs bool) error {
	return setMeta(l.abs(p), m, created, attrs)
}
