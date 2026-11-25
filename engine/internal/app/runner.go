package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"synkro/internal/syncer"
	"synkro/internal/vfs"
)

// Status est l'état courant d'un job, interrogé par l'interface.
type Status struct {
	State     string          `json:"state"` // idle, analyzing, syncing, done, error
	Message   string          `json:"message"`
	Progress  syncer.Progress `json:"progress"`
	StartedAt time.Time       `json:"startedAt"`
	Summary   *Summary        `json:"summary,omitempty"`
	Paused    bool            `json:"paused,omitempty"` // synchro en pause
	SpeedKBps *int            `json:"speedKBps,omitempty"` // limite de vitesse de la synchro en cours (0 = illimitée)
	Scan      *ScanProgress   `json:"scan,omitempty"`   // lecture des deux côtés en cours
	Fix       *FixProgress    `json:"fix,omitempty"`    // correction des dates en cours
}

// FixProgress : avancement de « Corriger les dates ».
type FixProgress struct {
	Done  int   `json:"done"`
	Total int   `json:"total"`
	MsEl  int64 `json:"elapsedMs"`
}

// ScanProgress : avancement de la lecture des deux côtés pendant une analyse.
type ScanProgress struct {
	Side  string           `json:"side"` // côté en cours de lecture : "left" ou "right"
	Left  syncer.ScanCount `json:"left"`
	Right syncer.ScanCount `json:"right"`
	// Prev : éléments lus lors de l'analyse précédente (0 = inconnu)
	Prev [2]int `json:"prev"`
}

// Summary résume une analyse ou une synchro.
type Summary struct {
	ToRight   int   `json:"toRight"`
	ToLeft    int   `json:"toLeft"`
	DelLeft   int   `json:"delLeft"`
	DelRight  int   `json:"delRight"`
	Conflicts int   `json:"conflicts"`
	Unchanged int   `json:"unchanged"`
	Bytes     int64 `json:"bytes"`
	Errors    int   `json:"errors"`
	Canceled  int   `json:"canceled,omitempty"` // actions non faites car la synchro a été annulée
	Synced    bool  `json:"synced"`
	// fichiers identiques mais dates décalées de 1 ou 2 s (tâches à sens unique)
	DatesToFix int `json:"datesToFix,omitempty"`
}

type jobRun struct {
	status  Status
	cancel  context.CancelFunc
	plan    []syncer.Action
	results []syncer.Result
	// marks donne l'état de chaque action du plan, mis à jour pendant la synchro :
	// '.' en attente, 'o' réussie, 'x' échec, '-' annulée.
	marks     []byte
	errs      map[int]string
	canceled  bool
	same      []syncer.Action // éléments identiques, affichables à la demande
	conflicts []Conflict      // conflits en attente de choix (état "conflicts")
	gate      *syncer.Gate    // pause / reprise de la synchro
}

// Marks est l'état des actions envoyé à l'interface.
type Marks struct {
	Marks  string         `json:"marks"`
	Errors map[int]string `json:"errors"`
}

// Runner exécute les jobs (un passage à la fois par job).
type Runner struct {
	store  *Store
	mu     sync.Mutex
	runs   map[string]*jobRun
	active map[string]activeJob // jobs en cours et dossiers qu'ils réservent
	// sens choisis à la main dans l'interface (job → chemin → sens), appliqués
	// à la prochaine synchronisation demandée depuis l'interface
	ov map[string]map[string]string
	// dernière analyse de chaque job, réutilisée par la synchro qui suit
	analyzed map[string]analyzedPlan
	// informations des serveurs SFTP (job → côté), relevées à chaque passage
	remote map[string]map[string]vfs.SFTPInfo
	// ordre de copie imposé par la liste triée (job → chemins), pour le prochain passage
	order map[string][]string
	// datesOnly : prochain passage = correction des dates seulement (job → oui)
	datesOnly map[string]bool
}

// RemoteInfo renvoie les informations des serveurs SFTP du job (côté → infos).
func (r *Runner) RemoteInfo(id string) map[string]vfs.SFTPInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.remote[id]
}

// noteRemote relève les informations des côtés SFTP (en arrière-plan : ne
// retarde pas l'analyse).
func (r *Runner) noteRemote(id string, sides map[string]vfs.FS) {
	for side, fs := range sides {
		sf, ok := fs.(*vfs.SFTP)
		if !ok {
			continue
		}
		in := sf.Info()
		r.mu.Lock()
		if r.remote == nil {
			r.remote = map[string]map[string]vfs.SFTPInfo{}
		}
		if r.remote[id] == nil {
			r.remote[id] = map[string]vfs.SFTPInfo{}
		}
		r.remote[id][side] = in
		r.mu.Unlock()
		Debugf("serveur SFTP (%s) : %s · %s · libre %d / %d", side, in.Host, in.Software, in.Free, in.Total)
	}
}

// analyzedPlan : résultat d'une analyse, valable tant que le job n'a pas changé.
type analyzedPlan struct {
	plan *syncer.Plan
	at   time.Time
	sig  string // empreinte du job (côtés + options) au moment de l'analyse
}

// analysisMaxAge : au-delà, la synchro relit les deux côtés.
const analysisMaxAge = time.Hour

func jobSig(j Job) string {
	b, _ := json.Marshal([]any{j.Left, j.Right, j.Options})
	return string(b)
}

// reusable renvoie l'analyse encore valable pour ce job (nil sinon).
func (r *Runner) reusable(j Job) *syncer.Plan {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.analyzed[j.ID]
	if !ok || time.Since(a.at) > analysisMaxAge || a.sig != jobSig(j) {
		return nil
	}
	return a.plan
}

func (r *Runner) setAnalyzed(id string, a *analyzedPlan) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if a == nil {
		delete(r.analyzed, id)
	} else {
		r.analyzed[id] = *a
	}
}

func (r *Runner) setOverrides(id string, ov map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(ov) == 0 {
		delete(r.ov, id)
		return
	}
	r.ov[id] = ov
}

func (r *Runner) overrides(id string) map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ov[id]
}

