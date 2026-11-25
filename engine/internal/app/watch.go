package app

import (
	"context"
	"log"
	"runtime/debug"
	"strings"
	"time"
)

// watchChanges : option « sur changement de fichier ». Les côtés locaux des
// tâches concernées sont surveillés ; après ChangeDelay secondes sans nouveau
// changement, la tâche est analysée et synchronisée.
func (r *Runner) watchChanges(ctx context.Context) {
	defer func() {
		if p := recover(); p != nil {
			log.Printf("PLANTAGE de la surveillance des changements : %v\n%s", p, debug.Stack())
		}
	}()
	type watch struct{ stop context.CancelFunc }
	active := map[string]watch{}      // tâche|dossier → surveillance
	events := make(chan string, 256)  // tâche
	pending := map[string]time.Time{} // tâche → dernier changement vu
	lastBusy := map[string]time.Time{}
	t := time.NewTicker(time.Second)
	defer t.Stop()
	defer func() {
		for _, w := range active {
			w.stop()
		}
	}()
	for n := 0; ; n++ {
		if n%5 == 0 { // mise à jour de la liste des dossiers surveillés
			want := map[string]bool{}
			for _, j := range r.store.List() {
				if !j.OnChange {
					continue
				}
				for _, e := range []Endpoint{j.Left, j.Right} {
					dir := ""
					switch e.Type {
					case "local":
						dir = e.Path
					case "usb":
						dir, _ = e.Resolve()
					}
					if dir == "" {
						continue
					}
					k := j.ID + "|" + dir
					want[k] = true
					if _, ok := active[k]; ok {
						continue
					}
					wctx, stop := context.WithCancel(ctx)
					id := j.ID
					if err := watchDir(wctx, dir, func(name string) {
						if strings.Contains(name, ".synkro") { // corbeille, fichiers temporaires
							return
						}
						select {
						case events <- id:
						default:
						}
					}); err != nil {
						stop()
						log.Printf("surveillance de %s : %v", dir, err)
						continue
					}
					active[k] = watch{stop}
				}
			}
			for k, w := range active {
				if !want[k] {
					w.stop()
					delete(active, k)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case id := <-events:
			// changements faits par Synkro lui-même pendant (ou juste après) un passage : ignorés
			if r.Busy(id) || time.Since(lastBusy[id]) < 5*time.Second {
				continue
			}
			pending[id] = time.Now()
			n-- // pas un tic de l'horloge
		case <-t.C:
			for id := range pending {
				if r.Busy(id) {
					lastBusy[id] = time.Now()
				}
			}
			for _, j := range r.store.List() {
				if r.Busy(j.ID) {
					lastBusy[j.ID] = time.Now()
				}
			}
			for id, at := range pending {
				j, err := r.store.Get(id)
				if err != nil || !j.OnChange {
					delete(pending, id)
					continue
				}
				if time.Since(at) >= time.Duration(j.ChangeDelay)*time.Second && !r.Busy(id) {
					delete(pending, id)
					r.store.AppendLog(id, "Fichiers modifiés : synchronisation automatique")
					r.StartAuto(id)
				}
			}
		}
	}
}
