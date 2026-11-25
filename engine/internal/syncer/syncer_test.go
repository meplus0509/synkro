package syncer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/sftp"

	"synkro/internal/vfs"
)

func write(t *testing.T, root, rel, content string, mt time.Time) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(p, mt, mt)
}

func read(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return "<absent>"
	}
	return string(b)
}

func exists(root, rel string) bool {
	_, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
	return err == nil
}

// run analyse puis exécute un passage complet.
func run(t *testing.T, l, r vfs.FS, st State, opt Options) (State, *Plan) {
	t.Helper()
	ls, err := Scan(context.Background(), l, opt.Exclude, nil)
	if err != nil {
		t.Fatal(err)
	}
	rs, err := Scan(context.Background(), r, opt.Exclude, nil)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan(ls, rs, st, opt)
	if err != nil {
		t.Fatal(err)
	}
	ns, res := Execute(context.Background(), plan, l, r, st, opt, nil, nil)
	for _, x := range res {
		if x.Err != "" {
			t.Errorf("action %s %s : %s", x.Kind, x.Path, x.Err)
		}
	}
	return ns, plan
}

func locals(t *testing.T) (string, string, vfs.FS, vfs.FS) {
	a, b := t.TempDir(), t.TempDir()
	la, _ := vfs.NewLocal(a)
	lb, _ := vfs.NewLocal(b)
	return a, b, la, lb
}

var t0 = time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC)

func TestTwoWayScenario(t *testing.T) {
	a, b, L, R := locals(t)
	opt := Options{Mode: TwoWay, UseTrash: true, Exclude: []string{"*.tmp"}}
	write(t, a, "docs/rapport.txt", "v1", t0)
	write(t, a, "photo.jpg", "img", t0)
	write(t, b, "notes.md", "notes", t0)
	write(t, a, "cache.tmp", "x", t0)

	st, _ := run(t, L, R, State{}, opt)
	if read(t, b, "docs/rapport.txt") != "v1" || read(t, a, "notes.md") != "notes" {
		t.Fatal("premier passage incomplet")
	}
	if exists(b, "cache.tmp") {
		t.Fatal("fichier exclu copié")
	}

	// modification à droite, suppression à gauche, nouveau dossier vide à droite
	write(t, b, "docs/rapport.txt", "v2", t0.Add(time.Hour))
	os.Remove(filepath.Join(a, "photo.jpg"))
	os.MkdirAll(filepath.Join(b, "vide"), 0o755)
	st, plan := run(t, L, R, st, opt)
	if read(t, a, "docs/rapport.txt") != "v2" {
		t.Fatal("modif droite non propagée")
	}
	if exists(b, "photo.jpg") {
		t.Fatal("suppression non propagée")
	}
	if !exists(a, "vide") {
		t.Fatal("dossier vide non créé")
	}
	if plan.Conflicts != 0 {
		t.Fatalf("conflits inattendus : %d", plan.Conflicts)
	}
	// la version supprimée est dans la corbeille
	entries, _ := os.ReadDir(filepath.Join(b, TrashDir))
	if len(entries) != 1 {
		t.Fatal("corbeille absente")
	}

	// conflit : les deux côtés modifiés, la droite est plus récente
	write(t, a, "notes.md", "gauche", t0.Add(2*time.Hour))
	write(t, b, "notes.md", "droite!", t0.Add(3*time.Hour))
	st, plan = run(t, L, R, st, opt)
	if plan.Conflicts != 1 || read(t, a, "notes.md") != "droite!" {
		t.Fatalf("conflit mal résolu : %d %q", plan.Conflicts, read(t, a, "notes.md"))
	}

	// stabilité : un nouveau passage ne fait rien
	_, plan = run(t, L, R, st, opt)
	if len(plan.Actions) != 0 {
		t.Fatalf("passage à vide attendu, obtenu %+v", plan.Actions)
	}
}

func TestDeleteDirKeepsModifiedContent(t *testing.T) {
	a, b, L, R := locals(t)
	opt := Options{Mode: TwoWay}
	write(t, a, "projet/a.txt", "a", t0)
	write(t, a, "projet/b.txt", "b", t0)
	write(t, a, "autre.txt", "reste en place", t0)
	st, _ := run(t, L, R, State{}, opt)

	// suppression du dossier à droite, mais b.txt modifié à gauche
	os.RemoveAll(filepath.Join(b, "projet"))
	write(t, a, "projet/b.txt", "b modifié", t0.Add(time.Hour))
	st, _ = run(t, L, R, st, opt)
	if exists(a, "projet/a.txt") {
		t.Fatal("a.txt aurait dû être supprimé")
	}
	if read(t, b, "projet/b.txt") != "b modifié" {
		t.Fatal("b.txt modifié aurait dû être conservé et recopié")
	}

	// suppression complète du dossier à droite : disparaît à gauche
	os.RemoveAll(filepath.Join(b, "projet"))
	run(t, L, R, st, Options{Mode: TwoWay, AllowEmptySide: true})
	if exists(a, "projet") {
		t.Fatal("dossier non supprimé")
	}
}

func TestGuardEmptySide(t *testing.T) {
	_, b, L, R := locals(t)
	write(t, b, "x.txt", "x", t0)
	opt := Options{Mode: TwoWay}
	st, _ := run(t, L, R, State{}, opt)
	os.Remove(filepath.Join(b, "x.txt"))
	ls, _ := Scan(context.Background(), L, nil, nil)
	rs, _ := Scan(context.Background(), R, nil, nil)
	if _, err := BuildPlan(ls, rs, st, opt); err == nil || !strings.Contains(err.Error(), "garde-fou") {
		t.Fatalf("garde-fou attendu, obtenu %v", err)
	}
}

func TestMirrorAndBackup(t *testing.T) {
	a, b, L, R := locals(t)
	write(t, a, "src.txt", "source", t0)
	write(t, b, "extra.txt", "en trop", t0)

	run(t, L, R, State{}, Options{Mode: Backup})
	if !exists(b, "extra.txt") || read(t, b, "src.txt") != "source" {
		t.Fatal("backup incorrect")
	}
	run(t, L, R, State{}, Options{Mode: Mirror})
	if exists(b, "extra.txt") {
		t.Fatal("le miroir doit supprimer les fichiers en trop")
	}
	if exists(a, "extra.txt") {
		t.Fatal("le miroir ne doit jamais écrire à gauche")
	}
}

func TestExcluded(t *testing.T) {
	cases := map[string]bool{
		"a/node_modules/x.js":  true,
		"a/b.tmp":              true,
		"build/out.bin":        true,
		"src/build/out.bin":    false,
		"docs/x.pdf":           true,
		"docs/sub/x.pdf":       false,
		".synkro-trash/x":      true,
		"$RECYCLE.BIN/S-1-5/x": true,
		"System Volume Information/IndexerVolumeGuid": true,
		"photos/.Trashes": true,
		"ok.txt":          false,
	}
	pats := []string{"node_modules", "*.tmp", "/build", "docs/*.pdf"}
	for p, want := range cases {
		if got := Excluded(p, pats); got != want {
			t.Errorf("Excluded(%q) = %v, attendu %v", p, got, want)
		}
	}
}