func NewRunner(s *Store) *Runner {
	return &Runner{store: s, runs: map[string]*jobRun{}, active: map[string]activeJob{}, ov: map[string]map[string]string{}, analyzed: map[string]analyzedPlan{}}
}

func (r *Runner) Status(id string) Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	if jr, ok := r.runs[id]; ok {
		return jr.status
	}
	return Status{State: "idle"}
}

// Marks renvoie l'état courant de chaque action du plan.
func (r *Runner) Marks(id string) Marks {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := Marks{Errors: map[int]string{}}
	if jr, ok := r.runs[id]; ok {
		m.Marks = string(jr.marks)
		for k, v := range jr.errs {
			m.Errors[k] = v
		}
	}
	return m
}

// Details renvoie le plan (après analyse) ou les résultats (après synchro).
// Same renvoie les éléments identiques du dernier passage.
func (r *Runner) Same(id string) []syncer.Action {
	r.mu.Lock()
	defer r.mu.Unlock()
	if jr, ok := r.runs[id]; ok {
		return jr.same
	}
	return nil
}

func (r *Runner) Details(id string) ([]syncer.Action, []syncer.Result) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if jr, ok := r.runs[id]; ok {
		return jr.plan, jr.results
	}
	return nil, nil
}

// Busy : le job tourne, attend qu'un dossier se libère, ou est en file d'attente.
func (r *Runner) Busy(id string) bool {
	switch r.Status(id).State {
	case "analyzing", "syncing", "waiting", "queued":
		return true
	}
	return false
}

func (r *Runner) Cancel(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	jr, ok := r.runs[id]
	if !ok {
		return
	}
	if jr.status.State == "queued" { // pas encore démarré : on le retire de la file
		jr.status.State = "done"
		jr.status.Message = "Retiré de la file d'attente"
		return
	}
	if jr.cancel != nil {
		jr.canceled = true
		jr.cancel()
	}
}

// Pause met la synchro en cours en pause ; Resume la reprend.
func (r *Runner) Pause(id string) error  { return r.pauseResume(id, true) }
func (r *Runner) Resume(id string) error { return r.pauseResume(id, false) }

func (r *Runner) pauseResume(id string, pause bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	jr, ok := r.runs[id]
	if !ok || jr.gate == nil || (jr.status.State != "syncing" && jr.status.State != "waiting") {
		return errors.New("aucune synchronisation en cours pour ce job")
	}
	if pause {
		jr.gate.Pause()
	} else {
		jr.gate.Resume()
	}
	jr.status.Paused = pause
	return nil
}

// SetSpeed change la limite de vitesse de la synchro en cours (Ko/s, 0 = illimitée),
// sans l'interrompre : les copies la prennent en compte au bloc suivant.
func (r *Runner) SetSpeed(id string, kbps int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	jr, ok := r.runs[id]
	if !ok || jr.gate == nil || (jr.status.State != "syncing" && jr.status.State != "waiting") {
		return errors.New("aucune synchronisation en cours pour ce job")
	}
	k := max(kbps, 0)
	jr.gate.SetMaxKBps(k)
	jr.status.SpeedKBps = &k
	return nil
}

// CancelAll annule le job en cours et tous ceux qui attendent (dossier partagé ou file).
func (r *Runner) CancelAll() {
	r.mu.Lock()
	var ids []string
	for id, jr := range r.runs {
		switch jr.status.State {
		case "analyzing", "syncing", "waiting", "queued", "conflicts":
			ids = append(ids, id)
		}
	}
	r.mu.Unlock()
	// d'abord ceux qui attendent (pour qu'aucun ne démarre), puis ceux en cours
	for _, pass := range []bool{true, false} {
		for _, id := range ids {
			st := r.Status(id).State
			if (st == "waiting" || st == "queued") == pass {
				r.Cancel(id)
			}
		}
	}
}

func (r *Runner) wasCanceled(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	jr, ok := r.runs[id]
	return ok && jr.canceled
}

func (r *Runner) set(id string, fn func(*jobRun)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	jr := r.runs[id]
	if jr == nil {
		jr = &jobRun{}
		r.runs[id] = jr
	}
	fn(jr)
}

// open connecte un côté ; enregistre l'empreinte SFTP vue au premier contact.
func (r *Runner) open(jobID string, e Endpoint, side string) (vfs.FS, error) {
	switch e.Type {
	case "local":
		return vfs.NewLocal(e.Path)
	case "usb":
		dir, err := e.Resolve()
		if err != nil {
			return nil, err // ex : « la clé "xxx" (n° …) n'est pas branchée »
		}
		r.store.AppendLog(jobID, fmt.Sprintf("Clé %s trouvée : %s", e.Volume, dir))
		return vfs.NewLocal(dir)
	case "sftp":
		return vfs.DialSFTP(*e.SFTP, func(fp string) {
			r.store.AppendLog(jobID, fmt.Sprintf("Nouvelle clé d'hôte mémorisée pour %s : %s", e.SFTP.Host, fp))
			r.store.update(jobID, func(j *Job) {
				ep := &j.Left
				if side == "right" {
					ep = &j.Right
				}
				if ep.SFTP != nil {
					ep.SFTP.HostKey = fp
				}
			})
		})
	}
	return nil, fmt.Errorf("type %q inconnu", e.Type)
}

