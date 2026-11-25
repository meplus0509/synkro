package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"synkro/internal/syncer"
	"synkro/internal/vfs"
)

func TestDeleteItemAndFilter(t *testing.T) {
	l, rdir := t.TempDir(), t.TempDir()
	put(t, l, "a/x.txt", "x", time.Now())
	put(t, rdir, "a/x.txt", "x", time.Now())
	put(t, rdir, "b/y.txt", "y", time.Now())
	store, _ := OpenStore(t.TempDir())
	j, _ := store.Save(Job{Name: "J", Left: Endpoint{Type: "local", Path: l}, Right: Endpoint{Type: "local", Path: rdir}, Options: syncer.Options{Mode: syncer.Mirror, UseTrash: true}})
	r := NewRunner(store)
	for _, bad := range []string{"", "..", "../x", "/", "a/../../x"} {
		if _, err := r.DeleteItem(j.ID, bad, "left"); err == nil {
			t.Fatalf("chemin %q accepté", bad)
		}
	}
	if _, err := r.DeleteItem(j.ID, "a", "both"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(l, "a")); !os.IsNotExist(err) {
		t.Fatal("dossier a encore présent à gauche")
	}
	if _, err := os.Stat(filepath.Join(rdir, "a")); !os.IsNotExist(err) {
		t.Fatal("dossier a encore présent à droite")
	}
	if m, _ := filepath.Glob(filepath.Join(rdir, ".synkro-trash", "*", "a", "x.txt")); len(m) != 1 {
		t.Fatal("le dossier devait aller dans la corbeille")
	}
	// sans corbeille : suppression définitive d'un dossier non vide
	store.update(j.ID, func(x *Job) { x.Options.UseTrash = false })
	if _, err := r.DeleteItem(j.ID, "b", "right"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(rdir, "b")); !os.IsNotExist(err) {
		t.Fatal("dossier b encore présent")
	}
	if err := r.AddFilter(j.ID, "exclude", "*.log"); err != nil {
		t.Fatal(err)
	}
	r.AddFilter(j.ID, "exclude", "*.log")
	r.AddFilter(j.ID, "include", "/a")
	got, _ := store.Get(j.ID)
	if len(got.Options.Exclude) != 1 || got.Options.Include[0] != "/a" {
		t.Fatalf("filtres : %+v", got.Options)
	}
}

// La synchro qui suit une analyse part de cette analyse (pas de nouvelle lecture),
// sauf si le job a été modifié entre-temps.
func TestSyncReusesAnalysis(t *testing.T) {
	l, rdir := t.TempDir(), t.TempDir()
	put(t, l, "a.txt", "a", time.Now())
	store, _ := OpenStore(t.TempDir())
	j, _ := store.Save(Job{Name: "J", Left: Endpoint{Type: "local", Path: l}, Right: Endpoint{Type: "local", Path: rdir}, Options: syncer.Options{Mode: syncer.Mirror}})
	r := NewRunner(store)
	run := func(doSync bool) {
		if err := r.Start(j.ID, doSync); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 500 && r.Busy(j.ID); i++ {
			time.Sleep(10 * time.Millisecond)
		}
	}
	run(false)
	put(t, l, "apres-analyse.txt", "b", time.Now()) // ajouté après l'analyse
	run(true)
	if _, err := os.Stat(filepath.Join(rdir, "a.txt")); err != nil {
		t.Fatal("a.txt devait être copié")
	}
	if _, err := os.Stat(filepath.Join(rdir, "apres-analyse.txt")); err == nil {
		t.Fatal("la synchro devait suivre l'analyse affichée")
	}
	// la synchro suivante relit tout
	run(true)
	if _, err := os.Stat(filepath.Join(rdir, "apres-analyse.txt")); err != nil {
		t.Fatal("la 2e synchro devait relire les côtés")
	}
	// job modifié après l'analyse : on relit
	put(t, l, "c.txt", "c", time.Now())
	run(false)
	r.AddFilter(j.ID, "exclude", "*.log")
	put(t, l, "d.txt", "d", time.Now())
	run(true)
	if _, err := os.Stat(filepath.Join(rdir, "d.txt")); err != nil {
		t.Fatal("job modifié : la synchro devait relire les côtés")
	}
}

