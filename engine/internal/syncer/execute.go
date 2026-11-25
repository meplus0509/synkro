package syncer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"synkro/internal/vfs"
)

// Progress est envoyé pendant l'exécution.
type Progress struct {
	DoneActions  int    `json:"doneActions"`
	TotalActions int    `json:"totalActions"`
	DoneBytes    int64  `json:"doneBytes"`
	TotalBytes   int64  `json:"totalBytes"`
	Current      string `json:"current"`
	// Fichiers en cours de transfert : chemin → [octets copiés, taille]
	Active map[string][2]int64 `json:"active,omitempty"`
	// Note : message ponctuel (ex. reconnexion en cours)
	Note string `json:"note,omitempty"`
	// ElapsedMs : durée des transferts depuis le début de la synchro, pauses exclues
	ElapsedMs int64 `json:"elapsedMs"`
}

// Result est le résultat d'une action exécutée.
type Result struct {
	Action
	Err  string `json:"error,omitempty"`
	Note string `json:"note,omitempty"` // précision sur une action réussie (montrée dans le journal)
}

type executor struct {
	ctx      context.Context
	l, r     vfs.FS
	opt      Options
	stamp    string
	mu       sync.Mutex // protège prog et lastEmit (plusieurs transferts en parallèle)
	prog     Progress
	limit    *limiter // limite de vitesse (nil = illimitée)
	reconnMu sync.Mutex
	active   map[string]*[2]int64 // fichiers en cours de copie (protégé par mu)
	onProg   func(Progress)
	onResult func(i int, r Result)
	lastEmit time.Time
	started  time.Time // début de l'exécution
	bigSem   chan struct{}
	workers  int
}

// transferWorkers est le nombre de fichiers transférés en même temps. Sur SFTP,
// chaque fichier coûte plusieurs allers-retours réseau : les paralléliser
// multiplie le débit pour les petits fichiers.
var transferWorkers = 8

// Au-delà de bigFileSize, un fichier compte comme « gros » : au plus bigFileSlots
// gros fichiers sont envoyés en même temps, pour qu'ils n'accaparent pas une
// connexion lente au détriment des petits.
const bigFileSize = 4 << 20

var bigFileSlots = 2

// localWorkers : copies simultanées quand les deux côtés sont des dossiers locaux.
const localWorkers = 24

// SetTransferWorkers règle le nombre de transferts simultanés (tests, réglages).
func SetTransferWorkers(n int) {
	if n > 0 {
		transferWorkers = n
	}
}