// TestSFTPBackend fait tourner la synchro contre un vrai serveur SFTP en mémoire (via un tube).
func TestSFTPBackend(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	cr, sw := io.Pipe()
	sr, cw := io.Pipe()
	srv, err := sftp.NewServer(struct {
		io.Reader
		io.WriteCloser
	}{sr, sw}, sftp.WithServerWorkingDirectory(b))
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	cl, err := sftp.NewClientPipe(cr, cw)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cw.Close(); sw.Close(); cr.Close(); sr.Close() }()
	R, err := vfs.NewSFTPFromClient(cl, b)
	if err != nil {
		t.Fatal(err)
	}
	L, _ := vfs.NewLocal(a)

	write(t, a, "dossier/gros.bin", strings.Repeat("z", 300_000), t0)
	write(t, b, "distant.txt", "depuis le serveur", t0)
	opt := Options{Mode: TwoWay, UseTrash: true}
	st, _ := run(t, L, R, State{}, opt)
	if read(t, b, "dossier/gros.bin") != strings.Repeat("z", 300_000) || read(t, a, "distant.txt") != "depuis le serveur" {
		t.Fatal("synchro SFTP incorrecte")
	}
	write(t, a, "distant.txt", "modifié localement", t0.Add(time.Hour))
	st, _ = run(t, L, R, st, opt)
	if read(t, b, "distant.txt") != "modifié localement" {
		t.Fatal("écrasement SFTP incorrect")
	}
	_, plan := run(t, L, R, st, opt)
	if len(plan.Actions) != 0 {
		t.Fatalf("passage à vide attendu sur SFTP, obtenu %+v", plan.Actions)
	}
}

// slowReader simule la latence réseau : chaque paquet reçu par le serveur est retardé.
// latencyReader livre chaque bloc reçu d avec un retard fixe, sans bloquer les blocs
// suivants (comme un vrai réseau : latence, pas débit).
type latencyReader struct {
	ch  chan latChunk
	buf []byte
}

type latChunk struct {
	data []byte
	at   time.Time
}

func newLatencyReader(r io.Reader, d time.Duration) *latencyReader {
	lr := &latencyReader{ch: make(chan latChunk, 4096)}
	go func() {
		defer close(lr.ch)
		for {
			b := make([]byte, 32*1024)
			n, err := r.Read(b)
			if n > 0 {
				lr.ch <- latChunk{b[:n], time.Now().Add(d)}
			}
			if err != nil {
				return
			}
		}
	}()
	return lr
}

func (l *latencyReader) Read(p []byte) (int, error) {
	if len(l.buf) == 0 {
		c, ok := <-l.ch
		if !ok {
			return 0, io.EOF
		}
		time.Sleep(time.Until(c.at))
		l.buf = c.data
	}
	n := copy(p, l.buf)
	l.buf = l.buf[n:]
	return n, nil
}

func sftpPair(t *testing.T, root string, latency time.Duration) vfs.FS {
	cr, sw := io.Pipe()
	sr, cw := io.Pipe()
	srv, _ := sftp.NewServer(struct {
		io.Reader
		io.WriteCloser
	}{newLatencyReader(sr, latency), sw}, sftp.WithServerWorkingDirectory(root))
	go srv.Serve()
	cl, err := sftp.NewClientPipe(cr, cw, sftp.UseConcurrentWrites(true)) // comme DialSFTP
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cw.Close(); sw.Close(); cr.Close(); sr.Close() })
	fs, err := vfs.NewSFTPFromClient(cl, root)
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

