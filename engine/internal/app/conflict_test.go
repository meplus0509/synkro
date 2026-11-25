package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"synkro/internal/syncer"
)

func put(t *testing.T, root, rel, content string, mt time.Time) {
	t.Helper()
	p := filepath.Join(root, rel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte(content), 0o644)
	os.Chtimes(p, mt, mt)
}

func get(root, rel string) string {
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		return "<absent>"
	}
	return string(b)
}

func wait(t *testing.T, r *Runner, id string, states ...string) Status {
	t.Helper()
	for i := 0; i < 200; i++ {
		st := r.Status(id)
		for _, s := range states {
			if st.State == s {
				return st
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("état %v jamais atteint (actuel : %+v)", states, r.Status(id))
	return Status{}
}

// Synchro manuelle avec « me demander » : arrêt sur les conflits, puis reprise
// avec les choix ; abandon possible ; une synchro automatique ne pose pas de question.
func TestConflictWorkflow(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	t0 := time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC)
	put(t, a, "x.txt", "gauche", t0)
	put(t, b, "x.txt", "droite, plus longue", t0.Add(time.Hour)) // jamais synchronisés : conflit
	put(t, a, "seul.txt", "pas de conflit", t0)

	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.Save(Job{Name: "bidir", Left: Endpoint{Type: "local", Path: a}, Right: Endpoint{Type: "local", Path: b},
		Options: syncer.Options{Mode: syncer.TwoWay, ConflictAsk: true, ConflictDefault: "left"}})
	if err != nil {
		t.Fatal(err)
	}
	r := NewRunner(store)

	// 1. abandon : rien n'est copié
	if err := r.Start(job.ID, true); err != nil {
		t.Fatal(err)
	}
	wait(t, r, job.ID, "conflicts")
	cs := r.Conflicts(job.ID)
	if len(cs) != 1 || cs[0].Path != "x.txt" || cs[0].Default != "left" {
		t.Fatalf("conflit attendu sur x.txt avec « gauche » par défaut : %+v", cs)
	}
	if get(b, "seul.txt") != "<absent>" {
		t.Fatal("rien ne doit être copié tant que les conflits ne sont pas tranchés")
	}
	if err := r.Resolve(job.ID, nil, true); err != nil {
		t.Fatal(err)
	}
	if st := r.Status(job.ID); st.State != "done" || get(b, "seul.txt") != "<absent>" {
		t.Fatalf("abandon incorrect : %+v", st)
	}

	// 2. reprise avec le choix « droite »
	r.Start(job.ID, true)
	wait(t, r, job.ID, "conflicts")
	if err := r.Resolve(job.ID, map[string]string{"x.txt": "right"}, false); err != nil {
		t.Fatal(err)
	}
	st := wait(t, r, job.ID, "done", "error")
	if st.State != "done" || get(a, "x.txt") != "droite, plus longue" || get(b, "seul.txt") != "pas de conflit" {
		t.Fatalf("reprise incorrecte : %+v / x=%q", st, get(a, "x.txt"))
	}

	// 3. synchro automatique : le conflit est laissé tel quel, le reste avance
	put(t, a, "x.txt", "auto gauche", t0.Add(3*time.Hour))
	put(t, b, "x.txt", "auto droite", t0.Add(4*time.Hour))
	put(t, a, "nouveau.txt", "n", t0)
	r.StartAuto(job.ID)
	st = wait(t, r, job.ID, "done", "error")
	if st.State != "done" || get(a, "x.txt") != "auto gauche" || get(b, "x.txt") != "auto droite" || get(b, "nouveau.txt") != "n" {
		t.Fatalf("synchro auto incorrecte : %+v", st)
	}
}