func join(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

// emit doit être appelée avec x.mu verrouillé.
func (x *executor) emit(force bool) {
	if x.onProg == nil {
		return
	}
	if force || time.Since(x.lastEmit) > 200*time.Millisecond {
		x.lastEmit = time.Now()
		p := x.prog
		p.ElapsedMs = (time.Since(x.started) - gateOf(x.ctx).PausedFor()).Milliseconds()
		p.Active = nil
		if len(x.active) > 0 {
			p.Active = make(map[string][2]int64, len(x.active))
			for k, v := range x.active {
				p.Active[k] = *v
			}
		}
		x.onProg(p)
	}
}

func (x *executor) addBytes(path string, n int64) {
	x.mu.Lock()
	x.prog.DoneBytes += n
	if f := x.active[path]; f != nil {
		f[0] += n
	}
	x.emit(false)
	x.mu.Unlock()
}

// startFile / endFile tiennent la liste des fichiers en cours de copie.
func (x *executor) startFile(path string, size int64) {
	x.mu.Lock()
	if x.active == nil {
		x.active = map[string]*[2]int64{}
	}
	x.active[path] = &[2]int64{0, size}
	x.mu.Unlock()
}

// limited : un débit est imposé, en tenant compte d'un changement de vitesse fait
// pendant la synchro (lu à chaque bloc : il s'applique sans interrompre la copie).
func (x *executor) limited() bool {
	if k, ok := gateOf(x.ctx).MaxKBps(); ok {
		x.limit.setRate(float64(k) * 1024)
	}
	return x.limit.on()
}

// fileDone : octets déjà passés pour un fichier en cours de copie.
func (x *executor) fileDone(path string) int64 {
	x.mu.Lock()
	defer x.mu.Unlock()
	if f := x.active[path]; f != nil {
		return f[0]
	}
	return 0
}

// human : taille lisible pour le journal (1 décimale, unités de 1024).
func human(n int64) string {
	const u = "KMGT"
	if n < 1024 {
		return fmt.Sprintf("%d o", n)
	}
	f, i := float64(n)/1024, 0
	for f >= 1024 && i < len(u)-1 {
		f /= 1024
		i++
	}
	return strings.Replace(fmt.Sprintf("%.1f %co", f, u[i]), ".", ",", 1)
}

func (x *executor) endFile(path string) {
	x.mu.Lock()
	delete(x.active, path)
	x.mu.Unlock()
}

// progressWriter compte les octets écrits, vérifie l'annulation et note l'heure
// du dernier progrès (pour détecter un transfert bloqué).
type progressWriter struct {
	w    io.Writer
	x    *executor
	last *atomic.Int64
	path string
}

func (pw progressWriter) Write(b []byte) (int, error) {
	if pw.x.limited() && len(b) > limitChunk {
		// limite de vitesse : on écrit par petits morceaux pour garder un débit régulier
		total := 0
		for len(b) > 0 {
			n := min(len(b), limitChunk)
			w, err := pw.Write(b[:n])
			total += w
			if err != nil {
				return total, err
			}
			b = b[n:]
		}
		return total, nil
	}
	if err := pw.x.ctx.Err(); err != nil {
		return 0, err
	}
	if pw.x.limited() {
		if err := pw.x.limit.wait(pw.x.ctx, len(b)); err != nil {
			return 0, err
		}
		pw.last.Store(time.Now().UnixNano()) // l'attente imposée n'est pas un blocage
	}
	if g := gateOf(pw.x.ctx); g.Paused() {
		if err := g.Wait(pw.x.ctx); err != nil {
			return 0, err
		}
		pw.last.Store(time.Now().UnixNano()) // la pause ne compte pas comme un blocage
	}
	n, err := pw.w.Write(b)
	pw.x.addBytes(pw.path, int64(n))
	pw.last.Store(time.Now().UnixNano())
	return n, err
}

// progressReader compte ce qui est lu (envoi SFTP en flux : la bibliothèque lit
// la source elle-même) avec les mêmes règles que progressWriter : annulation,
// pause, limite de vitesse, progression et chien de garde.
type progressReader struct {
	r    io.Reader
	x    *executor
	last *atomic.Int64
	path string
}

func (pr progressReader) Read(b []byte) (int, error) {
	if err := pr.x.ctx.Err(); err != nil {
		return 0, err
	}
	if pr.x.limited() {
		if len(b) > limitChunk {
			b = b[:limitChunk]
		}
		if err := pr.x.limit.wait(pr.x.ctx, len(b)); err != nil {
			return 0, err
		}
		pr.last.Store(time.Now().UnixNano())
	}
	if g := gateOf(pr.x.ctx); g.Paused() {
		if err := g.Wait(pr.x.ctx); err != nil {
			return 0, err
		}
		pr.last.Store(time.Now().UnixNano())
	}
	n, err := pr.r.Read(b)
	pr.x.addBytes(pr.path, int64(n))
	pr.last.Store(time.Now().UnixNano())
	return n, err
}

// Fichiers distants (SFTP) qui savent transférer en flux, plusieurs paquets en vol :
// sans cela, chaque bloc attend la réponse du serveur, et le débit plafonne à
// (taille du bloc / temps d'aller-retour), soit ~1,4 Mo/s à 180 ms.
type streamWriter interface {
	ReadFromWithConcurrency(r io.Reader, concurrency int) (int64, error)
}
type streamReader interface {
	streamWriter // c'est le même type de fichier distant (sert à le reconnaître)
	WriteTo(w io.Writer) (int64, error)
}

// StallTimeout : un fichier dont le transfert n'avance plus depuis ce délai est
// abandonné (marqué en échec) et on passe au suivant.
var StallTimeout = 2 * time.Minute

// errStalled signale un transfert abandonné faute de progrès.
var errStalled = errors.New("transfert bloqué, abandonné")

// streamCopy : transfert SFTP en flux (désactivable dans les tests, pour comparer).
var streamCopy = true

func isStream(w io.Writer) bool  { _, ok := w.(streamWriter); return ok && streamCopy }
func isStreamR(r io.Reader) bool { _, ok := r.(streamReader); return ok && streamCopy }

// discard met un fichier de côté (corbeille) ou le supprime.
func (x *executor) discard(fs vfs.FS, p string) error {
	if x.opt.UseTrash {
		return fs.Rename(p, TrashDir+"/"+x.stamp+"/"+p)
	}
	return fs.Remove(p)
}

func (x *executor) copy(src, dst vfs.FS, p string, srcEntry vfs.Entry, dstExists bool) error {
	in, err := src.Open(p)
	if err != nil {
		return fmt.Errorf("lecture : %w", err)
	}
	defer in.Close()
	tmp := join(vfs.Parent(p), reservedPrefix+"tmp-"+vfs.Base(p))
	out, err := dst.Create(tmp)
	if err != nil {
		return fmt.Errorf("création : %w", err)
	}
	// Blocs de 256 Ko : sur SFTP, chaque bloc part en 8 paquets simultanés au lieu
	// d'un aller-retour réseau par tranche de 32 Ko, tout en gardant une progression
	// fine et une annulation rapide sur une connexion lente.
	// (Les enveloppes masquent ReadFrom/WriteTo pour que ce tampon soit bien utilisé.)
	buf := make([]byte, 256<<10)
	// Surveillance : si plus rien n'avance pendant StallTimeout, on ferme les
	// fichiers pour débloquer la copie ; ce fichier passe en échec, les autres continuent.
	var last atomic.Int64
	last.Store(time.Now().UnixNano())
	var stalled atomic.Bool
	done := make(chan struct{})
	timeout := StallTimeout // lu une seule fois : le chien de garde n'y retouche plus
	go func() {
		t := time.NewTicker(timeout / 8)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if gateOf(x.ctx).Paused() {
					last.Store(time.Now().UnixNano())
					continue
				}
				if time.Since(time.Unix(0, last.Load())) > timeout {
					stalled.Store(true)
					out.Close()
					in.Close()
					return
				}
			}
		}
	}()
	x.startFile(p, srcEntry.Size)
	defer x.endFile(p)
	// mesure du débit, écrite dans le journal détaillé (fin de copie, et toutes les
	// 30 s pendant un gros fichier) : sert à juger la vitesse réelle du lien
	t0 := time.Now()
	mode := "bloc par bloc"
	if isStream(out) || isStreamR(in) {
		mode = "en flux"
	}
	defer func() {
		n := x.fileDone(p)
		if Logf != nil && n > 0 {
			el := time.Since(t0)
			Logf("[détail] transfert %s : %s sur %s en %v · %s/s (%s)", p, human(n), human(srcEntry.Size), el.Round(100*time.Millisecond), human(int64(float64(n)/max(el.Seconds(), 0.001))), mode)
		}
	}()
	if srcEntry.Size >= 50<<20 && Logf != nil {
		go func() {
			t := time.NewTicker(30 * time.Second)
			defer t.Stop()
			var prev int64
			for {
				select {
				case <-done:
					return
				case <-t.C:
					n := x.fileDone(p)
					Logf("[détail] transfert en cours %s : %s sur %s · %s/s sur les 30 dernières s (%s)", p, human(n), human(srcEntry.Size), human((n-prev)/30), mode)
					prev = n
				}
			}
		}()
	}
	switch {
	case isStream(out):
		// envoi vers SFTP en flux
		_, err = out.(streamWriter).ReadFromWithConcurrency(progressReader{in, x, &last, p}, 0)
	case isStreamR(in):
		// réception depuis SFTP en flux (lectures en vol), écrite dans l'ordre
		_, err = in.(streamReader).WriteTo(progressWriter{out, x, &last, p})
	default:
		_, err = io.CopyBuffer(progressWriter{out, x, &last, p}, struct{ io.Reader }{in}, buf)
	}
	close(done)
	if stalled.Load() {
		_ = dst.Remove(tmp)
		return fmt.Errorf("%w (aucun progrès depuis %v)", errStalled, timeout)
	}
	if err != nil {
		out.Close()
		_ = dst.Remove(tmp)
		return fmt.Errorf("copie : %w", err)
	}
	if err := out.Close(); err != nil {
		_ = dst.Remove(tmp)
		return fmt.Errorf("écriture : %w", err)
	}
	// l'ancienne version va à la corbeille avant d'être remplacée
	if x.opt.UseTrash && dstExists {
		if err := x.discard(dst, p); err != nil {
			_ = dst.Remove(tmp)
			return fmt.Errorf("mise à la corbeille de l'ancienne version : %w", err)
		}
	}
	if err := dst.Rename(tmp, p); err != nil {
		_ = dst.Remove(tmp)
		return fmt.Errorf("renommage final : %w", err)
	}
	if err := dst.Chtimes(p, srcEntry.ModTime); err != nil {
		return fmt.Errorf("date de modification : %w", err)
	}
	if x.opt.CopyCreated || x.opt.CopyAttrs {
		sm, ok1 := src.(vfs.MetaFS)
		dm, ok2 := dst.(vfs.MetaFS)
		if ok1 && ok2 {
			if m, err := sm.GetMeta(p); err == nil {
				if err := dm.SetMeta(p, m, x.opt.CopyCreated, x.opt.CopyAttrs); err != nil {
					return fmt.Errorf("date de création / attributs : %w", err)
				}
			}
		}
	}
	return nil
}

