package app

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"synkro/internal/syncer"
	"synkro/internal/vfs"
)

func TestOverlaps(t *testing.T) {
	loc := func(p string) place { return placeOf(Endpoint{Type: "local", Path: p}) }
	sftp := func(h string, port int, p string) place {
		return placeOf(Endpoint{Type: "sftp", SFTP: &vfs.SFTPConfig{Host: h, Port: port, Path: p}})
	}
	cases := []struct {
		a, b place
		want bool
		why  string
	}{
		{loc("/data/photos"), loc("/data/photos/"), true, "même dossier"},
		{loc("/data"), loc("/data/photos/2024"), true, "parent / sous-dossier"},
		{loc("/data/photos"), loc("/data/photos-old"), false, "préfixe de nom seulement"},
		{loc("/a"), loc("/b"), false, "dossiers distincts"},
		{sftp("NAS.local", 22, "/home/x"), sftp("nas.local", 0, "/home/x/sub"), true, "même serveur (casse, port par défaut)"},
		{sftp("nas.local", 22, "/home/x"), sftp("nas.local", 2222, "/home/x"), false, "autre port = autre serveur"},
		{loc("/home/x"), sftp("nas.local", 22, "/home/x"), false, "local et distant"},
	}
	for _, c := range cases {
		if got := overlaps(c.a, c.b); got != c.want {
			t.Errorf("%s : overlaps=%v, attendu %v (%+v / %+v)", c.why, got, c.want, c.a, c.b)
		}
	}
}

// Deux jobs qui partagent un dossier : le second attend la fin du premier
// puis démarre seul ; ils ne tournent jamais en même temps.
func TestSharedFolderWaits(t *testing.T) {
	src, shared, other := t.TempDir(), t.TempDir(), t.TempDir()
	for i := 0; i < 1500; i++ {
		put(t, src, fmt.Sprintf("d%02d/f%04d.txt", i%30, i), "contenu", time.Now())
	}
	put(t, other, "o.txt", "o", time.Now())
	os.MkdirAll(filepath.Join(shared, "sous-dossier"), 0o755)
	store, _ := OpenStore(t.TempDir())
	a, _ := store.Save(Job{Name: "A", Left: Endpoint{Type: "local", Path: src}, Right: Endpoint{Type: "local", Path: shared}, Options: syncer.Options{Mode: syncer.Backup}})
	b, _ := store.Save(Job{Name: "B", Left: Endpoint{Type: "local", Path: other}, Right: Endpoint{Type: "local", Path: filepath.Join(shared, "sous-dossier")}, Options: syncer.Options{Mode: syncer.Backup}})
	r := NewRunner(store)

	if err := r.Start(a.ID, true); err != nil {
		t.Fatal(err)
	}
	wait(t, r, a.ID, "syncing")
	if err := r.Start(b.ID, true); err != nil {
		t.Fatal(err)
	}
	sawWaiting, together := false, false
	for i := 0; i < 1000; i++ {
		sa, sb := r.Status(a.ID).State, r.Status(b.ID).State
		if sb == "waiting" {
			sawWaiting = true
		}
		if (sa == "syncing" || sa == "analyzing") && (sb == "syncing" || sb == "analyzing") {
			together = true
		}
		if !r.Busy(a.ID) && !r.Busy(b.ID) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !sawWaiting || together {
		t.Fatalf("B devait attendre A (attente vue=%v, en même temps=%v)", sawWaiting, together)
	}
	if st := r.Status(b.ID); st.State != "done" || get(filepath.Join(shared, "sous-dossier"), "o.txt") != "o" {
		t.Fatalf("B devait démarrer seul après A : %+v", st)
	}
}

// File d'attente : les jobs passent dans l'ordre ; un job retiré ne part pas.
func TestQueueRunsInOrder(t *testing.T) {
	store, _ := OpenStore(t.TempDir())
	var ids []string
	var dsts []string
	for i := 0; i < 3; i++ {
		s, d := t.TempDir(), t.TempDir()
		put(t, s, "f.txt", fmt.Sprint(i), time.Now())
		j, _ := store.Save(Job{Name: fmt.Sprint("J", i), Left: Endpoint{Type: "local", Path: s}, Right: Endpoint{Type: "local", Path: d}, Options: syncer.Options{Mode: syncer.Backup}})
		ids = append(ids, j.ID)
		dsts = append(dsts, d)
	}
	r := NewRunner(store)
	if err := r.Queue(ids); err != nil {
		t.Fatal(err)
	}
	if st := r.Status(ids[2]).State; st != "queued" {
		t.Fatalf("le 3e job devait être en file d'attente, état %q", st)
	}
	r.Cancel(ids[1]) // retiré avant son tour
	if err := r.Queue([]string{ids[2]}); err == nil {
		t.Fatal("un job déjà en file ne doit pas pouvoir y être ajouté deux fois")
	}
	for i := 0; i < 400 && (r.Busy(ids[0]) || r.Busy(ids[2])); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if get(dsts[0], "f.txt") != "0" || get(dsts[2], "f.txt") != "2" {
		t.Fatal("les jobs 1 et 3 devaient être synchronisés")
	}
	if get(dsts[1], "f.txt") != "<absent>" || r.Status(ids[1]).Message != "Retiré de la file d'attente" {
		t.Fatalf("le job retiré ne devait pas tourner : %+v", r.Status(ids[1]))
	}
}

// « Tout annuler » arrête le job en cours et ceux qui attendent son dossier ;
// « Annuler » seul laisse partir le suivant.
func TestCancelAllAndCancelOne(t *testing.T) {
	mk := func() (*Runner, []string) {
		src := t.TempDir()
		for i := 0; i < 3000; i++ {
			put(t, src, fmt.Sprintf("d%02d/f%04d.txt", i%30, i), "contenu", time.Now())
		}
		store, _ := OpenStore(t.TempDir())
		var ids []string
		for _, n := range []string{"A", "B", "C"} {
			j, _ := store.Save(Job{Name: n, Left: Endpoint{Type: "local", Path: src}, Right: Endpoint{Type: "local", Path: t.TempDir()}, Options: syncer.Options{Mode: syncer.Backup}})
			ids = append(ids, j.ID)
		}
		return NewRunner(store), ids
	}
	settle := func(r *Runner, ids []string) {
		deadline := time.Now().Add(30 * time.Second)
		for _, id := range ids {
			for r.Busy(id) && time.Now().Before(deadline) {
				time.Sleep(20 * time.Millisecond)
			}
		}
	}
	// tout annuler
	r, ids := mk()
	if err := r.Start(ids[0], true); err != nil {
		t.Fatal(err)
	}
	wait(t, r, ids[0], "syncing")
	for _, id := range ids[1:] {
		if err := r.Start(id, true); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(100 * time.Millisecond)
	r.CancelAll()
	settle(r, ids)
	for _, id := range ids[1:] {
		if st := r.Status(id); st.Message != "Annulé avant le démarrage" {
			t.Fatalf("le job en attente devait être annulé : %+v", st)
		}
	}
	// annuler seulement le premier : les suivants passent
	r, ids = mk()
	if err := r.Start(ids[0], true); err != nil {
		t.Fatal(err)
	}
	wait(t, r, ids[0], "syncing")
	for _, id := range ids[1:] {
		if err := r.Start(id, true); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(100 * time.Millisecond)
	r.Cancel(ids[0])
	settle(r, ids)
	for _, id := range ids[1:] {
		if st := r.Status(id); st.State != "done" || st.Message == "Annulé avant le démarrage" {
			t.Fatalf("le job suivant devait se synchroniser : %+v", st)
		}
	}
}
