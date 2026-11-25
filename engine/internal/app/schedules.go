package app

import (
	"errors"
	"fmt"
	"log"
	"time"
)

// Schedule : lancement planifié d'une tâche ou d'un groupe à une date et une
// heure précises, une seule fois ou de façon répétée.
type Schedule struct {
	ID      string    `json:"id"`
	Kind    string    `json:"kind"`             // "job" ou "group"
	Ref     string    `json:"ref"`              // id de la tâche, ou nom du groupe (« * » = Tout)
	At      time.Time `json:"at"`               // prochain lancement
	Repeat  string    `json:"repeat,omitempty"` // "", "daily", "weekdays", "weekly", "monthly"
	Enabled bool      `json:"enabled"`
	LastRun time.Time `json:"lastRun,omitempty"`
	Note    string    `json:"note,omitempty"` // dernier événement (« lancé », « manqué »…)
}

// GroupAll : référence du groupe « Tout » dans une planification.
const GroupAll = "*"

// ScheduleLate : un lancement prévu pendant que Synkro était fermé est encore
// fait au démarrage s'il a moins de ce retard ; au-delà il est compté manqué.
const ScheduleLate = 12 * time.Hour

// next renvoie la prochaine date après t selon la répétition (heure locale
// conservée, même au changement d'heure).
func nextAt(t time.Time, repeat string) time.Time {
	t = t.Local()
	switch repeat {
	case "daily":
		return t.AddDate(0, 0, 1)
	case "weekdays":
		n := t.AddDate(0, 0, 1)
		for n.Weekday() == time.Saturday || n.Weekday() == time.Sunday {
			n = n.AddDate(0, 0, 1)
		}
		return n
	case "weekly":
		return t.AddDate(0, 0, 7)
	case "monthly":
		return t.AddDate(0, 1, 0)
	}
	return t
}

// Schedules renvoie toutes les planifications, triées par prochain lancement.
func (s *Store) Schedules() []Schedule {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]Schedule{}, s.plans...)
	return out
}

func (s *Store) dropPlans(match func(Schedule) bool) {
	out := s.plans[:0]
	for _, p := range s.plans {
		if !match(p) {
			out = append(out, p)
		}
	}
	s.plans = out
}

// AddSchedule ajoute une planification.
func (s *Store) AddSchedule(p Schedule) (Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch p.Kind {
	case "job":
		ok := false
		for _, j := range s.jobs {
			ok = ok || j.ID == p.Ref
		}
		if !ok {
			return p, errors.New("tâche introuvable")
		}
	case "group":
		if p.Ref != GroupAll && !hasGroup(s.groups, p.Ref) {
			return p, fmt.Errorf("groupe « %s » introuvable", p.Ref)
		}
	default:
		return p, errors.New("planification : tâche ou groupe attendu")
	}
	switch p.Repeat {
	case "", "daily", "weekdays", "weekly", "monthly":
	default:
		return p, errors.New("répétition inconnue")
	}
	if p.At.IsZero() {
		return p, errors.New("indiquez la date et l'heure")
	}
	if p.Repeat == "" && p.At.Before(time.Now().Add(-time.Minute)) {
		return p, errors.New("cette date est déjà passée")
	}
	for p.Repeat != "" && p.At.Before(time.Now()) { // répétée : premier lancement à venir
		p.At = nextAt(p.At, p.Repeat)
	}
	p.ID, p.Enabled, p.Note = newID(), true, ""
	s.plans = append(s.plans, p)
	return p, s.saveLocked()
}

// EditSchedule active / désactive, ou supprime (del) une planification.
func (s *Store) EditSchedule(id string, enabled *bool, del bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.plans {
		if s.plans[i].ID != id {
			continue
		}
		if del {
			s.plans = append(s.plans[:i], s.plans[i+1:]...)
		} else if enabled != nil {
			p := &s.plans[i]
			p.Enabled = *enabled
			// réactivée : si la date est passée, prochaine occurrence
			for p.Enabled && p.Repeat != "" && p.At.Before(time.Now()) {
				p.At = nextAt(p.At, p.Repeat)
			}
		}
		return s.saveLocked()
	}
	return errors.New("planification introuvable")
}

// duePlans renvoie les planifications à lancer maintenant et met à jour leur
// prochaine date (ou les désactive si elles ne se répètent pas).
func (s *Store) duePlans(now time.Time) []Schedule {
	s.mu.Lock()
	defer s.mu.Unlock()
	var due []Schedule
	changed := false
	for i := range s.plans {
		p := &s.plans[i]
		if !p.Enabled || p.At.After(now) {
			continue
		}
		changed = true
		if now.Sub(p.At) <= ScheduleLate {
			due = append(due, *p)
			p.LastRun, p.Note = now, "lancé"
		} else {
			p.Note = "manqué le " + p.At.Local().Format("02/01/2006 15:04") + " (Synkro était fermé)"
			log.Printf("planification %s manquée (%s)", p.ID, p.At.Local().Format("02/01/2006 15:04"))
		}
		if p.Repeat == "" {
			p.Enabled = false
		} else {
			for !p.At.After(now) {
				p.At = nextAt(p.At, p.Repeat)
			}
		}
	}
	if changed {
		s.saveLocked()
	}
	return due
}

// runSchedules lance ce qui est prévu maintenant (appelé par le planificateur).
func (r *Runner) runSchedules(now time.Time) {
	for _, p := range r.store.duePlans(now) {
		var ids []string
		var name string
		switch p.Kind {
		case "job":
			if j, err := r.store.Get(p.Ref); err == nil {
				ids, name = []string{j.ID}, "tâche « "+j.Name+" »"
			}
		case "group":
			name = "groupe « " + p.Ref + " »"
			if p.Ref == GroupAll {
				name = "toutes les tâches"
			}
			for _, j := range r.store.List() {
				if p.Ref == GroupAll || hasGroup(j.Groups, p.Ref) {
					ids = append(ids, j.ID)
				}
			}
		}
		var free []string
		for _, id := range ids {
			if !r.Busy(id) {
				free = append(free, id)
			}
		}
		if len(free) == 0 {
			log.Printf("planification : %s — rien à lancer (déjà en cours ou vide)", name)
			continue
		}
		log.Printf("planification : lancement de %s (%d tâche(s))", name, len(free))
		if err := r.Queue(free); err != nil {
			log.Printf("planification : %s — %v", name, err)
		}
	}
}