// Execute applique le plan et renvoie le nouvel état ainsi que le résultat de chaque action.
// Ordre : création des dossiers (niveau par niveau, en parallèle), puis fichiers
// (en parallèle), puis suppression des dossiers (enfants d'abord, à la suite).
// onResult (facultatif) est appelée dès qu'une action est terminée, avec son
// indice dans plan.Actions : l'interface peut ainsi afficher l'état en direct.
func Execute(ctx context.Context, plan *Plan, left, right vfs.FS, prev State, opt Options, onProg func(Progress), onResult func(i int, r Result)) (State, []Result) {
	x := &executor{ctx: ctx, l: left, r: right, opt: opt, stamp: time.Now().Format("2006-01-02_15-04-05"), onProg: onProg, onResult: onResult, bigSem: make(chan struct{}, bigFileSlots), workers: transferWorkers, started: time.Now()}
	// Entre deux dossiers locaux, chaque fichier attend surtout le disque et l'antivirus
	// (analyse à la création) : plus de copies simultanées accélèrent nettement.
	_, lLocal := left.(*vfs.Local)
	_, rLocal := right.(*vfs.Local)
	if lLocal && rLocal && x.workers < localWorkers {
		x.workers = localWorkers
	}
	if opt.Parallel > 0 { // réglage du job
		x.workers = opt.Parallel
	}
	// toujours présent : la vitesse peut être limitée (ou libérée) en cours de route
	x.limit = newLimiter(float64(max(opt.MaxKBps, 0)) * 1024)
	x.prog.TotalActions = len(plan.Actions)
	x.prog.TotalBytes = plan.TotalBytes

	results := make([]Result, len(plan.Actions))
	var mkdirs, files, fileDels, dirDels []int
	for i, a := range plan.Actions {
		switch {
		case a.Kind == MkdirL || a.Kind == MkdirR:
			mkdirs = append(mkdirs, i)
		case a.IsDir && (a.Kind == DelL || a.Kind == DelR):
			dirDels = append(dirDels, i)
		case a.Kind == DelL || a.Kind == DelR:
			fileDels = append(fileDels, i)
		default:
			files = append(files, i)
		}
	}

	// 0. suppressions d'abord (place libérée avant les copies) : fichiers en
	// parallèle, puis dossiers dans l'ordre du plan (enfants d'abord)
	x.runParallel(fileDels, plan, results)
	// dossiers : toujours du plus profond au moins profond (quel que soit l'ordre
	// d'affichage demandé), pour qu'un dossier soit vidé de ses sous-dossiers avant
	sort.SliceStable(dirDels, func(a, b int) bool {
		return depth(plan.Actions[dirDels[a]].Path) > depth(plan.Actions[dirDels[b]].Path)
	})
	for _, i := range dirDels {
		x.finish(i, x.do(plan, plan.Actions[i]), results)
	}

	// 1. dossiers, groupés par profondeur : un niveau en parallèle, puis le suivant
	byDepth := map[int][]int{}
	maxDepth := 0
	for _, i := range mkdirs {
		d := depth(plan.Actions[i].Path)
		byDepth[d] = append(byDepth[d], i)
		if d > maxDepth {
			maxDepth = d
		}
	}
	for d := 0; d <= maxDepth; d++ {
		x.runParallel(byDepth[d], plan, results)
	}
	// 2. fichiers en parallèle. Par défaut du plus petit au plus gros : sur une
	// connexion lente, les milliers de petits fichiers passent vite au lieu
	// d'attendre derrière les gros. Le job peut choisir un autre ordre.
	sortFiles(files, plan.Actions, x.opt.CopyOrder)
	if len(x.opt.Order) > 0 { // ordre de la liste triée par l'utilisateur
		rank := make(map[string]int, len(x.opt.Order))
		for i, p := range x.opt.Order {
			rank[p] = i
		}
		pos := func(i int) int {
			if r, ok := rank[plan.Actions[i].Path]; ok {
				return r
			}
			return len(rank) // absents de la liste : à la fin, ordre de copie normal
		}
		sort.SliceStable(files, func(a, b int) bool { return pos(files[a]) < pos(files[b]) })
	}
	x.runParallel(files, plan, results)
	x.fixDates(plan)

	x.mu.Lock()
	x.prog.Current = ""
	x.emit(true)
	x.mu.Unlock()
	return newState(plan, prev, results), results
}

