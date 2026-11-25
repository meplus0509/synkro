package vfs

import (
	"os"
	"path/filepath"
	"testing"
)

// Une racine qui est un lien vers un dossier (jonction sous Windows) doit être lue.
func TestWalkLinkedRoot(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "vrai")
	os.MkdirAll(filepath.Join(real, "sous"), 0o755)
	os.WriteFile(filepath.Join(real, "sous", "a.txt"), []byte("a"), 0o644)
	link := filepath.Join(base, "lien")
	if err := os.Symlink(real, link); err != nil {
		t.Skip(err)
	}
	l, err := NewLocal(link)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	l.Walk(func(e Entry) (bool, error) { got = append(got, e.Path); return false, nil })
	if len(got) != 2 || got[0] != "sous" || got[1] != "sous/a.txt" {
		t.Fatalf("lu : %v", got)
	}
	ents, _ := os.ReadDir(base)
	n := 0
	for _, e := range ents {
		if IsLinkedDir(base, e) {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("liens vers dossier trouvés : %d", n)
	}
}