// Arborescence de 300 dossiers avec latence : le parcours parallèle doit tout voir, vite,
// et l'annulation doit l'interrompre.
func TestSFTPWalkManyDirs(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 30; i++ {
		for j := 0; j < 10; j++ {
			write(t, root, fmt.Sprintf("d%02d/s%d/f.txt", i, j), "x", t0)
		}
	}
	write(t, root, "node_modules/x/y.js", "x", t0)
	fs := sftpPair(t, root, 10*time.Millisecond) // latence réseau simulée
	var durations []time.Duration
	for _, workers := range []int{1, 16} {
		vfs.SetWalkWorkers(workers)
		start := time.Now()
		got, err := Scan(context.Background(), fs, []string{"node_modules"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		// 30 dossiers + 300 sous-dossiers + 300 fichiers
		if len(got) != 630 {
			t.Fatalf("attendu 630 éléments, obtenu %d", len(got))
		}
		durations = append(durations, time.Since(start))
		t.Logf("parcours SFTP de 331 dossiers avec %2d lecture(s) en parallèle : %v", workers, time.Since(start).Round(time.Millisecond))
	}
	vfs.SetWalkWorkers(16)
	if durations[1]*3 > durations[0] {
		t.Fatalf("le parcours parallèle devrait être nettement plus rapide : %v contre %v", durations[1], durations[0])
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Scan(ctx, fs, nil, nil); err == nil {
		t.Fatal("l'annulation aurait dû interrompre le parcours")
	}
}

// Beaucoup de petits fichiers vers SFTP avec latence : les transferts parallèles
// doivent tout copier correctement et nettement plus vite.
func TestSFTPManySmallFilesParallel(t *testing.T) {
	var durations []time.Duration
	for _, workers := range []int{1, 8} {
		a, b := t.TempDir(), t.TempDir()
		for i := 0; i < 20; i++ {
			for j := 0; j < 10; j++ {
				write(t, a, fmt.Sprintf("p%02d/f%d.txt", i, j), fmt.Sprintf("contenu %d-%d", i, j), t0)
			}
		}
		L, _ := vfs.NewLocal(a)
		R := sftpPair(t, b, 10*time.Millisecond)
		SetTransferWorkers(workers)
		start := time.Now()
		st, plan := run(t, L, R, State{}, Options{Mode: Mirror, UseTrash: true})
		durations = append(durations, time.Since(start))
		t.Logf("200 petits fichiers + 20 dossiers, %d transfert(s) en parallèle : %v", workers, time.Since(start).Round(time.Millisecond))
		if len(plan.Actions) != 220 {
			t.Fatalf("attendu 220 actions, obtenu %d", len(plan.Actions))
		}
		for i := 0; i < 20; i++ {
			for j := 0; j < 10; j++ {
				if got := read(t, b, fmt.Sprintf("p%02d/f%d.txt", i, j)); got != fmt.Sprintf("contenu %d-%d", i, j) {
					t.Fatalf("p%02d/f%d.txt : %q", i, j, got)
				}
			}
		}
		// modification + nouveau passage : écrasement avec corbeille, en parallèle
		write(t, a, "p03/f1.txt", "modifié", t0.Add(time.Hour))
		_, plan = run(t, L, R, st, Options{Mode: Mirror, UseTrash: true})
		if len(plan.Actions) != 1 || read(t, b, "p03/f1.txt") != "modifié" {
			t.Fatalf("second passage incorrect : %+v", plan.Actions)
		}
	}
	SetTransferWorkers(8)
	if durations[1]*3 > durations[0] {
		t.Fatalf("les transferts parallèles devraient être nettement plus rapides : %v contre %v", durations[1], durations[0])
	}
}

// Gros fichier dans les deux sens avec latence : vérifie le contenu et mesure la durée.
func TestSFTPBigFile(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	big := strings.Repeat("0123456789abcdef", 1<<19) // 8 Mo
	write(t, a, "gros.bin", big, t0)
	write(t, b, "distant.bin", big[:4<<20], t0)
	L, _ := vfs.NewLocal(a)
	R := sftpPair(t, b, 10*time.Millisecond)
	start := time.Now()
	run(t, L, R, State{}, Options{Mode: TwoWay})
	t.Logf("8 Mo envoyés + 4 Mo reçus en SFTP : %v", time.Since(start).Round(time.Millisecond))
	if read(t, b, "gros.bin") != big || read(t, a, "distant.bin") != big[:4<<20] {
		t.Fatal("contenu des gros fichiers incorrect")
	}
}

// blockingFS simule un serveur où l'envoi d'un fichier précis ne progresse plus.
type blockingFS struct {
	vfs.FS
	stuck string
}

type blockedWriter struct{ closed chan struct{} }

func (b *blockedWriter) Write(p []byte) (int, error) {
	<-b.closed // bloqué jusqu'à la fermeture par le chien de garde
	return 0, errors.New("connexion fermée")
}
func (b *blockedWriter) Close() error {
	select {
	case <-b.closed:
	default:
		close(b.closed)
	}
	return nil
}

func (f blockingFS) Create(p string) (io.WriteCloser, error) {
	if strings.HasSuffix(p, f.stuck) {
		return &blockedWriter{closed: make(chan struct{})}, nil
	}
	return f.FS.Create(p)
}

// Un fichier bloqué est abandonné (échec) et les autres sont quand même copiés.
func TestStalledFileIsSkipped(t *testing.T) {
	old := StallTimeout
	StallTimeout = 300 * time.Millisecond
	defer func() { StallTimeout = old }()
	a, b, L, R := locals(t)
	write(t, a, "bloque.bin", "contenu qui ne passera pas", t0)
	for i := 0; i < 5; i++ {
		write(t, a, fmt.Sprintf("ok%d.txt", i), "ok", t0)
	}
	R = blockingFS{R, "bloque.bin"}
	ls, _ := Scan(context.Background(), L, nil, nil)
	rs, _ := Scan(context.Background(), R, nil, nil)
	plan, err := BuildPlan(ls, rs, State{}, Options{Mode: Backup})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	st, res := Execute(context.Background(), plan, L, R, State{}, Options{Mode: Backup}, nil, nil)
	if time.Since(start) > 5*time.Second {
		t.Fatal("le fichier bloqué a retenu la synchro trop longtemps")
	}
	var failed []string
	for _, r := range res {
		if r.Err != "" {
			failed = append(failed, r.Path+" : "+r.Err)
		}
	}
	if len(failed) != 1 || !strings.Contains(failed[0], "bloqué") {
		t.Fatalf("attendu un seul échec « bloqué », obtenu %v", failed)
	}
	for i := 0; i < 5; i++ {
		if read(t, b, fmt.Sprintf("ok%d.txt", i)) != "ok" {
			t.Fatalf("ok%d.txt aurait dû être copié malgré l'échec", i)
		}
	}
	if _, ok := st["bloque.bin"]; ok {
		t.Fatal("le fichier en échec ne doit pas être mémorisé comme synchronisé (il sera retenté)")
	}
	t.Logf("échec enregistré : %s", failed[0])
}

// Miroir : un répertoire (avec sous-dossiers et fichiers) présent seulement à droite
// doit disparaître entièrement, avec ou sans corbeille. En sauvegarde, il reste.
func TestMirrorDeletesExtraDirectory(t *testing.T) {
	for _, trash := range []bool{true, false} {
		a, b, L, R := locals(t)
		write(t, a, "garde.txt", "source", t0)
		write(t, b, "en-trop/sous/fichier.txt", "x", t0)
		write(t, b, "en-trop/autre.txt", "y", t0)
		os.MkdirAll(filepath.Join(b, "en-trop/vide"), 0o755)

		_, plan := run(t, L, R, State{}, Options{Mode: Backup, UseTrash: trash})
		if !exists(b, "en-trop/sous/fichier.txt") {
			t.Fatal("la sauvegarde ne doit rien supprimer à droite")
		}
		if plan.Unchanged != 0 || len(plan.Same) != 0 {
			t.Fatalf("aucun élément identique attendu, obtenu %d", plan.Unchanged)
		}
		_, plan = run(t, L, R, State{}, Options{Mode: Mirror, UseTrash: trash})
		if exists(b, "en-trop") {
			t.Fatalf("corbeille=%v : le répertoire en trop aurait dû être supprimé", trash)
		}
		if read(t, b, "garde.txt") != "source" {
			t.Fatal("garde.txt devait rester")
		}
		if len(plan.Same) != 1 || plan.Same[0].Path != "garde.txt" || plan.Same[0].Kind != Same {
			t.Fatalf("garde.txt devait être listé comme identique : %+v", plan.Same)
		}
		if trash && read(t, b, filepath.ToSlash(findTrashed(t, b, "fichier.txt"))) != "x" {
			t.Fatal("le fichier supprimé devait être dans la corbeille")
		}
	}
}

// findTrashed renvoie le chemin relatif d'un fichier dans la corbeille.
func findTrashed(t *testing.T, root, name string) string {
	var found string
	filepath.Walk(filepath.Join(root, TrashDir), func(p string, info os.FileInfo, err error) error {
		if err == nil && info.Name() == name {
			found, _ = filepath.Rel(root, p)
		}
		return nil
	})
	if found == "" {
		t.Fatalf("%s introuvable dans la corbeille", name)
	}
	return found
}

// Conflits : chaque choix (gauche, droite, ne rien faire) produit le bon résultat,
// y compris pour un fichier modifié d'un côté et supprimé de l'autre.
func TestResolveConflicts(t *testing.T) {
	a, b, L, R := locals(t)
	opt := Options{Mode: TwoWay}
	for _, f := range []string{"g.txt", "d.txt", "rien.txt", "supprime.txt"} {
		write(t, a, f, "base", t0)
	}
	st, _ := run(t, L, R, State{}, opt)
	// modifié des deux côtés (la droite est la plus récente)
	for _, f := range []string{"g.txt", "d.txt", "rien.txt"} {
		write(t, a, f, "gauche", t0.Add(time.Hour))
		write(t, b, f, "droite!", t0.Add(2*time.Hour))
	}
	// modifié à gauche, supprimé à droite
	write(t, a, "supprime.txt", "modifié à gauche", t0.Add(time.Hour))
	os.Remove(filepath.Join(b, "supprime.txt"))

	ls, _ := Scan(context.Background(), L, nil, nil)
	rs, _ := Scan(context.Background(), R, nil, nil)
	plan, err := BuildPlan(ls, rs, st, opt)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, x := range plan.Actions {
		if IsChoosable(x) {
			n++
			if x.Path == "supprime.txt" && (x.Right.Exists || !x.Left.Exists) {
				t.Fatal("supprime.txt : la droite doit apparaître comme supprimée")
			}
			if x.Path == "g.txt" && DefaultChoice(x, "newer") != ChooseRight {
				t.Fatal("par défaut (plus récente), la droite devait être proposée")
			}
		}
	}
	if n != 4 {
		t.Fatalf("4 conflits attendus, obtenu %d", n)
	}
	choices := map[string]string{"g.txt": ChooseLeft, "d.txt": ChooseRight, "rien.txt": ChooseLeave, "supprime.txt": ChooseRight}
	ResolveConflicts(plan, func(x Action) string { return choices[x.Path] })
	ns, res := Execute(context.Background(), plan, L, R, st, opt, nil, nil)
	for _, r := range res {
		if r.Err != "" {
			t.Fatalf("%s : %s", r.Path, r.Err)
		}
	}
	if read(t, b, "g.txt") != "gauche" || read(t, a, "d.txt") != "droite!" {
		t.Fatal("les versions choisies n'ont pas été appliquées")
	}
	if read(t, a, "rien.txt") != "gauche" || read(t, b, "rien.txt") != "droite!" {
		t.Fatal("« ne rien faire » doit laisser les deux versions telles quelles")
	}
	if exists(a, "supprime.txt") {
		t.Fatal("choisir la droite (supprimé) devait supprimer à gauche")
	}
	if ns["rien.txt"] != st["rien.txt"] {
		t.Fatal("un conflit laissé tel quel garde son ancien état (il sera reproposé)")
	}
	// au passage suivant, seul le conflit laissé de côté revient
	ls, _ = Scan(context.Background(), L, nil, nil)
	rs, _ = Scan(context.Background(), R, nil, nil)
	plan, _ = BuildPlan(ls, rs, ns, opt)
	if len(plan.Actions) != 1 || plan.Actions[0].Path != "rien.txt" || !IsChoosable(plan.Actions[0]) {
		t.Fatalf("seul rien.txt devait rester en conflit : %+v", plan.Actions)
	}
}

// Sens choisis à la main : copie inversée, suppression évitée, élément ignoré.
func TestApplyOverrides(t *testing.T) {
	a, b, L, R := locals(t)
	write(t, a, "nouveau.txt", "gauche", t0)                 // → prévu, forcé ←? absent à droite : supprimer à gauche
	write(t, a, "modif.txt", "gauche v2", t0.Add(time.Hour)) // → prévu, forcé ← : la droite gagne
	write(t, b, "modif.txt", "droite", t0)
	write(t, b, "en-trop/garde.txt", "à garder", t0) // suppression prévue (miroir), forcée « rien »
	write(t, b, "en-trop/jette.txt", "x", t0)
	write(t, a, "ignore.txt", "g", t0) // → prévu, forcé « rien »

	ls, _ := Scan(context.Background(), L, nil, nil)
	rs, _ := Scan(context.Background(), R, nil, nil)
	opt := Options{Mode: Mirror}
	plan, err := BuildPlan(ls, rs, State{}, opt)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range plan.Actions {
		if x.Left == nil || x.Right == nil {
			t.Fatalf("côtés manquants pour %s", x.Path)
		}
	}
	ApplyOverrides(plan, map[string]string{
		"nouveau.txt": DirToLeft, "modif.txt": DirToLeft,
		"en-trop/garde.txt": DirNone, "ignore.txt": DirNone,
	})
	kinds := map[string]Kind{}
	for _, x := range plan.Actions {
		kinds[x.Path] = x.Kind
	}
	want := map[string]Kind{"nouveau.txt": DelL, "modif.txt": CopyRL, "en-trop/garde.txt": Leave,
		"en-trop/jette.txt": DelR, "en-trop": Leave, "ignore.txt": Leave}
	for p, k := range want {
		if kinds[p] != k {
			t.Errorf("%s : %s attendu, obtenu %s", p, k, kinds[p])
		}
	}
	_, res := Execute(context.Background(), plan, L, R, State{}, opt, nil, nil)
	for _, x := range res {
		if x.Err != "" {
			t.Errorf("%s %s : %s", x.Kind, x.Path, x.Err)
		}
	}
	if read(t, a, "modif.txt") != "droite" || exists(a, "nouveau.txt") || !exists(b, "en-trop/garde.txt") ||
		exists(b, "en-trop/jette.txt") || exists(b, "ignore.txt") {
		t.Fatal("résultat inattendu après les choix manuels")
	}
}

// Pause : rien ne se copie pendant la pause, tout se termine après la reprise.
func TestPauseResume(t *testing.T) {
	a, b, L, R := locals(t)
	for i := 0; i < 20; i++ {
		write(t, a, fmt.Sprintf("f%02d.txt", i), "x", t0)
	}
	ls, _ := Scan(context.Background(), L, nil, nil)
	rs, _ := Scan(context.Background(), R, nil, nil)
	plan, _ := BuildPlan(ls, rs, State{}, Options{Mode: Mirror})
	g := &Gate{}
	g.Pause()
	done := make(chan []Result)
	go func() {
		_, res := Execute(WithGate(context.Background(), g), plan, L, R, State{}, Options{Mode: Mirror}, nil, nil)
		done <- res
	}()
	time.Sleep(300 * time.Millisecond)
	if n, _ := os.ReadDir(b); len(n) != 0 {
		t.Fatalf("des fichiers ont été copiés pendant la pause : %d", len(n))
	}
	g.Resume()
	select {
	case res := <-done:
		for _, r := range res {
			if r.Err != "" {
				t.Fatalf("%s : %s", r.Path, r.Err)
			}
		}
	case <-time.After(10 * time.Second):
		t.Fatal("la synchro n'a pas repris")
	}
	if n, _ := os.ReadDir(b); len(n) != 20 {
		t.Fatalf("20 fichiers attendus après la reprise, obtenu %d", len(n))
	}
}

// Limite de vitesse : 300 Ko à 100 Ko/s prennent au moins ~2 s (1 s d'avance permise).
func TestSpeedLimit(t *testing.T) {
	a, _, L, R := locals(t)
	write(t, a, "gros.bin", strings.Repeat("x", 300<<10), t0)
	ls, _ := Scan(context.Background(), L, nil, nil)
	rs, _ := Scan(context.Background(), R, nil, nil)
	opt := Options{Mode: Mirror, MaxKBps: 100}
	plan, _ := BuildPlan(ls, rs, State{}, opt)
	start := time.Now()
	_, res := Execute(context.Background(), plan, L, R, State{}, opt, nil, nil)
	if res[0].Err != "" {
		t.Fatal(res[0].Err)
	}
	if d := time.Since(start); d < 1500*time.Millisecond {
		t.Fatalf("la limite de vitesse n'est pas respectée : %v", d)
	}
}

// Reconnexion : la destination disparaît (clé débranchée) puis revient ; la copie reprend.
func TestRetryAfterDisconnect(t *testing.T) {
	old := retryPause
	retryPause = 100 * time.Millisecond
	defer func() { retryPause = old }()
	run := func(window int, comeBack bool) []Result {
		a, b, L, R := locals(t)
		write(t, a, "f.txt", "contenu", t0)
		ls, _ := Scan(context.Background(), L, nil, nil)
		rs, _ := Scan(context.Background(), R, nil, nil)
		opt := Options{Mode: Mirror, RetrySeconds: window}
		plan, _ := BuildPlan(ls, rs, State{}, opt)
		gone := b + "-debranchee"
		if err := os.Rename(b, gone); err != nil {
			t.Fatal(err)
		}
		if comeBack {
			go func() { time.Sleep(600 * time.Millisecond); os.Rename(gone, b) }()
		}
		_, res := Execute(context.Background(), plan, L, R, State{}, opt, nil, nil)
		return res
	}
	if res := run(10, true); res[0].Err != "" {
		t.Fatalf("la copie devait reprendre après le retour de la destination : %s", res[0].Err)
	}
	if res := run(1, false); !strings.Contains(res[0].Err, "connexion perdue") {
		t.Fatalf("échec attendu après le délai de reconnexion : %q", res[0].Err)
	}
	if res := run(-1, true); res[0].Err == "" || strings.Contains(res[0].Err, "connexion perdue") {
		t.Fatalf("sans reconnexion, échec immédiat attendu : %q", res[0].Err)
	}
}

// « Inclure seulement » : seuls les fichiers retenus (et leurs dossiers) sont synchronisés.
func TestInclude(t *testing.T) {
	a, b, L, R := locals(t)
	write(t, a, "outils/app.exe", "x", t0)
	write(t, a, "outils/notes.txt", "x", t0)
	write(t, a, "docs/guide.pdf", "x", t0)
	write(t, a, "racine.exe", "x", t0)
	ls, _ := Scan(context.Background(), L, nil, nil)
	rs, _ := Scan(context.Background(), R, nil, nil)
	opt := Options{Mode: Mirror, Include: []string{"*.exe", "/docs"}}
	plan, _ := BuildPlan(ls, rs, State{}, opt)
	Execute(context.Background(), plan, L, R, State{}, opt, nil, nil)
	for _, p := range []string{"outils/app.exe", "racine.exe", "docs/guide.pdf"} {
		if !exists(b, p) {
			t.Errorf("%s devait être copié", p)
		}
	}
	if exists(b, "outils/notes.txt") {
		t.Error("notes.txt ne devait pas être copié")
	}
}

// Exclusions automatiques : cachés, temporaires, dossiers vides.
func TestAutoExclusions(t *testing.T) {
	a, b, L, R := locals(t)
	write(t, a, ".cache/x", "x", t0)
	write(t, a, "doc.txt", "x", t0)
	write(t, a, "brouillon.tmp", "x", t0)
	write(t, a, "~$rapport.docx", "x", t0)
	os.MkdirAll(filepath.Join(a, "vide/sous"), 0o755)
	opt := Options{Mode: Mirror, ExcludeHidden: true, ExcludeTemp: true, ExcludeEmptyDirs: true}
	skL := map[string]Skipped{}
	ls, _ := ScanWith(context.Background(), L, opt, nil, skL)
	rs, _ := ScanWith(context.Background(), R, opt, nil, nil)
	plan, _ := BuildPlan(ls, rs, State{}, opt)
	plan.AddExcluded(skL, nil)
	reasons := map[string]string{}
	for _, a := range plan.Same {
		if a.Kind == ExcludedKind {
			reasons[a.Path] = a.Reason
		}
	}
	for p, want := range map[string]string{".cache": "exclu : caché", "brouillon.tmp": "exclu : temporaire", "~$rapport.docx": "exclu : temporaire", "vide": "exclu : dossier vide"} {
		if reasons[p] != want {
			t.Errorf("%s : raison %q, attendu %q", p, reasons[p], want)
		}
	}
	Execute(context.Background(), plan, L, R, State{}, opt, nil, nil)
	if !exists(b, "doc.txt") {
		t.Fatal("doc.txt devait être copié")
	}
	for _, p := range []string{".cache", "brouillon.tmp", "~$rapport.docx", "vide"} {
		if exists(b, p) {
			t.Errorf("%s ne devait pas être copié", p)
		}
	}
}

func TestGatePausedFor(t *testing.T) {
	var g Gate
	g.Pause()
	time.Sleep(60 * time.Millisecond)
	if d := g.PausedFor(); d < 50*time.Millisecond {
		t.Fatalf("pause en cours non comptée : %v", d)
	}
	g.Resume()
	d := g.PausedFor()
	time.Sleep(30 * time.Millisecond)
	if g.PausedFor() != d {
		t.Fatal("le temps continue après la reprise")
	}
}

func TestAlignExcluded(t *testing.T) {
	ls := map[string]vfs.Entry{"a": {Path: "a", IsDir: true}, "a/x": {Path: "a/x"}, "a/y/z": {Path: "a/y/z"}, "b": {Path: "b"}}
	rs := map[string]vfs.Entry{"b": {Path: "b"}, "c": {Path: "c"}}
	skL := map[string]Skipped{"c": {Entry: vfs.Entry{Path: "c"}, Reason: "caché"}}
	skR := map[string]Skipped{"a": {Entry: vfs.Entry{Path: "a", IsDir: true}, Reason: "caché"}}
	AlignExcluded(ls, rs, skL, skR)
	if len(ls) != 1 || len(rs) != 1 {
		t.Fatalf("gauche %v droite %v", ls, rs)
	}
	if skL["a/y/z"].Reason != "caché (autre côté)" {
		t.Fatalf("raison : %+v", skL["a/y/z"])
	}
}

func TestSortFiles(t *testing.T) {
	acts := []Action{{Path: "b/Zèbre.txt", Size: 5}, {Path: "a.txt", Size: 9}, {Path: "B/école.txt", Size: 1}, {Path: "c", Size: 3}}
	order := func(o string) string {
		f := []int{0, 1, 2, 3}
		sortFiles(f, acts, o)
		s := ""
		for _, i := range f {
			s += acts[i].Path + ";"
		}
		return s
	}
	if g := order(""); g != "B/école.txt;c;b/Zèbre.txt;a.txt;" {
		t.Fatal(g)
	}
	if g := order("size-desc"); g != "a.txt;b/Zèbre.txt;c;B/école.txt;" {
		t.Fatal(g)
	}
	if g := order("path"); g != "a.txt;B/école.txt;b/Zèbre.txt;c;" {
		t.Fatal(g)
	}
}

func TestCopyOrderRespected(t *testing.T) {
	old := bigFileSlots
	bigFileSlots = 1
	defer func() { bigFileSlots = old }()
	for _, order := range []string{"size-desc", "path", ""} {
		l, r := t.TempDir(), t.TempDir()
		sizes := map[string]int{"a.bin": 5, "b.bin": 9, "c.bin": 6, "d.bin": 8, "e.bin": 7, "f.bin": 10}
		for n, mb := range sizes {
			os.WriteFile(filepath.Join(l, n), make([]byte, mb<<20), 0o644)
		}
		L, _ := vfs.NewLocal(l)
		R, _ := vfs.NewLocal(r)
		ls, _ := Scan(context.Background(), L, nil, nil)
		rs, _ := Scan(context.Background(), R, nil, nil)
		opt := Options{Mode: Mirror, CopyOrder: order, Parallel: 8}
		plan, _ := BuildPlan(ls, rs, State{}, opt)
		var mu sync.Mutex
		var got []string
		Execute(context.Background(), plan, L, R, State{}, opt, nil, func(i int, res Result) {
			mu.Lock()
			got = append(got, res.Path)
			mu.Unlock()
		})
		want := map[string]string{"size-desc": "f.bin b.bin d.bin e.bin c.bin a.bin", "path": "a.bin b.bin c.bin d.bin e.bin f.bin", "": "a.bin c.bin e.bin d.bin b.bin f.bin"}[order]
		if g := strings.Join(got, " "); g != want {
			t.Fatalf("ordre %q : %s (attendu %s)", order, g, want)
		}
	}
}

func TestUserOrderRespected(t *testing.T) {
	old := bigFileSlots
	bigFileSlots = 1
	defer func() { bigFileSlots = old }()
	l, r := t.TempDir(), t.TempDir()
	for n, mb := range map[string]int{"a.bin": 5, "b.bin": 9, "c.bin": 6, "d.bin": 8} {
		os.WriteFile(filepath.Join(l, n), make([]byte, mb<<20), 0o644)
	}
	L, _ := vfs.NewLocal(l)
	R, _ := vfs.NewLocal(r)
	ls, _ := Scan(context.Background(), L, nil, nil)
	rs, _ := Scan(context.Background(), R, nil, nil)
	opt := Options{Mode: Mirror, Parallel: 8, Order: []string{"c.bin", "a.bin", "d.bin", "b.bin"}}
	plan, _ := BuildPlan(ls, rs, State{}, opt)
	var mu sync.Mutex
	var got []string
	Execute(context.Background(), plan, L, R, State{}, opt, nil, func(i int, res Result) {
		mu.Lock()
		got = append(got, res.Path)
		mu.Unlock()
	})
	if g := strings.Join(got, " "); g != "c.bin a.bin d.bin b.bin" {
		t.Fatalf("ordre : %s", g)
	}
}

func TestDeletesFirst(t *testing.T) {
	l, r := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(l, "a.txt"), []byte("a"), 0o644)
	os.WriteFile(filepath.Join(l, "b.txt"), []byte("b"), 0o644)
	os.MkdirAll(filepath.Join(r, "vieux"), 0o755)
	os.WriteFile(filepath.Join(r, "vieux", "x.txt"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(r, "z.txt"), []byte("z"), 0o644)
	L, _ := vfs.NewLocal(l)
	R, _ := vfs.NewLocal(r)
	ls, _ := Scan(context.Background(), L, nil, nil)
	rs, _ := Scan(context.Background(), R, nil, nil)
	opt := Options{Mode: Mirror, AllowEmptySide: true}
	plan, _ := BuildPlan(ls, rs, State{}, opt)
	var mu sync.Mutex
	var got []string
	Execute(context.Background(), plan, L, R, State{}, opt, nil, func(i int, res Result) {
		mu.Lock()
		got = append(got, string(plan.Actions[i].Kind)+" "+res.Path)
		mu.Unlock()
	})
	seenCopy := false
	for _, g := range got {
		if strings.HasPrefix(g, "copy") {
			seenCopy = true
		}
		if strings.HasPrefix(g, "delete") && seenCopy {
			t.Fatalf("suppression après une copie : %v", got)
		}
	}
	if len(got) != 5 {
		t.Fatalf("actions : %v", got)
	}
}

func TestFixDates(t *testing.T) {
	l, r := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(l, "a.txt"), []byte("abc"), 0o644)
	os.WriteFile(filepath.Join(r, "a.txt"), []byte("abc"), 0o644)
	src := time.Date(2020, 5, 6, 5, 57, 1, 600_000_000, time.Local)
	os.Chtimes(filepath.Join(l, "a.txt"), src, src)
	os.Chtimes(filepath.Join(r, "a.txt"), src.Add(-1600*time.Millisecond), src.Add(-1600*time.Millisecond))
	L, _ := vfs.NewLocal(l)
	R, _ := vfs.NewLocal(r)
	for _, fix := range []bool{false, true} {
		ls, _ := Scan(context.Background(), L, nil, nil)
		rs, _ := Scan(context.Background(), R, nil, nil)
		opt := Options{Mode: Backup, KeepDates: !fix}
		plan, _ := BuildPlan(ls, rs, State{}, opt)
		if len(plan.Actions) != 0 {
			t.Fatalf("actions : %v", plan.Actions)
		}
		Execute(context.Background(), plan, L, R, State{}, opt, nil, nil)
		fi, _ := os.Stat(filepath.Join(r, "a.txt"))
		if got := fi.ModTime().Equal(src); got != fix || plan.DatesFixed != map[bool]int{true: 1}[fix] {
			t.Fatalf("fix=%v : date droite %v, corrigées %d", fix, fi.ModTime(), plan.DatesFixed)
		}
	}
}

// FS qui annule le contexte après n changements de date
type cancelFS struct {
	vfs.FS
	n      int32
	cancel context.CancelFunc
}

func (c *cancelFS) Chtimes(p string, t time.Time) error {
	if atomic.AddInt32(&c.n, 1) == 100 {
		c.cancel()
	}
	return c.FS.Chtimes(p, t)
}

func TestFixDatesCancel(t *testing.T) {
	l, r := t.TempDir(), t.TempDir()
	src := time.Date(2020, 5, 6, 5, 57, 1, 600_000_000, time.Local)
	for i := 0; i < 1000; i++ {
		for _, d := range []string{l, r} {
			p := filepath.Join(d, fmt.Sprintf("f%d.txt", i))
			os.WriteFile(p, []byte("x"), 0o644)
		}
		os.Chtimes(filepath.Join(l, fmt.Sprintf("f%d.txt", i)), src, src)
		os.Chtimes(filepath.Join(r, fmt.Sprintf("f%d.txt", i)), src.Add(-time.Second), src.Add(-time.Second))
	}
	L, _ := vfs.NewLocal(l)
	R, _ := vfs.NewLocal(r)
	ls, _ := Scan(context.Background(), L, nil, nil)
	rs, _ := Scan(context.Background(), R, nil, nil)
	plan, _ := BuildPlan(ls, rs, State{}, Options{Mode: Mirror})
	ctx, cancel := context.WithCancel(context.Background())
	FixDates(ctx, plan, &cancelFS{FS: R, cancel: cancel}, 4, false, nil)
	left := len(DatesToFix(plan, Mirror, false))
	// sur disque : nombre de dates réellement corrigées
	ok := 0
	for i := 0; i < 1000; i++ {
		fi, _ := os.Stat(filepath.Join(r, fmt.Sprintf("f%d.txt", i)))
		if fi.ModTime().Equal(src) {
			ok++
		}
	}
	t.Logf("corrigées %d (disque %d), restantes %d", plan.DatesFixed, ok, left)
	if plan.DatesFixed < 100 || ok != plan.DatesFixed || left != 1000-ok {
		t.Fatalf("corrigées %d, sur disque %d, restantes %d", plan.DatesFixed, ok, left)
	}
}

// FS dont les dates sont arrondies aux 2 s supérieures (comme FAT / exFAT sous Windows)
type coarseFS struct{ vfs.FS }

func (c coarseFS) Chtimes(p string, t time.Time) error {
	r := t.Truncate(2 * time.Second)
	if r.Before(t) {
		r = r.Add(2 * time.Second)
	}
	return c.FS.Chtimes(p, r)
}

func TestFixDatesCoarse(t *testing.T) {
	l, r := t.TempDir(), t.TempDir()
	src := time.Date(2020, 5, 6, 5, 57, 1, 600_000_000, time.UTC)
	for i := 0; i < 200; i++ {
		for _, d := range []string{l, r} {
			os.WriteFile(filepath.Join(d, fmt.Sprintf("f%d.txt", i)), []byte("x"), 0o644)
		}
		os.Chtimes(filepath.Join(l, fmt.Sprintf("f%d.txt", i)), src, src)
		os.Chtimes(filepath.Join(r, fmt.Sprintf("f%d.txt", i)), src.Add(400*time.Millisecond), src.Add(400*time.Millisecond))
	}
	testCoarse(t, l, r, 40)
	// très peu de fichiers (cas de la tâche « Fichiers Outlook » : 3 fichiers)
	l2, r2 := t.TempDir(), t.TempDir()
	for i := 0; i < 3; i++ {
		for _, d := range []string{l2, r2} {
			os.WriteFile(filepath.Join(d, fmt.Sprintf("f%d.txt", i)), []byte("x"), 0o644)
		}
		os.Chtimes(filepath.Join(l2, fmt.Sprintf("f%d.txt", i)), src, src)
		os.Chtimes(filepath.Join(r2, fmt.Sprintf("f%d.txt", i)), src.Add(400*time.Millisecond), src.Add(400*time.Millisecond))
	}
	testCoarse(t, l2, r2, 3)
}

func testCoarse(t *testing.T, l, r string, maxTries int) {
	L, _ := vfs.NewLocal(l)
	R, _ := vfs.NewLocal(r)
	ls, _ := Scan(context.Background(), L, nil, nil)
	rs, _ := Scan(context.Background(), R, nil, nil)
	plan, _ := BuildPlan(ls, rs, State{}, Options{Mode: Mirror})
	FixDates(context.Background(), plan, coarseFS{R}, 4, false, nil)
	if !plan.CoarseFound || plan.DatesImprecise > maxTries || plan.DatesFixed != 0 {
		t.Fatalf("coarse %v, arrondies %d, corrigées %d", plan.CoarseFound, plan.DatesImprecise, plan.DatesFixed)
	}
	if n := len(DatesToFix(plan, Mirror, true)); n != 0 {
		t.Fatalf("encore %d à corriger", n)
	}
}

// Un dossier supprimé à gauche mais qui contient à droite des éléments écartés
// par l'analyse (exclus, restes de copie) : il ne doit plus rester en erreur.
func TestRemoveDirWithExcluded(t *testing.T) {
	run := func(trash bool, files ...string) (Result, string) {
		l, r := t.TempDir(), t.TempDir()
		os.WriteFile(filepath.Join(l, "garde.txt"), []byte("g"), 0o644)
		os.WriteFile(filepath.Join(r, "garde.txt"), []byte("g"), 0o644)
		os.MkdirAll(filepath.Join(r, "vieux"), 0o755)
		os.WriteFile(filepath.Join(r, "vieux", "x.txt"), []byte("x"), 0o644)
		for _, f := range files {
			os.WriteFile(filepath.Join(r, "vieux", f), []byte("?"), 0o644)
		}
		L, _ := vfs.NewLocal(l)
		R, _ := vfs.NewLocal(r)
		opt := Options{Mode: Mirror, UseTrash: trash, Exclude: []string{"*.tmp"}}
		sl, sr := map[string]Skipped{}, map[string]Skipped{}
		ls, _ := ScanWith(context.Background(), L, opt, nil, sl)
		rs, _ := ScanWith(context.Background(), R, opt, nil, sr)
		plan, _ := BuildPlan(ls, rs, State{}, opt)
		plan.AddExcluded(sl, sr)
		var dirRes Result
		Execute(context.Background(), plan, L, R, State{}, opt, nil, func(i int, res Result) {
			if res.Path == "vieux" {
				dirRes = res
			}
		})
		return dirRes, r
	}
	// reste de copie interrompue : effacé, dossier supprimé
	res, r := run(false, ".synkro-tmp-y.txt")
	if res.Err != "" || !strings.Contains(res.Note, "reste") {
		t.Fatalf("reste de copie : %+v", res)
	}
	if _, err := os.Stat(filepath.Join(r, "vieux")); !os.IsNotExist(err) {
		t.Fatal("dossier encore là")
	}
	// élément exclu + corbeille : dossier entier à la corbeille
	res, r = run(true, "k.tmp")
	if res.Err != "" || !strings.Contains(res.Note, "corbeille") || !strings.Contains(res.Note, "k.tmp") {
		t.Fatalf("corbeille : %+v", res)
	}
	if _, err := os.Stat(filepath.Join(r, "vieux")); !os.IsNotExist(err) {
		t.Fatal("dossier encore là")
	}
	if findTrashed(t, r, "k.tmp") == "" {
		t.Fatal("k.tmp pas dans la corbeille")
	}
	// élément exclu (ou caché) sans corbeille activée : il part quand même dans
	// .synkro-trash (jamais effacé définitivement) et le dossier est supprimé
	res, r = run(false, "k.tmp")
	if res.Err != "" || !strings.Contains(res.Note, "k.tmp") {
		t.Fatalf("sans corbeille : %+v", res)
	}
	if _, err := os.Stat(filepath.Join(r, "vieux")); !os.IsNotExist(err) {
		t.Fatal("dossier encore là")
	}
	if findTrashed(t, r, "k.tmp") == "" {
		t.Fatal("k.tmp pas dans la corbeille")
	}
}

// Même si le plan liste un dossier avant ses sous-dossiers (ordre d'affichage
// choisi par l'utilisateur), tous les fichiers puis les dossiers les plus
// profonds partent d'abord : aucune erreur « dossier non vide ».
func TestDeleteDirsDeepestFirst(t *testing.T) {
	l, r := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(l, "garde.txt"), []byte("g"), 0o644)
	os.WriteFile(filepath.Join(r, "garde.txt"), []byte("g"), 0o644)
	for _, d := range []string{"_gsdata_/_saved_/Favorites", "_gsdata_/_saved_/Outlook"} {
		os.MkdirAll(filepath.Join(r, d), 0o755)
		os.WriteFile(filepath.Join(r, d, "f.url"), []byte("x"), 0o644)
	}
	os.WriteFile(filepath.Join(r, "_gsdata_", "a.gs"), []byte("x"), 0o644)
	L, _ := vfs.NewLocal(l)
	R, _ := vfs.NewLocal(r)
	ls, _ := Scan(context.Background(), L, nil, nil)
	rs, _ := Scan(context.Background(), R, nil, nil)
	opt := Options{Mode: Mirror, UseTrash: true}
	plan, _ := BuildPlan(ls, rs, State{}, opt)
	sort.SliceStable(plan.Actions, func(a, b int) bool { return plan.Actions[a].Path < plan.Actions[b].Path }) // parents d'abord
	var errs []string
	Execute(context.Background(), plan, L, R, State{}, opt, nil, func(i int, res Result) {
		if res.Err != "" {
			errs = append(errs, res.Path+" : "+res.Err)
		}
	})
	if len(errs) > 0 {
		t.Fatalf("erreurs : %v", errs)
	}
	if _, err := os.Stat(filepath.Join(r, "_gsdata_")); !os.IsNotExist(err) {
		t.Fatal("_gsdata_ encore là")
	}
}

// slowLink livre ce qu'on y écrit après un délai, sans bloquer l'écrivain :
// une vraie latence réseau, où plusieurs paquets peuvent être en route.
type slowLink struct {
	ch     chan slowChunk
	mu     sync.Mutex
	closed bool
}
type slowChunk struct {
	b  []byte
	at time.Time
}

func newSlowLink(dst io.Writer, delay time.Duration) *slowLink {
	l := &slowLink{ch: make(chan slowChunk, 1<<14)}
	go func() {
		for c := range l.ch {
			time.Sleep(time.Until(c.at.Add(delay)))
			if _, err := dst.Write(c.b); err != nil {
				return
			}
		}
	}()
	return l
}
func (l *slowLink) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return 0, io.ErrClosedPipe
	}
	l.ch <- slowChunk{append([]byte(nil), b...), time.Now()}
	return len(b), nil
}
func (l *slowLink) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.closed {
		l.closed = true
		close(l.ch)
	}
	return nil
}