func (x *executor) finish(i int, r Result, results []Result) {
	results[i] = r
	if x.onResult != nil {
		x.onResult(i, r)
	}
}

// runParallel exécute les actions désignées avec transferWorkers goroutines.
func (x *executor) runParallel(idx []int, plan *Plan, results []Result) {
	if len(idx) == 0 {
		return
	}
	type job struct {
		i    int
		held bool // place de « gros fichier » réservée par le distributeur
	}
	jobs := make(chan job)
	var wg sync.WaitGroup
	n := x.workers
	if n > len(idx) {
		n = len(idx)
	}
	for w := 0; w < n; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				x.finish(j.i, x.do(plan, plan.Actions[j.i]), results)
				if j.held {
					<-x.bigSem
				}
			}
		}()
	}
	// Les places pour les gros fichiers sont réservées ici, dans l'ordre de la
	// liste : sinon les transferts en attente se les disputent et l'ordre de
	// copie choisi n'est pas respecté.
	for _, i := range idx {
		j := job{i: i}
		if isBig(plan.Actions[i]) {
			select {
			case x.bigSem <- struct{}{}:
				j.held = true
			case <-x.ctx.Done():
			}
		}
		jobs <- j
	}
	close(jobs)
	wg.Wait()
}

// do exécute une action (appelée en parallèle) et met à jour la progression.
func (x *executor) do(plan *Plan, a Action) Result {
	if gateOf(x.ctx).Wait(x.ctx) != nil {
		return Result{Action: a, Err: "annulé"}
	}
	x.mu.Lock()
	x.prog.Current = a.Path
	x.emit(false)
	x.mu.Unlock()

	if x.ctx.Err() != nil {
		return Result{Action: a, Err: "annulé"}
	}
	var note string
	err := x.withRetry(a, func() (e error) { note, e = x.applyNote(plan, a); return })
	r := Result{Action: a, Note: note}
	if err != nil {
		r.Err = err.Error() + vfs.Hint(err)
		if x.ctx.Err() != nil {
			r.Err = "annulé"
		}
	}
	x.mu.Lock()
	x.prog.DoneActions++
	x.emit(false)
	x.mu.Unlock()
	return r
}

