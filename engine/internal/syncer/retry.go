package syncer

import (
	"context"
	"fmt"
	"sync"
	"time"

	"synkro/internal/vfs"
)

// ---------- Limite de vitesse ----------

// limitChunk : taille des morceaux écrits quand la vitesse est limitée.
const limitChunk = 32 << 10

// limiter répartit un débit (octets/s) entre toutes les copies en cours.
type limiter struct {
	mu     sync.Mutex
	rate   float64 // octets par seconde
	tokens float64
	last   time.Time
}

func newLimiter(bytesPerSec float64) *limiter {
	return &limiter{rate: bytesPerSec, last: time.Now()}
}

// setRate change le débit (0 = illimité) ; repart sans avance ni retard.
func (l *limiter) setRate(r float64) {
	l.mu.Lock()
	if r != l.rate {
		l.rate, l.tokens, l.last = r, 0, time.Now()
	}
	l.mu.Unlock()
}

// on : un débit est imposé.
func (l *limiter) on() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rate > 0
}

// wait réserve n octets et attend le temps nécessaire pour respecter le débit.
func (l *limiter) wait(ctx context.Context, n int) error {
	l.mu.Lock()
	if l.rate <= 0 { // illimité
		l.mu.Unlock()
		return nil
	}
	now := time.Now()
	l.tokens += now.Sub(l.last).Seconds() * l.rate
	if l.tokens > l.rate { // au plus une seconde d'avance
		l.tokens = l.rate
	}
	l.last = now
	l.tokens -= float64(n)
	var d time.Duration
	if l.tokens < 0 {
		d = time.Duration(-l.tokens / l.rate * float64(time.Second))
	}
	l.mu.Unlock()
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ---------- Reconnexion ----------

// retryPause : délai entre deux tentatives de reconnexion.
var retryPause = 3 * time.Second

// withRetry exécute fn ; si elle échoue parce que la connexion est perdue (serveur
// injoignable, clé USB débranchée…), on tente de la rétablir pendant
// opt.RetryWindow() puis on recommence l'action. Une erreur « normale » (droits,
// fichier verrouillé…) n'est pas retentée.
func (x *executor) withRetry(a Action, fn func() error) error {
	window := x.opt.RetryWindow()
	var deadline time.Time
	for {
		err := fn()
		if err == nil || x.ctx.Err() != nil || window <= 0 {
			return err
		}
		down := x.downSides()
		if len(down) == 0 {
			return err // la connexion va bien : vraie erreur sur ce fichier
		}
		if deadline.IsZero() {
			deadline = time.Now().Add(window)
		}
		if !x.reconnect(down, deadline) {
			if x.ctx.Err() != nil {
				return err
			}
			return fmt.Errorf("%w (connexion perdue, non rétablie après %v)", err, window)
		}
		// connexion rétablie : on recommence l'action
	}
}

// alive vérifie qu'un côté répond encore (lecture de sa racine).
func alive(fs vfs.FS) bool {
	_, err := fs.Stat("")
	return err == nil
}

func (x *executor) downSides() []vfs.FS {
	var down []vfs.FS
	for _, fs := range []vfs.FS{x.l, x.r} {
		if !alive(fs) {
			down = append(down, fs)
		}
	}
	return down
}

// reconnect essaie de rétablir les côtés perdus jusqu'à l'échéance.
// Une seule goroutine se reconnecte à la fois ; les autres attendent son résultat.
func (x *executor) reconnect(down []vfs.FS, deadline time.Time) bool {
	x.reconnMu.Lock()
	defer x.reconnMu.Unlock()
	for {
		ok := true
		for _, fs := range down {
			if alive(fs) { // déjà rétabli par une autre copie
				continue
			}
			if rc, can := fs.(vfs.Reconnecter); can {
				if err := rc.Reconnect(); err != nil {
					ok = false
				}
			} else {
				ok = false // dossier local : on attend qu'il revienne
			}
		}
		if ok && len(x.downSides()) == 0 {
			x.note("") // rétablie : on efface le message
			return true
		}
		if time.Now().After(deadline) || x.ctx.Err() != nil {
			return false
		}
		x.note(fmt.Sprintf("connexion perdue, nouvel essai… (encore %ds)", int(time.Until(deadline).Seconds())))
		t := time.NewTimer(retryPause)
		select {
		case <-t.C:
		case <-x.ctx.Done():
			t.Stop()
			return false
		}
	}
}

// note affiche un message d'état dans la progression.
// Logf (facultatif) reçoit les événements importants (reconnexions) pour le journal du moteur.
var Logf func(format string, args ...any)

func (x *executor) note(msg string) {
	if Logf != nil && msg != "" {
		Logf("synchro : %s", msg)
	}
	x.mu.Lock()
	x.prog.Note = msg
	x.emit(true)
	x.mu.Unlock()
}
