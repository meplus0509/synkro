package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"synkro/internal/drives"
	"synkro/internal/syncer"
)

// fakeDrives simule le branchement d'une clé sur une lettre (un dossier temporaire).
type fakeDrives struct {
	mu  sync.Mutex
	ds  []drives.Drive
	old func() ([]drives.Drive, error)
}

func plugger(t *testing.T) *fakeDrives {
	f := &fakeDrives{old: drives.List}
	drives.List = func() ([]drives.Drive, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		return append([]drives.Drive(nil), f.ds...), nil
	}
	t.Cleanup(func() { drives.List = f.old })
	return f
}

func (f *fakeDrives) plug(root, serial string) {
	f.mu.Lock()
	f.ds = []drives.Drive{{Root: root, Label: "xxx", Serial: serial, Removable: true}}
	f.mu.Unlock()
}
func (f *fakeDrives) unplug() { f.mu.Lock(); f.ds = nil; f.mu.Unlock() }

// La clé change de lettre d'un jour à l'autre : le job la retrouve sans modification.
func TestUSBFollowsDriveLetter(t *testing.T) {
	f := plugger(t)
	src := t.TempDir()
	put(t, src, "doc.txt", "v1", time.Now())
	jour1, jour2 := t.TempDir(), t.TempDir() // « F: » puis « G: »
	for _, root := range []string{jour1, jour2} {
		os.MkdirAll(filepath.Join(root, "Sauvegarde"), 0o755)
	}
	store, _ := OpenStore(t.TempDir())
	job, err := store.Save(Job{Name: "test USB", Left: Endpoint{Type: "local", Path: src},
		Right:   Endpoint{Type: "usb", Path: `\Sauvegarde`, Volume: &drives.Volume{Serial: "ABCD-1234", Label: "xxx"}},
		Options: syncer.Options{Mode: syncer.Backup}})
	if err != nil {
		t.Fatal(err)
	}
	r := NewRunner(store)

	// clé absente : message clair, aucune action
	f.unplug()
	st, _, _ := r.RunBlocking(job.ID, true, nil)
	if st.State != "error" || !strings.Contains(st.Message, "n'est pas branchée") {
		t.Fatalf("clé absente : %+v", st)
	}

	f.plug(jour1, "ABCD-1234")
	if st, _, _ := r.RunBlocking(job.ID, true, nil); st.State != "done" || get(filepath.Join(jour1, "Sauvegarde"), "doc.txt") != "v1" {
		t.Fatalf("jour 1 : %+v", st)
	}
	put(t, src, "doc.txt", "v2, plus long", time.Now().Add(time.Hour))
	f.plug(jour2, "ABCD-1234") // autre lettre le lendemain
	if st, _, _ := r.RunBlocking(job.ID, true, nil); st.State != "done" || get(filepath.Join(jour2, "Sauvegarde"), "doc.txt") != "v2, plus long" {
		t.Fatalf("jour 2 (autre lettre) : %+v", st)
	}
	// une autre clé, même nom, autre numéro : refusée
	f.plug(jour1, "FFFF-0000")
	if st, _, _ := r.RunBlocking(job.ID, true, nil); st.State != "error" {
		t.Fatalf("une autre clé de même nom ne doit pas être utilisée : %+v", st)
	}
}

// Option « synchroniser au branchement » : la synchro part toute seule.
func TestSyncOnPlug(t *testing.T) {
	f := plugger(t)
	src, key := t.TempDir(), t.TempDir()
	put(t, src, "photo.jpg", "img", time.Now())
	store, _ := OpenStore(t.TempDir())
	job, _ := store.Save(Job{Name: "auto", Left: Endpoint{Type: "local", Path: src},
		Right:   Endpoint{Type: "usb", Path: "/", Volume: &drives.Volume{Serial: "ABCD-1234", Label: "xxx"}},
		Options: syncer.Options{Mode: syncer.Backup}, SyncOnPlug: true})
	r := NewRunner(store)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.watchPlugs(ctx)
	time.Sleep(200 * time.Millisecond)
	if get(key, "photo.jpg") != "<absent>" {
		t.Fatal("rien ne doit partir avant le branchement")
	}
	f.plug(key, "ABCD-1234")
	for i := 0; i < 100 && get(key, "photo.jpg") != "img"; i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if get(key, "photo.jpg") != "img" {
		t.Fatalf("la synchro devait partir au branchement : %+v", r.Status(job.ID))
	}
}