// applyNote effectue une action une fois ; pour la suppression d'un dossier,
// elle renvoie aussi une précision sur ce qui a été fait de son contenu exclu.
func (x *executor) applyNote(plan *Plan, a Action) (string, error) {
	if a.IsDir && (a.Kind == DelL || a.Kind == DelR) {
		return x.removeDir(plan, a)
	}
	return "", x.apply(plan, a)
}

// removeDir supprime un dossier dont tout le contenu analysé a déjà été traité.
// S'il reste dedans des éléments que l'analyse a écartés (exclus par les filtres,
// fichiers système, restes « .synkro-tmp- » d'une copie interrompue) :
//   - les restes de copie de Synkro sont effacés (ce sont nos propres déchets) ;
//   - le reste part dans la corbeille .synkro-trash (récupérable), même si la
//     corbeille n'est pas activée pour la tâche, puis le dossier est supprimé ;
//   - si même cela échoue, il est conservé et l'erreur dit ce qu'il contient encore.
func (x *executor) removeDir(plan *Plan, a Action) (string, error) {
	fs := x.l
	if a.Kind == DelR {
		fs = x.r
	}
	err := fs.Remove(a.Path)
	if err == nil {
		return "", nil
	}
	if _, e := fs.Stat(a.Path); errors.Is(e, vfs.ErrNotExist) {
		return "", nil // déjà parti (supprimé entre-temps)
	}
	rest := leftInside(plan, a.Path, a.Kind == DelR)
	cleaned := 0
	var others []string
	for _, p := range rest {
		if strings.HasPrefix(vfs.Base(p), reservedPrefix+"tmp-") && fs.Remove(p) == nil {
			cleaned++
			continue
		}
		others = append(others, p)
	}
	if cleaned > 0 {
		if fs.Remove(a.Path) == nil {
			return fmt.Sprintf("%d reste(s) de copie interrompue effacé(s)", cleaned), nil
		}
	}
	what := describeLeft(others, a.Path)
	// Le dossier n'existe plus à la source : ses restes (exclus, cachés) n'ont plus
	// de raison d'être, mais on ne les efface jamais définitivement. Ils vont dans
	// la corbeille .synkro-trash même si la corbeille n'est pas activée pour la tâche.
	{
		// chaque reste rejoint la corbeille à sa place (récupérable), puis le dossier vide part
		for _, p := range others {
			if _, e := fs.Stat(p); e != nil {
				continue // déjà déplacé avec son dossier parent
			}
			fs.Rename(p, TrashDir+"/"+x.stamp+"/"+p)
		}
		if fs.Remove(a.Path) == nil {
			return "le dossier contenait encore " + what + " : mis dans la corbeille " + TrashDir + " avec lui", nil
		}
		// dernier recours : le dossier entier, sous un nom libre dans la corbeille
		if fs.Rename(a.Path, TrashDir+"/"+x.stamp+"/"+a.Path+" (restes)") == nil {
			return "le dossier contenait encore " + what + " : mis en entier dans la corbeille " + TrashDir, nil
		}
	}
	return "", fmt.Errorf("dossier non vide, conservé : il contient encore %s : %w", what, err)
}