// TestEndpoint vérifie qu'un côté est accessible (sans mémoriser d'empreinte).
func TestEndpoint(e Endpoint) (string, error) {
	var fs vfs.FS
	var err error
	fp := ""
	switch e.Type {
	case "local":
		fs, err = vfs.NewLocal(e.Path)
	case "usb":
		var dir string
		if dir, err = e.Resolve(); err == nil {
			fs, err = vfs.NewLocal(dir)
		}
	case "sftp":
		if e.SFTP == nil {
			return "", errors.New("configuration SFTP manquante")
		}
		fs, err = vfs.DialSFTP(*e.SFTP, func(f string) { fp = f })
	default:
		return "", fmt.Errorf("type %q inconnu", e.Type)
	}
	if err != nil {
		return "", err
	}
	defer fs.Close()
	msg := "Connexion réussie : " + fs.Describe()
	if sf, ok := fs.(*vfs.SFTP); ok {
		in := sf.Info()
		var parts []string
		for _, x := range []string{in.Host, in.Protocol, in.Software} {
			if x != "" {
				parts = append(parts, x)
			}
		}
		msg += "\n" + strings.Join(parts, " · ")
		if in.Total > 0 {
			msg += fmt.Sprintf("\nLibre : %s sur %s", humanBytes(in.Free), humanBytes(in.Total))
		}
	}
	if fp != "" {
		msg += "\nEmpreinte du serveur : " + fp
	}
	return msg, nil
}

// Start lance une analyse (doSync=false) ou une synchronisation en arrière-plan,
// demandée par l'utilisateur (les conflits pourront lui être présentés).
func (r *Runner) Start(id string, doSync bool) error { return r.StartWith(id, doSync, nil) }

// StartWith lance un passage en imposant le sens de certains éléments
// (chemin → "right", "left" ou "none"), choisis dans l'interface après l'analyse.
func (r *Runner) StartWith(id string, doSync bool, ov map[string]string, order ...[]string) error {
	if r.Busy(id) {
		return errors.New("ce job est déjà en cours ou en file d'attente")
	}
	r.setOverrides(id, ov)
	r.mu.Lock()
	if r.order == nil {
		r.order = map[string][]string{}
	}
	if len(order) > 0 && len(order[0]) > 0 {
		r.order[id] = order[0]
	} else {
		delete(r.order, id)
	}
	r.mu.Unlock()
	return r.start(id, doSync, true, nil, false)
}

// StartFixDates corrige seulement les dates des fichiers identiques à 1 ou 2 s
// près (à partir de l'analyse affichée), sans rien copier.
func (r *Runner) StartFixDates(id string) error {
	if r.Busy(id) {
		return errors.New("ce job est déjà en cours ou en file d'attente")
	}
	r.mu.Lock()
	if r.datesOnly == nil {
		r.datesOnly = map[string]bool{}
	}
	r.datesOnly[id] = true
	delete(r.order, id)
	r.mu.Unlock()
	r.setOverrides(id, nil)
	return r.start(id, true, true, nil, false)
}

func (r *Runner) takeDatesOnly(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	v := r.datesOnly[id]
	delete(r.datesOnly, id)
	return v
}

// fixDatesOnly : passage « corriger les dates » (aucune copie, aucune suppression).
func (r *Runner) fixDatesOnly(ctx context.Context, job Job, plan *syncer.Plan, right vfs.FS, sum *Summary) {
	total := len(syncer.DatesToFix(plan, job.Options.Mode, job.RightCoarse))
	r.set(job.ID, func(jr *jobRun) {
		jr.plan, jr.same = plan.Actions, plan.Same
		jr.marks, jr.errs = pendingMarks(len(plan.Actions)), map[int]string{}
		jr.status.Summary = sum
		jr.status.Message = fmt.Sprintf("Correction des dates : 0/%d", total)
		jr.status.Fix = &FixProgress{Total: total}
	})
	t0 := time.Now()
	if job.Options.Mode != syncer.TwoWay {
		syncer.FixDates(ctx, plan, right, 8, job.RightCoarse, func(done, n int) {
			r.set(job.ID, func(jr *jobRun) {
				jr.status.Message = fmt.Sprintf("Correction des dates : %d/%d", done, n)
				jr.status.Fix = &FixProgress{Done: done, Total: n, MsEl: time.Since(t0).Milliseconds()}
			})
		})
	}
	coarseMsg := r.learnCoarse(&job, plan)
	msg := fmt.Sprintf("Dates corrigées : %d sur %d", plan.DatesFixed, total)
	if coarseMsg != "" {
		msg = coarseMsg
		if plan.DatesFixed > 0 {
			msg += fmt.Sprintf(" (%d date(s) corrigée(s) quand même)", plan.DatesFixed)
		}
	} else if plan.DatesImprecise > 0 {
		msg += fmt.Sprintf(" · %d arrondie(s) par le disque de droite (dates à 2 s près)", plan.DatesImprecise)
	}
	if plan.DatesFailed > 0 {
		msg += fmt.Sprintf(" · %d impossible(s)", plan.DatesFailed)
	}
	if ctx.Err() != nil && coarseMsg == "" {
		msg = fmt.Sprintf("Correction des dates annulée : %d date(s) déjà corrigée(s) (elles le restent), %d restante(s)",
			plan.DatesFixed, len(syncer.DatesToFix(plan, job.Options.Mode, job.RightCoarse)))
	}
	r.store.AppendLog(job.ID, msg)
	log.Printf("job %q : %s", job.Name, msg)
	sum.DatesToFix = len(syncer.DatesToFix(plan, job.Options.Mode, job.RightCoarse))
	r.set(job.ID, func(jr *jobRun) {
		jr.status.State = "done"
		jr.status.Message = msg
		jr.status.Summary = sum
		jr.status.Fix = nil
	})
	short := msg
	if coarseMsg != "" {
		short = "Dates : écarts normaux (disque à 2 s près)"
	}
	r.store.update(job.ID, func(j *Job) { j.LastRun = time.Now(); j.LastResult = short })
}

// learnCoarse : si la correction a montré que la droite arrondit les dates à 2 s,
// on le retient pour la tâche (plus de « dates à corriger » pour ces écarts).
func (r *Runner) learnCoarse(job *Job, plan *syncer.Plan) string {
	if !plan.CoarseFound || job.RightCoarse {
		return ""
	}
	job.RightCoarse = true
	r.store.update(job.ID, func(j *Job) { j.RightCoarse = true })
	return "Le disque de droite n'enregistre les dates qu'à 2 s près (FAT, ou exFAT sous Windows) : ces écarts sont normaux et ne peuvent pas être corrigés. Synkro ne les signalera plus pour cette tâche."
}