// Sur un lien à 60 ms d'aller-retour, un gros fichier doit passer en flux
// (plusieurs paquets en vol), dans les deux sens, et arriver intact.
func TestSFTPStreamingOverLatency(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	cr, sw := io.Pipe() // serveur → client
	sr, cw := io.Pipe() // client → serveur
	toSrv, toCli := newSlowLink(cw, 30*time.Millisecond), newSlowLink(sw, 30*time.Millisecond)
	srv, err := sftp.NewServer(struct {
		io.Reader
		io.WriteCloser
	}{sr, toCli}, sftp.WithServerWorkingDirectory(b))
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	cl, err := sftp.NewClientPipe(cr, toSrv, sftp.UseConcurrentWrites(true), sftp.MaxConcurrentRequestsPerFile(128))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { toSrv.Close(); toCli.Close(); cr.Close(); sr.Close() }()
	R, err := vfs.NewSFTPFromClient(cl, b)
	if err != nil {
		t.Fatal(err)
	}
	L, _ := vfs.NewLocal(a)
	big := make([]byte, 16<<20)
	for i := range big {
		big[i] = byte(i * 7)
	}
	os.WriteFile(filepath.Join(a, "envoi.pst"), big, 0o644)
	os.WriteFile(filepath.Join(b, "retour.pst"), big[:8<<20], 0o644)
	opt := Options{Mode: TwoWay}
	start := time.Now()
	run(t, L, R, State{}, opt)
	el := time.Since(start)
	if got, _ := os.ReadFile(filepath.Join(b, "envoi.pst")); !bytes.Equal(got, big) {
		t.Fatal("envoi SFTP altéré")
	}
	if got, _ := os.ReadFile(filepath.Join(a, "retour.pst")); !bytes.Equal(got, big[:8<<20]) {
		t.Fatal("réception SFTP altérée")
	}
	// bloc par bloc (256 Ko par aller-retour) : 24 Mo prendraient au moins 96 allers-retours,
	// soit ~6 s ; en flux, bien moins
	t.Logf("24 Mo sur un lien à 60 ms d'aller-retour : %v", el.Round(time.Millisecond))
	if el > 3*time.Second {
		t.Fatalf("transfert trop lent : %v", el)
	}
}