// leftInside liste les éléments écartés par l'analyse sous un dossier, du côté donné.
func leftInside(plan *Plan, dir string, right bool) []string {
	var out []string
	for _, s := range plan.Same {
		if s.Kind != ExcludedKind || !strings.HasPrefix(s.Path, dir+"/") {
			continue
		}
		side := s.Left
		if right {
			side = s.Right
		}
		if side != nil && side.Exists {
			out = append(out, s.Path)
		}
	}
	sort.Strings(out)
	return out
}

func describeLeft(paths []string, dir string) string {
	if len(paths) == 0 {
		return "des éléments non listés par l'analyse"
	}
	names := []string{}
	for i, p := range paths {
		if i == 3 {
			names = append(names, fmt.Sprintf("… (%d en tout)", len(paths)))
			break
		}
		names = append(names, "« "+strings.TrimPrefix(p, dir+"/")+" »")
	}
	return fmt.Sprintf("%d élément(s) exclu(s) de l'analyse : %s", len(paths), strings.Join(names, ", "))
}

// apply effectue une action une fois.
func (x *executor) apply(plan *Plan, a Action) error {
	var err error
	switch a.Kind {
	case CopyLR:
		_, exists := plan.Right[a.Path]
		err = x.copy(x.l, x.r, a.Path, plan.Left[a.Path], exists)
	case CopyRL:
		_, exists := plan.Left[a.Path]
		err = x.copy(x.r, x.l, a.Path, plan.Right[a.Path], exists)
	case MkdirL:
		err = x.l.MkdirAll(a.Path)
	case MkdirR:
		err = x.r.MkdirAll(a.Path)
	case DelL, DelR:
		fs := x.l
		if a.Kind == DelR {
			fs = x.r
		}
		if a.IsDir {
			err = fs.Remove(a.Path) // le contenu a déjà été traité ; échoue si non vide
			if err != nil {
				err = fmt.Errorf("dossier non vide, conservé : %w", err)
			}
		} else {
			err = x.discard(fs, a.Path)
		}
	case Skip:
		err = errors.New(a.Reason)
	case Leave:
		// rien à faire : conflit volontairement laissé tel quel
	}
	return err
}