func TestTrashInfoAndEmpty(t *testing.T) {
	l, rdir := t.TempDir(), t.TempDir()
	put(t, l, "keep.txt", "k", time.Now())
	put(t, l, ".synkro-trash/2026-01-01/a.txt", "12345", time.Now())
	put(t, l, ".synkro-trash/2026-01-02/sub/b.txt", "123", time.Now())
	store, _ := OpenStore(t.TempDir())
	j, _ := store.Save(Job{Name: "J", Left: Endpoint{Type: "local", Path: l}, Right: Endpoint{Type: "local", Path: rdir}, Options: syncer.Options{Mode: syncer.Mirror, UseTrash: true}})
	r := NewRunner(store)
	info, err := r.TrashInfo(j.ID)
	if err != nil || len(info) != 2 || info[0].Files != 2 || info[0].Bytes != 8 || info[1].Files != 0 {
		t.Fatalf("info = %+v, %v", info, err)
	}
	if _, err := r.EmptyTrash(j.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(l, ".synkro-trash")); !os.IsNotExist(err) {
		t.Fatal("corbeille encore présente")
	}
	if _, err := os.Stat(filepath.Join(l, "keep.txt")); err != nil {
		t.Fatal("fichier normal supprimé")
	}
}

func TestOwnDataExcluded(t *testing.T) {
	l, rdir := t.TempDir(), t.TempDir()
	put(t, l, "a.txt", "a", time.Now())
	data := filepath.Join(l, "Synkro", "synkro-data")
	store, err := OpenStore(data)
	if err != nil {
		t.Fatal(err)
	}
	j, _ := store.Save(Job{Name: "J", Left: Endpoint{Type: "local", Path: l}, Right: Endpoint{Type: "local", Path: rdir}, Options: syncer.Options{Mode: syncer.Mirror}})
	r := NewRunner(store)
	st, acts, _ := r.RunBlocking(j.ID, true, nil)
	for _, a := range acts {
		if strings.Contains(a.Path, "synkro-data") {
			t.Fatalf("dossier de données synchronisé : %s (%s)", a.Path, st.Message)
		}
	}
	if _, err := os.Stat(filepath.Join(rdir, "a.txt")); err != nil {
		t.Fatal("a.txt non copié")
	}
	if _, err := os.Stat(filepath.Join(rdir, "Synkro", "synkro-data")); !os.IsNotExist(err) {
		t.Fatal("synkro-data copié à droite")
	}
}

func TestSFTPUserEncrypted(t *testing.T) {
	dir := t.TempDir()
	store, _ := OpenStore(dir)
	sftp := func(u string) Endpoint {
		return Endpoint{Type: "sftp", SFTP: &vfs.SFTPConfig{Host: "h", Port: 22, User: u, Password: "pw", Path: "/x"}}
	}
	j, err := store.Save(Job{Name: "J", Left: Endpoint{Type: "local", Path: dir}, Right: sftp("demo"), Options: syncer.Options{Mode: syncer.Mirror}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "data.db"))
	if len(raw) == 0 || strings.Contains(string(raw), "demo") || strings.Contains(string(raw), `"pw"`) || strings.Contains(string(raw), `"name"`) {
		t.Fatalf("data.db absent ou lisible en clair :\n%s", raw)
	}
	if j.Right.SFTP.User != "demo" {
		t.Fatal("l'interface doit voir l'utilisateur en clair")
	}
	s2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	g, _ := s2.Get(j.ID)
	if g.Right.SFTP.User != "demo" || g.Right.SFTP.Password != "pw" {
		t.Fatalf("relu : %+v", g.Right.SFTP)
	}
	// ancien fichier avec l'utilisateur en clair : chiffré au démarrage
	old := `[{"id":"a1","name":"Vieux","left":{"type":"local","path":"/tmp"},"right":{"type":"sftp","path":"","sftp":{"host":"h","port":22,"user":"bob","path":"/"}},"options":{"mode":"mirror"}}]`
	os.Remove(filepath.Join(dir, "data.db"))
	os.WriteFile(filepath.Join(dir, "jobs.json"), []byte(old), 0o600)
	s3, _ := OpenStore(dir)
	if g, _ := s3.Get("a1"); g.Right.SFTP.User != "bob" {
		t.Fatalf("migration : %+v", g.Right.SFTP)
	}
	if _, err := os.Stat(filepath.Join(dir, "jobs.json")); !os.IsNotExist(err) {
		t.Fatal("jobs.json devait être converti en data.db puis supprimé")
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, "data.db")); strings.Contains(string(raw), "bob") {
		t.Fatal("ancien utilisateur resté en clair")
	}
}

