package vfs

import (
	"errors"
	"sync"
)

// ShadowSet : instantanés Volume Shadow Copy (Windows) créés pendant un passage,
// un par disque, supprimés à la fin (Close).
type ShadowSet struct {
	Log  func(string) // facultatif : journal de la tâche
	mu   sync.Mutex
	vols map[string]*shadow
	done bool
}

type shadow struct {
	id, device string
	err        error
}

// ErrShadowUnsupported : instantanés indisponibles sur ce système.
var ErrShadowUnsupported = errors.New("instantanés (Volume Shadow Copy) disponibles seulement sous Windows")

func NewShadowSet(log func(string)) *ShadowSet {
	return &ShadowSet{Log: log, vols: map[string]*shadow{}}
}

func (s *ShadowSet) log(m string) {
	if s.Log != nil {
		s.Log(m)
	}
}

// Path renvoie le chemin de abs dans l'instantané de son disque (créé au besoin).
func (s *ShadowSet) Path(abs string) (string, error) {
	vol, rest, err := splitVolume(abs)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return "", errors.New("instantané déjà supprimé")
	}
	sh := s.vols[vol]
	if sh == nil {
		sh = &shadow{}
		sh.id, sh.device, sh.err = createShadow(vol)
		if sh.err == nil {
			s.log("Instantané du disque " + vol + " créé (Volume Shadow Copy)")
		} else {
			s.log("Instantané du disque " + vol + " impossible : " + sh.err.Error())
		}
		s.vols[vol] = sh
	}
	if sh.err != nil {
		return "", sh.err
	}
	return sh.device + rest, nil
}

// Close supprime les instantanés créés.
func (s *ShadowSet) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return
	}
	s.done = true
	for vol, sh := range s.vols {
		if sh.err == nil && sh.id != "" {
			if err := deleteShadow(sh.id); err != nil {
				s.log("Suppression de l'instantané du disque " + vol + " : " + err.Error())
			}
		}
	}
}