// StartAuto lance une synchronisation automatique (planificateur) : personne pour
// répondre, les conflits sont traités sans poser de question.
func (r *Runner) StartAuto(id string) error {
	if r.Busy(id) {
		return errors.New("ce job est déjà en cours ou en file d'attente")
	}
	r.setOverrides(id, nil)
	return r.start(id, true, false, nil, false)
}

// Resolve reprend une synchronisation en attente avec le choix fait pour chaque
// conflit (chemin → "left", "right" ou "leave"), ou l'abandonne.
func (r *Runner) Resolve(id string, choices map[string]string, abort bool) error {
	if r.Status(id).State != "conflicts" {
		return errors.New("aucun conflit en attente pour ce job")
	}
	if abort {
		r.set(id, func(jr *jobRun) {
			jr.status.State = "done"
			jr.status.Message = "Synchronisation abandonnée : aucun fichier n'a été copié"
			jr.conflicts = nil
		})
		r.store.AppendLog(id, "Synchronisation abandonnée à l'étape des conflits")
		return nil
	}
	if choices == nil {
		choices = map[string]string{}
	}
	return r.start(id, true, true, choices, false)
}

// Conflicts renvoie les conflits en attente de choix.
func (r *Runner) Conflicts(id string) []Conflict {
	r.mu.Lock()
	defer r.mu.Unlock()
	if jr, ok := r.runs[id]; ok && jr.conflicts != nil {
		return jr.conflicts
	}
	return []Conflict{}
}

// Conflict est un fichier à trancher, avec le choix proposé par défaut.
type Conflict struct {
	syncer.Action
	Default string `json:"default"`
}

func (r *Runner) start(id string, doSync, interactive bool, choices map[string]string, fromQueue bool) error {
	job, err := r.store.Get(id)
	if err != nil {
		return err
	}
	if r.Busy(job.ID) && !(fromQueue && r.Status(job.ID).State == "queued") {
		return errors.New("ce job est déjà en cours ou en file d'attente")
	}
	ctx, cancel := context.WithCancel(context.Background())
	state := "analyzing"
	if doSync {
		state = "syncing"
	}
	reuse := doSync && interactive && r.reusable(job) != nil
	r.set(job.ID, func(jr *jobRun) {
		old := *jr
		// "waiting" tant que les dossiers ne sont pas réservés (voir acquire)
		*jr = jobRun{status: Status{State: "waiting", Message: "Démarrage…", StartedAt: time.Now()}, cancel: cancel}
		if doSync {
			// pause et vitesse utilisables dès le lancement, pendant la lecture des
			// deux côtés aussi : les copies attendront la reprise
			jr.gate = &syncer.Gate{}
			sp := max(job.Options.MaxKBps, 0)
			jr.status.SpeedKBps = &sp
		}
		if reuse { // la synchro part de l'analyse affichée : on garde la liste à l'écran
			jr.plan, jr.same, jr.marks, jr.errs = old.plan, old.same, pendingMarks(len(old.plan)), map[int]string{}
			jr.status.Summary = old.status.Summary
		}
	})
	go func() {
		// un autre job utilise un de nos dossiers : on attend qu'il ait fini
		if err := r.acquire(ctx, job); err != nil {
			r.set(job.ID, func(jr *jobRun) { jr.status.State = "done"; jr.status.Message = "Annulé avant le démarrage" })
			cancel()
			return
		}
		defer r.release(job.ID)
		r.set(job.ID, func(jr *jobRun) { jr.status.State = state; jr.status.Message = "Connexion…" })
		r.run(ctx, cancel, job, doSync, interactive, choices)
	}()
	return nil
}

// RunBlocking exécute un passage complet et attend la fin (ligne de commande).
func (r *Runner) RunBlocking(id string, doSync bool, onProg func(syncer.Progress)) (Status, []syncer.Action, []syncer.Result) {
	job, err := r.store.Get(id)
	if err != nil {
		return Status{State: "error", Message: err.Error()}, nil, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := r.acquire(ctx, job); err != nil {
		cancel()
		return Status{State: "error", Message: err.Error()}, nil, nil
	}
	defer r.release(job.ID)
	r.runSync(ctx, cancel, job, doSync, false, nil, onProg)
	plan, res := r.Details(job.ID)
	return r.Status(job.ID), plan, res
}

func (r *Runner) run(ctx context.Context, cancel context.CancelFunc, job Job, doSync, interactive bool, choices map[string]string) {
	// Un plantage pendant un passage devient une erreur du job : le moteur reste en vie.
	defer func() {
		if p := recover(); p != nil {
			stack := string(debug.Stack())
			log.Printf("PLANTAGE du job %s : %v\n%s", job.Name, p, stack)
			r.store.AppendLog(job.ID, fmt.Sprintf("PLANTAGE INTERNE : %v", p), stack)
			r.fail(job, fmt.Errorf("erreur interne du moteur : %v (détails dans le journal)", p))
		}
	}()
	r.runSync(ctx, cancel, job, doSync, interactive, choices, nil)
}

func (r *Runner) fail(job Job, err error) {
	msg := err.Error()
	if r.wasCanceled(job.ID) || errors.Is(err, context.Canceled) {
		// arrêt voulu pendant la lecture des dossiers : rien n'a été touché,
		// ce n'est pas une erreur et le résultat du dernier passage reste valable
		msg = "Analyse annulée : rien n'a été copié ni supprimé"
		r.set(job.ID, func(jr *jobRun) { jr.status.State = "done"; jr.status.Message = msg })
		log.Printf("job %q : analyse annulée", job.Name)
		r.store.AppendLog(job.ID, msg)
		return
	}
	r.set(job.ID, func(jr *jobRun) { jr.status.State = "error"; jr.status.Message = msg })
	log.Printf("job %q : ERREUR %v", job.Name, err)
	r.store.AppendLog(job.ID, "ERREUR : "+msg)
	r.store.update(job.ID, func(j *Job) { j.LastRun = time.Now(); j.LastResult = "Erreur : " + msg })
}

func pendingMarks(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = '.'
	}
	return b
}