func TestLogsWithoutSFTPUser(t *testing.T) {
	ep := Endpoint{Type: "sftp", SFTP: &vfs.SFTPConfig{Host: "localhost", Port: 54321, User: "demo", Path: "/media/sauve"}}
	if s := ep.String(); s != "sftp://localhost:54321/media/sauve" {
		t.Fatal(s)
	}
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "logs"), 0o755)
	os.WriteFile(filepath.Join(dir, "logs", "a.log"), []byte("── Analyse : C:\\x ⇄ sftp://demo@localhost/media (mirror)\n"), 0o600)
	ScrubLogs(dir)
	if b, _ := os.ReadFile(filepath.Join(dir, "logs", "a.log")); strings.Contains(string(b), "demo") || !strings.Contains(string(b), "sftp://localhost/media") {
		t.Fatalf("%s", b)
	}
}

func TestHostPortEncryptedAndKeyInSettings(t *testing.T) {
	dir := t.TempDir()
	// ancienne installation : secret.key séparé, jobs.json en clair
	oldKey := make([]byte, 32)
	for i := range oldKey {
		oldKey[i] = byte(i)
	}
	os.WriteFile(filepath.Join(dir, "secret.key"), oldKey, 0o600)
	os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"theme":"dark"}`), 0o600)
	old := `[{"id":"a1","name":"Vieux","left":{"type":"local","path":"/tmp"},"right":{"type":"sftp","path":"","sftp":{"host":"serveur.madatech.info","port":54321,"user":"bob","path":"/media"}},"options":{"mode":"mirror"}}]`
	os.WriteFile(filepath.Join(dir, "jobs.json"), []byte(old), 0o600)
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "secret.key")); !os.IsNotExist(err) {
		t.Fatal("secret.key devait être intégré à settings.json puis supprimé")
	}
	if st := s.Settings(); st["theme"] != "dark" || st[settingsKey] != "" {
		t.Fatalf("réglages : %v", st)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "data.db"))
	for _, clair := range []string{"madatech", "54321", "bob", "Vieux", "/media"} {
		if strings.Contains(string(raw), clair) {
			t.Fatalf("%q en clair dans data.db :\n%s", clair, raw)
		}
	}
	g, _ := s.Get("a1")
	if c := g.Right.SFTP; c.Host != "serveur.madatech.info" || c.Port != 54321 || c.User != "bob" {
		t.Fatalf("en mémoire : %+v", c)
	}
	// redémarrage : la clé vient de settings.json, tout se relit
	s2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	g, _ = s2.Get("a1")
	if c := g.Right.SFTP; c.Host != "serveur.madatech.info" || c.Port != 54321 || c.User != "bob" {
		t.Fatalf("après redémarrage : %+v", c)
	}
	if out, _ := s2.SetSettings(map[string]string{"theme": "light"}); out[settingsKey] != "" {
		t.Fatal("la clé ne doit jamais être renvoyée à l'interface")
	}
	s3, _ := OpenStore(dir)
	if g, _ := s3.Get("a1"); g.Right.SFTP.Host != "serveur.madatech.info" {
		t.Fatal("clé perdue après un changement de réglage")
	}
}

func TestDataDBWrongKey(t *testing.T) {
	dir := t.TempDir()
	s, _ := OpenStore(dir)
	s.Save(Job{Name: "J", Left: Endpoint{Type: "local", Path: dir}, Right: Endpoint{Type: "local", Path: t.TempDir()}, Options: syncer.Options{Mode: syncer.Mirror}})
	if _, err := OpenStore(dir); err != nil {
		t.Fatal(err)
	}
	// autre clé : refus clair, data.db intact
	os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{}`), 0o600)
	before, _ := os.ReadFile(filepath.Join(dir, "data.db"))
	s2, err := OpenStore(dir)
	if err != nil || !strings.Contains(s2.Warning, "data.db") || len(s2.List()) != 0 {
		t.Fatalf("démarrage attendu avec un avertissement : %v %q", err, s2.Warning)
	}
	aside, _ := filepath.Glob(filepath.Join(dir, "data.db.illisible-*"))
	if len(aside) != 1 {
		t.Fatal("ancien data.db non mis de côté")
	}
	if kept, _ := os.ReadFile(aside[0]); !bytes.Equal(before, kept) {
		t.Fatal("ancien data.db modifié")
	}
}