func toState(e vfs.Entry) FileState {
	if e.IsDir {
		return FileState{IsDir: true}
	}
	return FileState{Size: e.Size, MTime: e.ModTime.Unix()}
}

// newState calcule l'état après exécution : ce qui est identique des deux côtés,
// plus les actions réussies ; une action échouée conserve l'ancien état pour être retentée.
func newState(plan *Plan, prev State, results []Result) State {
	ns := State{}
	// éléments hors du filtre « inclure seulement » : leur état est conservé tel quel
	if hasPatterns(plan.include) {
		for p, st := range prev {
			_, inL := plan.Left[p]
			_, inR := plan.Right[p]
			if !inL && !inR && !Included(p, plan.include) {
				ns[p] = st
			}
		}
	}
	for p, l := range plan.Left {
		if r, ok := plan.Right[p]; ok && sameEntry(l, r) {
			ns[p] = toState(l)
		}
	}
	for _, r := range results {
		p := r.Path
		if r.Err != "" {
			if s, ok := prev[p]; ok {
				ns[p] = s
			} else {
				delete(ns, p)
			}
			continue
		}
		switch r.Kind {
		case CopyLR:
			ns[p] = toState(plan.Left[p])
		case CopyRL:
			ns[p] = toState(plan.Right[p])
		case MkdirL, MkdirR:
			ns[p] = FileState{IsDir: true}
		case DelL, DelR:
			delete(ns, p)
		case Leave:
			// laissé tel quel (conflit non tranché ou choix « ne rien faire ») :
			// on garde l'ancien état, la même action sera reproposée la prochaine fois
			if s, ok := prev[p]; ok {
				ns[p] = s
			} else {
				delete(ns, p)
			}
		}
	}
	return ns
}

// isBig : copie d'un gros fichier (nombre limité en même temps, voir bigFileSlots).
func isBig(a Action) bool { return (a.Kind == CopyLR || a.Kind == CopyRL) && a.Size >= bigFileSize }

// sortFiles range les fichiers à copier selon l'ordre choisi pour le job.
func sortFiles(files []int, acts []Action, order string) {
	switch order {
	case "size-desc":
		sort.SliceStable(files, func(a, b int) bool { return acts[files[a]].Size > acts[files[b]].Size })
	case "path":
		sort.SliceStable(files, func(a, b int) bool { return pathLess(acts[files[a]].Path, acts[files[b]].Path) })
	default:
		sort.SliceStable(files, func(a, b int) bool { return acts[files[a]].Size < acts[files[b]].Size })
	}
}

// pathLess : ordre de la liste affichée, dossier par dossier, sans tenir compte
// des majuscules ni des accents simples.
func pathLess(a, b string) bool {
	pa, pb := strings.Split(a, "/"), strings.Split(b, "/")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		x, y := foldName(pa[i]), foldName(pb[i])
		if x != y {
			return x < y
		}
	}
	return len(pa) < len(pb)
}

var accentFold = strings.NewReplacer("à", "a", "â", "a", "ä", "a", "é", "e", "è", "e", "ê", "e", "ë", "e",
	"î", "i", "ï", "i", "ô", "o", "ö", "o", "ù", "u", "û", "u", "ü", "u", "ç", "c")