func summarize(p *syncer.Plan) *Summary {
	s := &Summary{Conflicts: p.Conflicts, Unchanged: p.Unchanged, Bytes: p.TotalBytes}
	for _, a := range p.Actions {
		switch a.Kind {
		case syncer.CopyLR, syncer.MkdirR:
			s.ToRight++
		case syncer.CopyRL, syncer.MkdirL:
			s.ToLeft++
		case syncer.DelL:
			s.DelLeft++
		case syncer.DelR:
			s.DelRight++
		case syncer.Skip:
			s.Errors++
		}
	}
	return s
}

func (r *Runner) runSync(ctx context.Context, cancel context.CancelFunc, job Job, doSync, interactive bool, choices map[string]string, onProg func(syncer.Progress)) {
	defer cancel()
	verb := "Analyse"
	if doSync {
		verb = "Synchronisation"
	}
	r.store.AppendLog(job.ID, fmt.Sprintf("── %s : %s ⇄ %s (%s)", verb, job.Left, job.Right, job.Options.Mode))
	t0 := time.Now()
	log.Printf("job %q : %s démarrée (%s ⇄ %s, %s)", job.Name, verb, job.Left, job.Right, job.Options.Mode)
	defer func() {
		st := r.Status(job.ID)
		log.Printf("job %q : %s finie en %s → %s : %s", job.Name, verb, fmtDur(time.Since(t0)), st.State, st.Message)
	}()

	left, err := r.open(job.ID, job.Left, "left")
	if err != nil {
		r.fail(job, fmt.Errorf("côté gauche : %w", err))
		return
	}
	defer left.Close()
	right, err := r.open(job.ID, job.Right, "right")
	if err != nil {
		r.fail(job, fmt.Errorf("côté droit : %w", err))
		return
	}
	defer right.Close()
	r.setupShadows(job, left, right, doSync)
	// Annulation immédiate : on coupe les connexions, ce qui interrompt aussi les
	// envois en cours (sinon il faudrait attendre la fin de chaque bloc).
	stopClose := context.AfterFunc(ctx, func() { left.Close(); right.Close() })
	defer stopClose()

	prev := r.store.LoadState(job.ID)
	go r.noteRemote(job.ID, map[string]vfs.FS{"left": left, "right": right})
	var plan *syncer.Plan
	if doSync && interactive {
		plan = r.reusable(job) // analyse récente du même job : pas besoin de tout relire
	}
	if plan != nil {
		r.store.AppendLog(job.ID, "Synchronisation à partir de l'analyse affichée (sans relire les deux côtés)")
	} else {
		if plan, err = r.scanAndPlan(ctx, job, left, right, prev); err != nil {
			r.fail(job, err)
			return
		}
	}
	if !doSync {
		r.setAnalyzed(job.ID, &analyzedPlan{plan: plan, at: time.Now(), sig: jobSig(job)})
	}
	r.continueSync(ctx, job, plan, prev, left, right, doSync, interactive, choices, onProg)
}

// scanAndPlan lit les deux côtés et calcule les actions.
func (r *Runner) scanAndPlan(ctx context.Context, job Job, left, right vfs.FS, prev syncer.State) (*syncer.Plan, error) {
	ts := time.Now()
	defer func() { Debugf("job %q : lecture des deux côtés en %s", job.Name, fmtDur(time.Since(ts))) }()
	job.Options.Own = ownDataDirs(r.store.Dir, left, right)
	skL, skR := map[string]syncer.Skipped{}, map[string]syncer.Skipped{}
	sp := ScanProgress{Side: "left", Prev: job.LastScan}
	show := func(msg string) {
		cp := sp
		r.set(job.ID, func(jr *jobRun) { jr.status.Message = msg; jr.status.Scan = &cp })
	}
	defer r.set(job.ID, func(jr *jobRun) { jr.status.Scan = nil })
	show("Lecture du côté gauche…")
	ls, err := syncer.ScanWith(ctx, left, job.Options, func(c syncer.ScanCount) {
		sp.Left = c
		show(fmt.Sprintf("Lecture du côté gauche… %d éléments", c.Dirs+c.Files))
	}, skL)
	if err != nil {
		return nil, fmt.Errorf("lecture gauche : %w", err)
	}
	sp.Side = "right"
	show("Lecture du côté droit…")
	rs, err := syncer.ScanWith(ctx, right, job.Options, func(c syncer.ScanCount) {
		sp.Right = c
		show(fmt.Sprintf("Lecture du côté droit… %d éléments", c.Dirs+c.Files))
	}, skR)
	if err != nil {
		return nil, fmt.Errorf("lecture droite : %w", err)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	r.store.update(job.ID, func(j *Job) { j.LastScan = [2]int{len(ls), len(rs)} })
	syncer.AlignExcluded(ls, rs, skL, skR)
	plan, err := syncer.BuildPlan(ls, rs, prev, job.Options)
	if err == nil {
		plan.AddExcluded(skL, skR)
	}
	return plan, err
}

// actionWhy décrit les deux côtés d'une action pour le journal détaillé.
func actionWhy(a syncer.Action) string {
	side := func(x *syncer.Side) string {
		if x == nil || !x.Exists {
			return "absent"
		}
		if a.IsDir {
			return "dossier"
		}
		return fmt.Sprintf("%d o, %s", x.Size, x.ModTime.Format("2006-01-02 15:04:05"))
	}
	return fmt.Sprintf(" (gauche : %s ; droite : %s)", side(a.Left), side(a.Right))
}

// humanBytes : taille lisible (« 47,8 Go »).
func humanBytes(n uint64) string {
	u := []string{"o", "Ko", "Mo", "Go", "To"}
	f, i := float64(n), 0
	for f >= 1024 && i < len(u)-1 {
		f /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d o", n)
	}
	return strings.Replace(fmt.Sprintf("%.1f %s", f, u[i]), ".", ",", 1)
}

// ownDataDirs renvoie, sous forme de motifs ancrés, le dossier de données de
// Synkro (réglages, journaux, états) s'il se trouve dans un des côtés du job :
// le synchroniser n'a pas de sens et il change à chaque passage.
func ownDataDirs(dataDir string, sides ...vfs.FS) []string {
	if dataDir == "" {
		return nil
	}
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return nil
	}
	var out []string
	for _, fs := range sides {
		l, ok := fs.(*vfs.Local)
		if !ok {
			continue
		}
		rel, err := filepath.Rel(l.Root, abs)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		out = append(out, "/"+filepath.ToSlash(rel))
	}
	return out
}