func TestGroups(t *testing.T) {
	dir := t.TempDir()
	s, _ := OpenStore(dir)
	mk := func(n string) Job {
		j, err := s.Save(Job{Name: n, Left: Endpoint{Type: "local", Path: dir}, Right: Endpoint{Type: "local", Path: t.TempDir()}, Options: syncer.Options{Mode: syncer.Mirror}})
		if err != nil {
			t.Fatal(err)
		}
		return j
	}
	a, b := mk("A"), mk("B")
	if err := s.EditGroup("create", "Vers 1To", ""); err != nil {
		t.Fatal(err)
	}
	if s.EditGroup("create", "tout", "") == nil || s.EditGroup("create", "vers 1to", "") == nil {
		t.Fatal("doublons acceptés")
	}
	s.JobGroup(a.ID, "add", "Vers 1To")
	// le formulaire ne fait pas perdre le groupe
	a2, _ := s.Get(a.ID)
	a2.Name = "A modifié"
	s.Save(a2)
	s.EditGroup("rename", "Vers 1To", "Vers Home")
	s2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if g, _ := s2.Get(a.ID); len(g.Groups) != 1 || g.Groups[0] != "Vers Home" || g.Name != "A modifié" {
		t.Fatalf("après relecture : %+v", g)
	}
	if gs := s2.Groups(); len(gs) != 1 || gs[0] != "Vers Home" {
		t.Fatal(gs)
	}
	s2.EditGroup("delete", "Vers Home", "")
	if g, _ := s2.Get(a.ID); len(g.Groups) != 0 {
		t.Fatal("la tâche doit revenir dans Tout")
	}
	if len(s2.List()) != 2 || b.ID == "" {
		t.Fatal("supprimer un groupe ne supprime pas ses tâches")
	}
}

func TestStateEncrypted(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "state"), 0o700)
	// ancien état en clair : converti à l'ouverture
	old := syncer.State{"Photos/vacances.jpg": {Size: 123}}
	b, _ := json.Marshal(old)
	os.WriteFile(filepath.Join(dir, "state", "ancien.json"), b, 0o600)
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "state", "ancien.json")); !os.IsNotExist(err) {
		t.Fatal("ancien état en clair resté")
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "state", "ancien.state"))
	if len(raw) == 0 || bytes.Contains(raw, []byte("vacances")) {
		t.Fatal("état absent ou lisible en clair")
	}
	if st := s.LoadState("ancien"); st["Photos/vacances.jpg"].Size != 123 {
		t.Fatalf("relu : %+v", st)
	}
	// gros état : compressé avant chiffrement
	big := syncer.State{}
	for i := 0; i < 20000; i++ {
		big[fmt.Sprintf("Documents/Projets/dossier-%d/fichier-%d.txt", i%300, i)] = syncer.FileState{Size: int64(i)}
	}
	s.SaveState("gros", big)
	js, _ := json.Marshal(big)
	enc, _ := os.ReadFile(filepath.Join(dir, "state", "gros.state"))
	if len(enc)*3 > len(js) {
		t.Fatalf("pas assez compressé : %d o chiffré pour %d o de JSON", len(enc), len(js))
	}
	if st := s.LoadState("gros"); len(st) != 20000 {
		t.Fatal("gros état mal relu")
	}
}

