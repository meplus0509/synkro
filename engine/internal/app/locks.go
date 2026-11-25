package app

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ---------- Verrouillage des dossiers ----------
//
// Deux jobs qui touchent au même dossier (le même, un parent ou un sous-dossier)
// ne tournent jamais en même temps : le second attend que le premier ait fini.

// place identifie un emplacement : "local" ou "sftp://hôte:port", plus un chemin.
type place struct {
	scope string
	path  string
}

func placeOf(e Endpoint) place {
	switch e.Type {
	case "usb":
		id := "?"
		if e.Volume != nil {
			id = strings.ToLower(e.Volume.Serial + "|" + e.Volume.Label)
		}
		return place{"usb:" + id, usbSub(e.Path)}
	case "sftp":
		if e.SFTP == nil {
			return place{"sftp://?", ""}
		}
		port := e.SFTP.Port
		if port == 0 {
			port = 22
		}
		p := path.Clean("/" + strings.TrimPrefix(e.SFTP.Path, "/"))
		return place{fmt.Sprintf("sftp://%s:%d", strings.ToLower(e.SFTP.Host), port), p}
	default:
		p, err := filepath.Abs(e.Path)
		if err != nil {
			p = e.Path
		}
		p = filepath.ToSlash(filepath.Clean(p))
		if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
			p = strings.ToLower(p) // systèmes de fichiers insensibles à la casse
		}
		return place{"local", p}
	}
}

// overlaps : même dossier, ou l'un contient l'autre.
func overlaps(a, b place) bool {
	if a.scope != b.scope {
		return false
	}
	pa, pb := strings.TrimSuffix(a.path, "/"), strings.TrimSuffix(b.path, "/")
	return pa == pb || strings.HasPrefix(pa+"/", pb+"/") || strings.HasPrefix(pb+"/", pa+"/")
}

func placesOf(j Job) []place { return []place{placeOf(j.Left), placeOf(j.Right)} }

type activeJob struct {
	name   string
	places []place
}

// blocker renvoie le nom d'un job actif qui partage un dossier avec j ("" si aucun).
// À appeler avec r.mu verrouillé.
func (r *Runner) blockerLocked(j Job) string {
	mine := placesOf(j)
	for id, a := range r.active {
		if id == j.ID {
			continue
		}
		for _, p := range mine {
			for _, q := range a.places {
				if overlaps(p, q) {
					return a.name
				}
			}
		}
	}
	return ""
}

var errCanceledWhileWaiting = errors.New("annulé avant le démarrage")

// acquire attend que les dossiers du job soient libres, puis les réserve.
func (r *Runner) acquire(ctx context.Context, j Job) error {
	for {
		r.mu.Lock()
		who := r.blockerLocked(j)
		if who == "" {
			r.active[j.ID] = activeJob{name: j.Name, places: placesOf(j)}
			r.mu.Unlock()
			return nil
		}
		if jr := r.runs[j.ID]; jr != nil {
			jr.status.State = "waiting"
			jr.status.Message = fmt.Sprintf("En attente : un dossier est utilisé par « %s » en cours. Démarrage automatique dès qu'il aura fini.", who)
		}
		r.mu.Unlock()
		select {
		case <-ctx.Done():
			return errCanceledWhileWaiting
		case <-time.After(time.Second):
		}
	}
}

func (r *Runner) release(id string) {
	r.mu.Lock()
	delete(r.active, id)
	r.mu.Unlock()
}

// ---------- File d'attente ----------

// Queue synchronise plusieurs jobs l'un après l'autre, dans l'ordre donné.
// Un job qui s'arrête sur des conflits à résoudre ne bloque pas la file :
// il attend la décision de l'utilisateur pendant que les suivants passent.
func (r *Runner) Queue(ids []string) error {
	var jobs []Job
	for _, id := range ids {
		j, err := r.store.Get(id)
		if err != nil {
			return err
		}
		if r.Busy(j.ID) {
			return fmt.Errorf("« %s » est déjà en cours ou en file d'attente", j.Name)
		}
		jobs = append(jobs, j)
	}
	for i, j := range jobs {
		pos := i + 1
		r.set(j.ID, func(jr *jobRun) {
			*jr = jobRun{status: Status{State: "queued", StartedAt: time.Now(),
				Message: fmt.Sprintf("En file d'attente (%d sur %d)", pos, len(jobs))}}
		})
	}
	go func() {
		for _, j := range jobs {
			if r.Status(j.ID).State != "queued" {
				continue // retiré de la file entre-temps
			}
			r.setOverrides(j.ID, nil)
			if err := r.start(j.ID, true, true, nil, true); err != nil {
				r.set(j.ID, func(jr *jobRun) { jr.status.State = "error"; jr.status.Message = err.Error() })
				continue
			}
			for r.Busy(j.ID) {
				time.Sleep(300 * time.Millisecond)
			}
		}
	}()
	return nil
}