func foldName(s string) string { return accentFold.Replace(strings.ToLower(s)) }

// fixDates recopie la date des fichiers identiques dont la date diffère d'une ou
// deux secondes (tâches à sens unique, sauf KeepDates) : seule la date change.
func (x *executor) fixDates(plan *Plan) {
	if x.opt.KeepDates || x.opt.Mode == TwoWay || x.ctx.Err() != nil {
		return
	}
	FixDates(x.ctx, plan, x.r, x.workers, x.opt.CoarseDates, x.opt.OnFixDates)
}

// DatesToFix : fichiers identiques dont la date (à la seconde) diffère à droite
// (tâches à sens unique : la gauche est la source).
// coarse : la droite arrondit à 2 s, les écarts jusqu'à 2 s sont normaux.
func DatesToFix(plan *Plan, mode Mode, coarse bool) []string {
	if mode == TwoWay {
		return nil
	}
	var todo []string
	for _, a := range plan.Same {
		if a.Kind != Same || a.IsDir {
			continue
		}
		l, lok := plan.Left[a.Path]
		r, rok := plan.Right[a.Path]
		if lok && rok && !l.IsDir && !r.IsDir && l.ModTime.Unix() != r.ModTime.Unix() {
			if coarse {
				d := l.ModTime.Sub(r.ModTime)
				if d < 0 {
					d = -d
				}
				if d <= 2*time.Second {
					continue
				}
			}
			todo = append(todo, a.Path)
		}
	}
	return todo
}

// FixDates recopie à droite (dst) la date de gauche des fichiers de DatesToFix.
// Résultat dans plan.DatesFixed / DatesFailed / DatesImprecise ; onProg(fait, total).
func FixDates(ctx context.Context, plan *Plan, dst vfs.FS, workers int, coarse bool, onProg func(done, total int)) {
	todo := DatesToFix(plan, Mirror, coarse)
	plan.CoarseFound = false
	plan.DatesFixed, plan.DatesFailed, plan.DatesImprecise = 0, 0, 0
	plan.DatesFailedWhy = nil
	if len(todo) == 0 {
		return
	}
	if workers < 1 {
		workers = 8
	}
	var mu sync.Mutex
	done, failed, imprecise, n := 0, 0, 0, 0
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	stop := false // le disque arrondit toutes les dates : inutile d'insister
	for _, p := range todo {
		mu.Lock()
		halt := stop
		mu.Unlock()
		if ctx.Err() != nil || halt {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(p string) {
			defer func() { <-sem; wg.Done() }()
			want := plan.Left[p].ModTime
			err := dst.Chtimes(p, want)
			// relecture : FAT / FAT32 n'enregistrent les dates qu'à 2 s près (exFAT : 10 ms)
			coarse := false
			if err == nil {
				if e, e2 := dst.Stat(p); e2 == nil && e.ModTime.Unix() != want.Unix() {
					coarse = true
				}
			}
			mu.Lock()
			defer mu.Unlock()
			switch {
			case coarse:
				imprecise++
			case err == nil:
				done++
				e := plan.Right[p]
				e.ModTime = want
				plan.Right[p] = e
			default:
				failed++
				if len(plan.DatesFailedWhy) < 20 {
					plan.DatesFailedWhy = append(plan.DatesFailedWhy, p+" : "+err.Error())
				}
			}
			n++
			if done == 0 && imprecise >= 20 && imprecise == n-failed {
				stop = true
				plan.CoarseFound = true
			}
			if onProg != nil && (n%50 == 0 || n == len(todo)) {
				onProg(n, len(todo))
			}
		}(p)
	}
	wg.Wait()
	// aucune date gardée telle quelle, toutes arrondies : le disque arrondit (même
	// avec 1 seul fichier, la relecture le prouve)
	if done == 0 && imprecise > 0 {
		plan.CoarseFound = true
	}
	plan.DatesFixed, plan.DatesFailed, plan.DatesImprecise = done, failed, imprecise
	plan.DatesTodo = len(todo)
	fillSides(plan)
}
