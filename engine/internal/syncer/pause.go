package syncer

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// Gate permet de mettre une synchronisation en pause puis de la reprendre :
// les transferts en cours s'arrêtent entre deux blocs, aucun nouveau ne démarre.
type Gate struct {
	mu     sync.Mutex
	ch     chan struct{} // non nil = en pause ; fermé à la reprise
	since  time.Time     // début de la pause en cours
	paused time.Duration // total des pauses terminées
	// vitesse changée en cours de synchro (remplace celle de la tâche)
	speedSet atomic.Bool
	kbps     atomic.Int64 // Ko/s, 0 = illimitée
}

// SetMaxKBps change la limite de vitesse de la synchro en cours (0 = illimitée) :
// les copies la prennent en compte au bloc suivant, sans être interrompues.
func (g *Gate) SetMaxKBps(k int) {
	if g == nil {
		return
	}
	g.kbps.Store(int64(max(k, 0)))
	g.speedSet.Store(true)
}

// MaxKBps : limite choisie en cours de synchro, si elle a été changée.
func (g *Gate) MaxKBps() (int, bool) {
	if g == nil || !g.speedSet.Load() {
		return 0, false
	}
	return int(g.kbps.Load()), true
}

// Pause met en pause (sans effet si c'est déjà le cas).
func (g *Gate) Pause() {
	g.mu.Lock()
	if g.ch == nil {
		g.ch = make(chan struct{})
		g.since = time.Now()
	}
	g.mu.Unlock()
}

// Resume reprend (sans effet si ce n'était pas en pause).
func (g *Gate) Resume() {
	g.mu.Lock()
	if g.ch != nil {
		close(g.ch)
		g.ch = nil
		g.paused += time.Since(g.since)
	}
	g.mu.Unlock()
}

// Paused indique si la pause est active.
func (g *Gate) Paused() bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.ch != nil
}

// PausedFor renvoie la durée totale passée en pause (pause en cours comprise).
func (g *Gate) PausedFor() time.Duration {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	d := g.paused
	if g.ch != nil {
		d += time.Since(g.since)
	}
	return d
}

// Wait bloque tant que la pause est active (ou jusqu'à l'annulation).
func (g *Gate) Wait(ctx context.Context) error {
	if g == nil {
		return ctx.Err()
	}
	for {
		g.mu.Lock()
		c := g.ch
		g.mu.Unlock()
		if c == nil {
			return ctx.Err()
		}
		select {
		case <-c:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

type gateKey struct{}

// WithGate associe une pause au contexte passé à Execute.
func WithGate(ctx context.Context, g *Gate) context.Context {
	return context.WithValue(ctx, gateKey{}, g)
}

func gateOf(ctx context.Context) *Gate {
	g, _ := ctx.Value(gateKey{}).(*Gate)
	return g
}