// continueSync termine un passage : résumé d'analyse, ou conflits puis exécution.
func (r *Runner) continueSync(ctx context.Context, job Job, plan *syncer.Plan, prev syncer.State, left, right vfs.FS, doSync, interactive bool, choices map[string]string, onProg func(syncer.Progress)) {
	if doSync && interactive {
		r.mu.Lock()
		job.Options.Order = r.order[job.ID]
		r.mu.Unlock()
		if ov := r.overrides(job.ID); len(ov) > 0 {
			syncer.ApplyOverrides(plan, ov)
			r.store.AppendLog(job.ID, fmt.Sprintf("%d élément(s) avec un sens choisi à la main", len(ov)))
		}
	}
	sum := summarize(plan)
	sum.DatesToFix = len(syncer.DatesToFix(plan, job.Options.Mode, job.RightCoarse))
	if doSync && r.takeDatesOnly(job.ID) {
		r.fixDatesOnly(ctx, job, plan, right, sum)
		return
	}
	if doSync && !interactive && job.Options.MaxChanges > 0 {
		if n := sum.ToRight + sum.ToLeft + sum.DelLeft + sum.DelRight; n > job.Options.MaxChanges {
			msg := fmt.Sprintf("%d modification(s) prévue(s), plus que la limite fixée (%d) : synchronisation automatique non faite", n, job.Options.MaxChanges)
			r.store.AppendLog(job.ID, msg)
			r.set(job.ID, func(jr *jobRun) {
				jr.status.State = "error"
				jr.status.Message = msg
				jr.status.Summary = sum
			})
			r.store.update(job.ID, func(j *Job) { j.LastResult = "Non synchronisé : trop de modifications" })
			return
		}
	}
	if !doSync && Debug {
		// journal détaillé : ce que l'analyse propose (pour comprendre a posteriori)
		for i, a := range plan.Actions {
			if i == 300 {
				Debugf("job %q : … %d autres actions", job.Name, len(plan.Actions)-i)
				break
			}
			Debugf("job %q : à faire %s %s%s", job.Name, a.Kind, a.Path, actionWhy(a))
		}
	}
	if !doSync {
		r.set(job.ID, func(jr *jobRun) {
			jr.plan = plan.Actions
			jr.same = plan.Same
			jr.marks, jr.errs = pendingMarks(len(plan.Actions)), map[int]string{}
			jr.status.State = "done"
			jr.status.Summary = sum
			jr.status.Message = fmt.Sprintf("Analyse terminée : %d action(s), %d inchangé(s)", len(plan.Actions), plan.Unchanged)
		})
		r.store.AppendLog(job.ID, fmt.Sprintf("Analyse : %d action(s), %d conflit(s), %d inchangé(s)", len(plan.Actions), plan.Conflicts, plan.Unchanged))
		if len(plan.Actions) == 0 { // rien à faire : les deux côtés sont à jour, c'est un passage complet
			r.store.update(job.ID, func(j *Job) { j.LastRun = time.Now(); j.LastResult = "À jour, rien à faire" })
		}
		return
	}

	// Conflits (mode bidirectionnel) : demander, ou appliquer la règle par défaut.
	rule := job.Options.ConflictDefault
	var pending []Conflict
	for _, a := range plan.Actions {
		if syncer.IsChoosable(a) {
			pending = append(pending, Conflict{Action: a, Default: syncer.DefaultChoice(a, rule)})
		}
	}
	if len(pending) > 0 {
		switch {
		case choices != nil: // l'utilisateur a choisi fichier par fichier
			syncer.ResolveConflicts(plan, func(a syncer.Action) string {
				if c, ok := choices[a.Path]; ok {
					return c
				}
				return syncer.DefaultChoice(a, rule)
			})
		case job.Options.ConflictAsk && interactive: // on s'arrête pour demander
			r.set(job.ID, func(jr *jobRun) {
				jr.plan = plan.Actions
				jr.same = plan.Same
				jr.marks, jr.errs = pendingMarks(len(plan.Actions)), map[int]string{}
				jr.conflicts = pending
				jr.status.State = "conflicts"
				jr.status.Summary = sum
				jr.status.Message = fmt.Sprintf("%d conflit(s) à résoudre avant de synchroniser", len(pending))
			})
			r.store.AppendLog(job.ID, fmt.Sprintf("%d conflit(s) : en attente du choix de l'utilisateur", len(pending)))
			return
		case job.Options.ConflictAsk: // synchro automatique : personne pour répondre
			syncer.ResolveConflicts(plan, func(syncer.Action) string { return syncer.ChooseLeave })
			for _, c := range pending {
				r.store.AppendLog(job.ID, "conflit laissé en attente (à trancher lors d'une synchro manuelle) : "+c.Path)
			}
		default: // règle par défaut, sans question
			syncer.ResolveConflicts(plan, func(a syncer.Action) string { return syncer.DefaultChoice(a, rule) })
		}
		sum = summarize(plan)
	}

	r.set(job.ID, func(jr *jobRun) {
		jr.plan = plan.Actions
		jr.same = plan.Same
		jr.conflicts = nil
		jr.marks, jr.errs = pendingMarks(len(plan.Actions)), map[int]string{}
		jr.status.Summary = sum
		jr.status.Message = fmt.Sprintf("%d action(s) à effectuer", len(plan.Actions))
	})
	r.setAnalyzed(job.ID, nil) // l'analyse est consommée : la prochaine synchro relira tout
	// la pause et la vitesse créées au lancement (voir start) ; sinon (synchro
	// lancée autrement), créées ici
	var gate *syncer.Gate
	r.set(job.ID, func(jr *jobRun) {
		if jr.gate == nil {
			jr.gate = &syncer.Gate{}
			sp := max(job.Options.MaxKBps, 0)
			jr.status.SpeedKBps = &sp
		}
		gate = jr.gate
	})
	job.Options.CoarseDates = job.RightCoarse
	tFix := time.Time{}
	job.Options.OnFixDates = func(done, n int) { // dernière étape : dates des fichiers identiques
		if tFix.IsZero() {
			tFix = time.Now()
		}
		r.set(job.ID, func(jr *jobRun) {
			jr.status.Message = fmt.Sprintf("Correction des dates : %d/%d", done, n)
			jr.status.Fix = &FixProgress{Done: done, Total: n, MsEl: time.Since(tFix).Milliseconds()}
		})
	}
	defer r.set(job.ID, func(jr *jobRun) { jr.gate = nil; jr.status.Paused = false; jr.status.SpeedKBps = nil })
	newState, results := syncer.Execute(syncer.WithGate(ctx, gate), plan, left, right, prev, job.Options, func(p syncer.Progress) {
		r.set(job.ID, func(jr *jobRun) { jr.status.Progress = p })
		if onProg != nil {
			onProg(p)
		}
	}, func(i int, res syncer.Result) {
		r.set(job.ID, func(jr *jobRun) {
			if i >= len(jr.marks) {
				return
			}
			switch {
			case res.Err == "":
				jr.marks[i] = 'o'
			case res.Err == "annulé":
				jr.marks[i] = '-'
			default:
				jr.marks[i] = 'x'
				jr.errs[i] = res.Err
			}
		})
		if res.Err != "" && res.Err != "annulé" {
			log.Printf("job %q : échec %s %s : %s", job.Name, res.Kind, res.Path, res.Err)
		} else if res.Note != "" {
			log.Printf("job %q : %s %s : %s", job.Name, res.Kind, res.Path, res.Note)
		} else {
			Debugf("job %q : %s %s (%d o) %s", job.Name, res.Kind, res.Path, res.Size, map[bool]string{true: "OK", false: res.Err}[res.Err == ""])
		}
	})
	if err := r.store.SaveState(job.ID, newState); err != nil {
		r.store.AppendLog(job.ID, "ERREUR : enregistrement de l'état : "+err.Error())
	}
	if plan.DatesFixed > 0 || plan.DatesFailed > 0 || plan.DatesImprecise > 0 {
		msg := fmt.Sprintf("Dates corrigées à droite (fichiers identiques à 1 ou 2 s près) : %d", plan.DatesFixed)
		if plan.DatesFailed > 0 {
			msg += fmt.Sprintf(", %d impossible(s)", plan.DatesFailed)
		}
		if plan.DatesImprecise > 0 {
			msg += fmt.Sprintf(", %d arrondie(s) par le disque de destination (FAT32 : dates à 2 s près, écart normal)", plan.DatesImprecise)
		}
		r.store.AppendLog(job.ID, msg)
		log.Printf("job %q : %s", job.Name, msg)
		for _, w := range plan.DatesFailedWhy {
			r.store.AppendLog(job.ID, "  date impossible à corriger : "+w)
			log.Printf("job %q : date impossible à corriger : %s", job.Name, w)
		}
	}
	nErr, nCanc := 0, 0
	var lines []string
	for _, res := range results {
		line := fmt.Sprintf("%-8s %s", res.Kind, res.Path)
		if res.Conflict {
			line += "  [conflit]"
		}
		switch res.Err {
		case "":
			if res.Note != "" {
				line += "  (" + res.Note + ")"
			}
		case "annulé": // pas une erreur : l'action n'a pas été faite
			nCanc++
			line += "  → annulé"
		default:
			nErr++
			line += "  → ERREUR : " + res.Err
		}
		lines = append(lines, line)
	}
	r.store.AppendLog(job.ID, lines...)
	sum.Errors, sum.Canceled = nErr, nCanc
	sum.Synced = true
	msg := fmt.Sprintf("Synchronisation terminée : %d action(s)", len(results))
	result := "OK"
	if nErr > 0 {
		msg += fmt.Sprintf(", %d erreur(s)", nErr)
		result = fmt.Sprintf("%d erreur(s)", nErr)
	}
	fixCut := ctx.Err() != nil && nCanc == 0 && plan.DatesTodo > 0 // copie finie, arrêt pendant les dates
	if fixCut {
		left := plan.DatesTodo - plan.DatesFixed - plan.DatesFailed - plan.DatesImprecise
		msg = fmt.Sprintf("Synchronisation faite (%d action(s)) · correction des dates annulée : %d corrigée(s) sur %d, %d restante(s)",
			len(results), plan.DatesFixed, plan.DatesTodo, left)
		result = fmt.Sprintf("Annulé : dates %d/%d corrigées (copie faite)", plan.DatesFixed, plan.DatesTodo)
		if nErr > 0 {
			msg += fmt.Sprintf(", %d erreur(s)", nErr)
			result += fmt.Sprintf(", %d erreur(s)", nErr)
		}
	} else if ctx.Err() != nil || nCanc > 0 {
		done := len(results) - nErr - nCanc
		msg = fmt.Sprintf("Synchronisation annulée : %d faite(s), %d annulée(s)", done, nCanc)
		result = fmt.Sprintf("Annulé : %d/%d fait(s)", done, len(results))
		if nErr > 0 {
			msg += fmt.Sprintf(", %d erreur(s)", nErr)
			result += fmt.Sprintf(", %d erreur(s)", nErr)
		}
	}
	if m := r.learnCoarse(&job, plan); m != "" {
		r.store.AppendLog(job.ID, m)
	}
	sum.DatesToFix = max(0, len(syncer.DatesToFix(plan, job.Options.Mode, job.RightCoarse))-plan.DatesImprecise)
	if plan.DatesFixed > 0 && !fixCut { // dates déjà corrigées : elles le restent, même si la synchro est annulée
		msg += fmt.Sprintf(" · %d date(s) corrigée(s)", plan.DatesFixed)
		result += fmt.Sprintf(" · %d date(s) corrigée(s)", plan.DatesFixed)
	}
	r.store.AppendLog(job.ID, msg)
	r.set(job.ID, func(jr *jobRun) {
		jr.status.Fix = nil
		jr.results = results
		jr.status.State = "done"
		if nErr > 0 && ctx.Err() == nil {
			jr.status.State = "error"
		}
		jr.status.Message = msg
		jr.status.Summary = sum
	})
	r.store.update(job.ID, func(j *Job) { j.LastRun = time.Now(); j.LastResult = result })
}