func TestMultiGroupsAndSchedules(t *testing.T) {
	dir := t.TempDir()
	s, _ := OpenStore(dir)
	j, _ := s.Save(Job{Name: "A", Left: Endpoint{Type: "local", Path: dir}, Right: Endpoint{Type: "local", Path: t.TempDir()}, Options: syncer.Options{Mode: syncer.Mirror}})
	s.EditGroup("create", "Vers 1To", "")
	s.EditGroup("create", "Vers Home", "")
	s.JobGroup(j.ID, "add", "Vers 1To")
	s.JobGroup(j.ID, "add", "Vers Home")
	s.JobGroup(j.ID, "add", "Vers Home") // pas de doublon
	if g, _ := s.Get(j.ID); len(g.Groups) != 2 {
		t.Fatalf("groupes : %v", g.Groups)
	}
	s.JobGroup(j.ID, "remove", "Vers 1To")
	if g, _ := s.Get(j.ID); len(g.Groups) != 1 || g.Groups[0] != "Vers Home" {
		t.Fatalf("après retrait : %v", g.Groups)
	}
	// planifications
	now := time.Now()
	if _, err := s.AddSchedule(Schedule{Kind: "job", Ref: j.ID, At: now.Add(-time.Hour)}); err == nil {
		t.Fatal("date passée acceptée")
	}
	p1, err := s.AddSchedule(Schedule{Kind: "job", Ref: j.ID, At: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	p2, _ := s.AddSchedule(Schedule{Kind: "group", Ref: "Vers Home", At: now.Add(2 * time.Minute), Repeat: "daily"})
	s.AddSchedule(Schedule{Kind: "group", Ref: GroupAll, At: now.Add(3 * time.Minute), Repeat: "weekly"})
	if due := s.duePlans(now.Add(90 * time.Second)); len(due) != 1 || due[0].ID != p1.ID {
		t.Fatalf("dues : %+v", due)
	}
	if due := s.duePlans(now.Add(150 * time.Second)); len(due) != 1 || due[0].ID != p2.ID {
		t.Fatalf("dues (2) : %+v", due)
	}
	for _, p := range s.Schedules() {
		switch p.ID {
		case p1.ID:
			if p.Enabled {
				t.Fatal("planification unique encore active")
			}
		case p2.ID:
			if !p.Enabled || p.At.Sub(p2.At) != 24*time.Hour {
				t.Fatalf("répétition : %v → %v", p2.At, p.At)
			}
		}
	}
	// renommer le groupe suit dans les planifications ; tout est relu après redémarrage
	s.EditGroup("rename", "Vers Home", "Maison")
	s2, _ := OpenStore(dir)
	found := false
	for _, p := range s2.Schedules() {
		found = found || (p.Kind == "group" && p.Ref == "Maison")
	}
	if !found || len(s2.Schedules()) != 3 {
		t.Fatalf("après redémarrage : %+v", s2.Schedules())
	}
	// supprimer la tâche supprime ses planifications
	s2.Delete(j.ID)
	if n := len(s2.Schedules()); n != 2 {
		t.Fatalf("planifications restantes : %d", n)
	}
	// retard : manqué au-delà de 12 h
	s2.plans[0].At, s2.plans[0].Enabled = now.Add(-13*time.Hour), true
	if due := s2.duePlans(now); len(due) != 0 || !strings.Contains(s2.plans[0].Note, "manqué") {
		t.Fatalf("retard : %+v %q", due, s2.plans[0].Note)
	}
}

func TestOldSingleGroupMigrated(t *testing.T) {
	dir := t.TempDir()
	old := `[{"id":"a1","name":"Vieux","left":{"type":"local","path":"/tmp"},"right":{"type":"local","path":"/tmp/x"},"options":{"mode":"mirror"},"group":"G"}]`
	os.WriteFile(filepath.Join(dir, "jobs.json"), []byte(old), 0o600)
	s, _ := OpenStore(dir)
	if g, _ := s.Get("a1"); len(g.Groups) != 1 || g.Groups[0] != "G" || g.Group != "" {
		t.Fatalf("%+v", g)
	}
}

// Pause et vitesse acceptées dès le lancement (avant les copies, pendant la
// lecture des deux côtés) : rien n'est copié tant que la pause dure.
func TestPauseAndSpeedBeforeCopies(t *testing.T) {
	l, rdir := t.TempDir(), t.TempDir()
	for i := 0; i < 20; i++ {
		put(t, l, fmt.Sprintf("f%02d.txt", i), "x", time.Now())
	}
	store, _ := OpenStore(t.TempDir())
	j, _ := store.Save(Job{Name: "P", Left: Endpoint{Type: "local", Path: l}, Right: Endpoint{Type: "local", Path: rdir}, Options: syncer.Options{Mode: syncer.Mirror}})
	r := NewRunner(store)
	if err := r.Start(j.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := r.Pause(j.ID); err != nil { // tout de suite, avant toute copie
		t.Fatalf("pause refusée au lancement : %v", err)
	}
	if err := r.SetSpeed(j.ID, 300); err != nil {
		t.Fatalf("vitesse refusée au lancement : %v", err)
	}
	time.Sleep(400 * time.Millisecond)
	if n, _ := os.ReadDir(rdir); len(n) != 0 {
		t.Fatalf("%d fichier(s) copiés pendant la pause", len(n))
	}
	if err := r.Resume(j.ID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 500 && r.Busy(j.ID); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if n, _ := os.ReadDir(rdir); len(n) != 20 {
		t.Fatalf("%d fichier(s) copiés après la reprise, 20 attendus", len(n))
	}
	if got, _ := store.Get(j.ID); got.Options.MaxKBps != 0 {
		t.Log("(SetSpeed du moteur ne touche pas la tâche : c'est l'API qui l'enregistre)")
	}
}