// La vitesse se change pendant la copie, sans l'interrompre : limitée au départ,
// puis libérée, la copie finit vite, entière et sans erreur.
func TestChangeSpeedDuringCopy(t *testing.T) {
	l, r := t.TempDir(), t.TempDir()
	big := make([]byte, 8<<20)
	for i := range big {
		big[i] = byte(i * 13)
	}
	os.WriteFile(filepath.Join(l, "gros.bin"), big, 0o644)
	L, _ := vfs.NewLocal(l)
	R, _ := vfs.NewLocal(r)
	opt := Options{Mode: Mirror, MaxKBps: 512}
	ls, _ := Scan(context.Background(), L, nil, nil)
	rs, _ := Scan(context.Background(), R, nil, nil)
	plan, _ := BuildPlan(ls, rs, State{}, opt)
	g := &Gate{}
	var mid atomic.Bool
	go func() {
		time.Sleep(600 * time.Millisecond)
		mid.Store(true)
		g.SetMaxKBps(0) // illimité, en pleine copie
	}()
	start := time.Now()
	var errs []string
	Execute(WithGate(context.Background(), g), plan, L, R, State{}, opt, nil, func(i int, res Result) {
		if res.Err != "" {
			errs = append(errs, res.Err)
		}
	})
	el := time.Since(start)
	if len(errs) > 0 || !mid.Load() {
		t.Fatalf("erreurs %v (changement fait : %v)", errs, mid.Load())
	}
	if got, _ := os.ReadFile(filepath.Join(r, "gros.bin")); !bytes.Equal(got, big) {
		t.Fatal("copie altérée")
	}
	// à 512 Ko/s, 8 Mo prendraient 16 s
	if el > 4*time.Second {
		t.Fatalf("la vitesse libérée n'a pas été prise en compte : %v", el)
	}
	t.Logf("8 Mo : limité à 512 Ko/s puis libéré après 0,6 s → %v", el.Round(10*time.Millisecond))

	// et dans l'autre sens : libre au départ, puis limité très bas → la copie ralentit
	os.Remove(filepath.Join(r, "gros.bin"))
	rs, _ = Scan(context.Background(), R, nil, nil)
	opt.MaxKBps = 0
	plan, _ = BuildPlan(ls, rs, State{}, opt)
	g2 := &Gate{}
	g2.SetMaxKBps(256) // posé avant le premier bloc
	start = time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	Execute(WithGate(ctx, g2), plan, L, R, State{}, opt, nil, nil)
	if time.Since(start) < 1200*time.Millisecond {
		t.Fatal("la limite posée en cours de route n'a pas été appliquée")
	}
}