// Scheduler lance les jobs automatiques à intervalle régulier.
func (r *Runner) Scheduler(ctx context.Context) {
	go func() {
		defer func() {
			if p := recover(); p != nil {
				log.Printf("PLANTAGE de la surveillance des clés : %v\n%s", p, debug.Stack())
			}
		}()
		r.watchPlugs(ctx)
	}()
	go r.watchChanges(ctx)
	time.AfterFunc(3*time.Second, r.startOnLaunch)
	// planifications : vérifiées toutes les 15 s (et tout de suite au démarrage,
	// pour rattraper un lancement prévu pendant que Synkro était fermé)
	r.runSchedules(time.Now())
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	n := 0
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			r.runSchedules(now)
			if n++; n%2 == 0 {
				r.tick()
			}
		}
	}
}

// setupShadows : Volume Shadow Copy (réglages généraux) — fichiers verrouillés lus
// dans un instantané, ou instantané de la source avant une sauvegarde à sens unique.
func (r *Runner) setupShadows(job Job, left, right vfs.FS, doSync bool) {
	locked := r.store.Setting("vssLocked", "off") == "on"
	snap := doSync && r.store.Setting("vssSnapshot", "off") == "on" && job.Options.Mode != syncer.TwoWay
	logf := func(m string) { r.store.AppendLog(job.ID, m) }
	if l, ok := left.(*vfs.Local); ok && snap {
		set := vfs.NewShadowSet(logf)
		if err := l.Snapshot(set); err != nil {
			r.store.AppendLog(job.ID, "Instantané de la source impossible, lecture directe : "+err.Error())
			set.Close()
		} else {
			r.store.AppendLog(job.ID, "Source lue dans un instantané pris avant la synchronisation")
		}
	}
	if locked {
		for _, fs := range []vfs.FS{left, right} {
			if l, ok := fs.(*vfs.Local); ok && l.Shadows == nil {
				l.Shadows = vfs.NewShadowSet(logf)
			}
		}
	}
}

// StartLogoff lance les tâches prévues « à la fermeture de session » ; renvoie leur nombre.
func (r *Runner) StartLogoff() int {
	n := 0
	for _, j := range r.store.List() {
		if j.OnLogoff && !r.Busy(j.ID) {
			r.store.AppendLog(j.ID, "Fermeture de session : synchronisation automatique")
			if r.StartAuto(j.ID) == nil {
				n++
			}
		}
	}
	return n
}

// WaitIdle attend qu'aucune tâche ne tourne (au plus d).
func (r *Runner) WaitIdle(d time.Duration) {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		busy := false
		for _, j := range r.store.List() {
			if r.Busy(j.ID) {
				busy = true
				break
			}
		}
		if !busy {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// startOnLaunch : tâches à lancer au démarrage de Synkro.
func (r *Runner) startOnLaunch() {
	for _, j := range r.store.List() {
		if j.OnStart && !r.Busy(j.ID) {
			r.store.AppendLog(j.ID, "Démarrage de Synkro : synchronisation automatique")
			r.StartAuto(j.ID)
		}
	}
}

// reachable : le côté est-il accessible (dossier présent, clé branchée, serveur joignable) ?
func reachable(e Endpoint) bool {
	switch e.Type {
	case "local":
		_, err := os.Stat(e.Path)
		return err == nil
	case "usb":
		_, err := e.Resolve()
		return err == nil
	case "sftp":
		if e.SFTP == nil {
			return false
		}
		port := e.SFTP.Port
		if port == 0 {
			port = 22
		}
		c, err := net.DialTimeout("tcp", net.JoinHostPort(e.SFTP.Host, strconv.Itoa(port)), 3*time.Second)
		if err != nil {
			return false
		}
		c.Close()
		return true
	}
	return false
}

// watchPlugs synchronise un job dès que ses deux côtés deviennent accessibles
// (clé branchée, disque réseau connecté, serveur SFTP joignable) — option SyncOnPlug.
func (r *Runner) watchPlugs(ctx context.Context) {
	present := map[string]bool{} // job → clé présente au dernier contrôle
	first := true
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for n := 0; ; n++ {
		for _, j := range r.store.List() {
			if !j.SyncOnPlug {
				delete(present, j.ID)
				continue
			}
			// serveurs distants : contrôlés toutes les 30 s seulement
			remote := j.Left.Type == "sftp" || j.Right.Type == "sftp"
			if remote && n%10 != 0 {
				continue
			}
			ok := reachable(j.Left) && reachable(j.Right)
			// on ne lance qu'au branchement (absente → présente), pas au démarrage de Synkro
			if ok && !present[j.ID] && !first && !r.Busy(j.ID) {
				r.store.AppendLog(j.ID, "Dossiers de nouveau accessibles : synchronisation automatique")
				r.StartAuto(j.ID)
			}
			present[j.ID] = ok
		}
		first = false
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (r *Runner) tick() {
	defer func() {
		if p := recover(); p != nil {
			log.Printf("PLANTAGE du planificateur : %v\n%s", p, debug.Stack())
		}
	}()
	for _, j := range r.store.List() {
		if j.AutoMinutes > 0 && !r.Busy(j.ID) && time.Since(j.LastRun) >= time.Duration(j.AutoMinutes)*time.Minute {
			r.StartAuto(j.ID)
		}
	}
}
