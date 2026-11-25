// Package syncer contient l'algorithme de synchronisation : comparaison des
// deux côtés avec l'état du dernier passage, construction d'un plan d'actions
// puis exécution.
package syncer

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"synkro/internal/vfs"
)

// Mode de synchronisation.
type Mode string

const (
	TwoWay Mode = "two-way" // bidirectionnelle : les modifications vont dans les deux sens
	Mirror Mode = "mirror"  // gauche → droite, la droite devient une copie exacte (suppressions comprises)
	Backup Mode = "backup"  // gauche → droite, sans jamais supprimer à droite
)

// Options d'un job.
type Options struct {
	Mode    Mode     `json:"mode"`
	Exclude []string `json:"exclude"`
	// Include : si renseigné, seuls ces éléments (et leur contenu pour un dossier)
	// sont synchronisés ; mêmes règles d'écriture que Exclude.
	Include []string `json:"include,omitempty"`
	// Exclusions automatiques
	ExcludeHidden    bool `json:"excludeHidden,omitempty"`    // fichiers et dossiers cachés
	ExcludeSystem    bool `json:"excludeSystem,omitempty"`    // fichiers et dossiers système
	ExcludeTemp      bool `json:"excludeTemp,omitempty"`      // fichiers temporaires (TempPatterns)
	ExcludeEmptyDirs bool `json:"excludeEmptyDirs,omitempty"` // dossiers sans aucun fichier
	// UseTrash : les fichiers supprimés ou écrasés sont déplacés dans
	// .synkro-trash/<date>/ du côté concerné au lieu d'être effacés.
	UseTrash bool `json:"useTrash"`
	// Own : dossiers de données de Synkro situés dans le job (motifs ancrés
	// « /chemin »), toujours exclus. Calculé à chaque passage, jamais enregistré.
	Own []string `json:"-"`
	// AllowEmptySide désactive le garde-fou « un côté est vide » (clé USB débranchée…).
	AllowEmptySide bool `json:"allowEmptySide"`
	// ConflictDefault : version gardée par défaut quand un fichier a changé des deux
	// côtés (mode bidirectionnel) : "newer" (la plus récente), "left" ou "right".
	ConflictDefault string `json:"conflictDefault"`
	// ConflictAsk : avant une synchro lancée à la main, montrer les conflits et
	// laisser choisir fichier par fichier. Une synchro automatique, elle, laisse
	// les fichiers en conflit tels quels (ils seront proposés au prochain passage).
	ConflictAsk bool `json:"conflictAsk"`
	// Transferts (SFTP ou local)
	// MaxKBps : limite de vitesse en Ko/s pour l'ensemble des copies (0 = illimitée).
	MaxKBps int `json:"maxKBps,omitempty"`
	// Parallel : nombre de fichiers copiés en même temps (0 = automatique).
	Parallel int `json:"parallel,omitempty"`
	// RetrySeconds : quand la connexion (serveur, clé USB, disque réseau) est
	// perdue, durée pendant laquelle on essaie de la rétablir avant de marquer le
	// fichier en échec. 0 = valeur par défaut (60 s), négatif = aucune nouvelle tentative.
	RetrySeconds int `json:"retrySeconds,omitempty"`
	// CopyOrder : ordre de copie des fichiers. "" ou "size-asc" = du plus petit
	// au plus gros (défaut), "size-desc" = du plus gros au plus petit,
	// "path" = ordre d'apparition dans la liste (alphabétique, dossier par dossier).
	CopyOrder string `json:"copyOrder,omitempty"`
	// Order : ordre de copie imposé (chemins, dans l'ordre de la liste triée par
	// l'utilisateur) ; prioritaire sur CopyOrder. Jamais enregistré.
	Order []string `json:"-"`
	// MaxChanges : synchronisation automatique abandonnée au-delà de ce nombre de
	// modifications (copies et suppressions). 0 = pas de limite.
	MaxChanges int `json:"maxChanges,omitempty"`
	// CopyCreated / CopyAttrs : recopier la date de création et les attributs
	// (lecture seule, caché, système, archive) — Windows, disques locaux.
	CopyCreated bool `json:"copyCreated,omitempty"`
	CopyAttrs   bool `json:"copyAttrs,omitempty"`
	// Tâches à sens unique, fichiers identiques mais dates un peu différentes
	// (tolérance de 2 s) : la date de la source est recopiée à droite, sauf si
	// KeepDates (option « Corriger la date… » décochée).
	KeepDates bool `json:"keepDates,omitempty"`
	// OnFixDates : avancement de la correction des dates pendant une synchro
	OnFixDates func(done, total int) `json:"-"`
	// CoarseDates : le disque de droite n'enregistre les dates qu'à 2 s près
	CoarseDates bool `json:"-"`
}

// DefaultRetrySeconds est la durée de reconnexion quand RetrySeconds vaut 0.
const DefaultRetrySeconds = 60

// RetryWindow renvoie la durée pendant laquelle on tente de rétablir la connexion.
func (o Options) RetryWindow() time.Duration {
	switch {
	case o.RetrySeconds < 0:
		return 0
	case o.RetrySeconds == 0:
		return DefaultRetrySeconds * time.Second
	}
	return time.Duration(o.RetrySeconds) * time.Second
}

// Kind est le type d'une action.
type Kind string

const (
	CopyLR Kind = "copy→"   // copier gauche vers droite
	CopyRL Kind = "copy←"   // copier droite vers gauche
	DelL   Kind = "delete←" // supprimer à gauche
	DelR   Kind = "delete→" // supprimer à droite
	MkdirL Kind = "mkdir←"
	MkdirR Kind = "mkdir→"
	Skip   Kind = "skip"  // conflit non résolvable (fichier d'un côté, dossier de l'autre)
	Leave  Kind = "leave" // conflit laissé tel quel (choix « ne rien faire »)
)

// Action est une opération du plan.
type Action struct {
	Kind     Kind   `json:"kind"`
	Path     string `json:"path"`
	IsDir    bool   `json:"isDir,omitempty"`
	Size     int64  `json:"size"`
	Conflict bool   `json:"conflict,omitempty"` // modifié des deux côtés (ou modifié d'un côté, supprimé de l'autre)
	Reason   string `json:"reason"`
	// Pour un conflit : l'état du fichier de chaque côté, affiché pour choisir.
	Left  *Side `json:"left,omitempty"`
	Right *Side `json:"right,omitempty"`
}

// Side décrit la version d'un fichier d'un côté (Exists=false : supprimé de ce côté).
type Side struct {
	Exists  bool      `json:"exists,omitempty"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modTime,omitzero"`
}

func sideOf(e vfs.Entry, ok bool) *Side {
	if !ok {
		return &Side{}
	}
	return &Side{Exists: true, Size: e.Size, ModTime: e.ModTime}
}

// FileState est l'état mémorisé d'un chemin après une synchro réussie.
type FileState struct {
	IsDir bool  `json:"d,omitempty"`
	Size  int64 `json:"s"`
	MTime int64 `json:"m"` // secondes Unix
}

// State est l'état complet d'un job (chemin → état).
type State map[string]FileState

// Plan est le résultat de l'analyse.
type Plan struct {
	Actions    []Action             `json:"actions"`
	Left       map[string]vfs.Entry `json:"-"`
	include    []string             // motifs « inclure seulement » du passage
	Right      map[string]vfs.Entry `json:"-"`
	TotalBytes int64                `json:"totalBytes"`
	Conflicts  int                  `json:"conflicts"`
	Unchanged  int                  `json:"unchanged"`
	// Same liste les éléments identiques des deux côtés (rien à faire), pour
	// pouvoir les afficher à la demande dans l'interface.
	Same []Action `json:"-"`
	// dates de fichiers identiques corrigées pendant la synchro (option FixDates)
	DatesFixed, DatesFailed int      `json:"-"`
	DatesFailedWhy          []string `json:"-"` // premières dates impossibles : « chemin : raison »
	DatesTodo               int      `json:"-"` // dates à corriger au départ
	// DatesImprecise : dates que le disque de destination ne peut pas garder à la
	// seconde près (FAT, exFAT : 2 s)
	DatesImprecise int `json:"-"`
	// CoarseFound : la correction a montré que la droite arrondit les dates à 2 s
	CoarseFound bool `json:"-"`
}

// Tolérance sur les dates : FAT/exFAT (clés USB) arrondit à 2 s, SFTP à la seconde.
const timeTolerance = 2 * time.Second

const reservedPrefix = ".synkro-"

// TrashDir est le dossier de corbeille créé de chaque côté.
const TrashDir = ".synkro-trash"

func sameTime(a, b time.Time) bool {
	d := a.Sub(b)
	if d < 0 {
		d = -d
	}
	return d <= timeTolerance
}

func sameEntry(a, b vfs.Entry) bool {
	if a.IsDir || b.IsDir {
		return a.IsDir == b.IsDir
	}
	return a.Size == b.Size && sameTime(a.ModTime, b.ModTime)
}

func changedSince(e vfs.Entry, s FileState) bool {
	if e.IsDir || s.IsDir {
		return e.IsDir != s.IsDir
	}
	return e.Size != s.Size || !sameTime(e.ModTime, time.Unix(s.MTime, 0))
}

// systemNames : dossiers système présents à la racine des clés et disques. On ne les
// synchronise jamais : Windows en interdit l'accès, et les copier ou les supprimer
// toucherait à la corbeille ou aux données internes du système.
var systemNames = map[string]bool{
	"$recycle.bin":              true, // corbeille Windows
	"system volume information": true, // points de restauration, index Windows
	"recycler":                  true, // corbeille des anciens Windows
	".trashes":                  true, // corbeille macOS sur les clés
	".spotlight-v100":           true, // index macOS
	".fseventsd":                true, // journal macOS
	".temporaryitems":           true,
}

// Excluded indique si un chemin correspond à un motif d'exclusion.
// Motif sans "/" : comparé au nom de chaque élément (ex: "*.tmp", "node_modules").
// Motif avec "/" : comparé au chemin complet depuis la racine (ex: "/build", "docs/*.pdf").
func Excluded(p string, patterns []string) bool {
	parts := strings.Split(p, "/")
	for _, part := range parts {
		if strings.HasPrefix(part, reservedPrefix) || systemNames[strings.ToLower(part)] {
			return true
		}
	}
	return matchAny(parts, patterns)
}

// matchAny : le chemin (découpé) ou un de ses parents correspond à un des motifs.
func matchAny(parts []string, patterns []string) bool {
	for _, pat := range patterns {
		pat = strings.TrimSpace(pat)
		if pat == "" || strings.HasPrefix(pat, "#") {
			continue
		}
		if strings.Contains(pat, "/") {
			pat = strings.Trim(pat, "/")
			// le motif s'applique au chemin ou à n'importe lequel de ses parents
			for i := len(parts); i > 0; i-- {
				if ok, _ := path.Match(pat, strings.Join(parts[:i], "/")); ok {
					return true
				}
			}
			continue
		}
		for _, part := range parts {
			if ok, _ := path.Match(pat, part); ok {
				return true
			}
		}
	}
	return false
}

// Included indique si un chemin est retenu par les motifs « inclure seulement »
// (mêmes règles d'écriture que les exclusions ; un dossier inclus l'est avec tout son contenu).
func Included(p string, patterns []string) bool {
	return matchAny(strings.Split(p, "/"), patterns)
}

// hasPatterns : au moins un motif utile.
func hasPatterns(patterns []string) bool {
	for _, p := range patterns {
		if p = strings.TrimSpace(p); p != "" && !strings.HasPrefix(p, "#") {
			return true
		}
	}
	return false
}

type droppedItem struct {
	path   string
	reason string
}

// droppedFrom liste les éléments retirés (seulement le plus haut d'une branche retirée).
func droppedFrom(beforeL, beforeR, afterL, afterR map[string]vfs.Entry, reason string) []droppedItem {
	gone := map[string]bool{}
	for _, pair := range [][2]map[string]vfs.Entry{{beforeL, afterL}, {beforeR, afterR}} {
		for p := range pair[0] {
			if _, ok := pair[1][p]; !ok {
				gone[p] = true
			}
		}
	}
	var out []droppedItem
	for p := range gone {
		if par := vfs.Parent(p); par != "" && gone[par] {
			continue
		}
		out = append(out, droppedItem{p, reason})
	}
	return out
}

// dropEmptyDirs retire les dossiers qui ne contiennent aucun fichier (d'un côté comme de l'autre).
func dropEmptyDirs(left, right map[string]vfs.Entry) (map[string]vfs.Entry, map[string]vfs.Entry) {
	full := map[string]bool{}
	for _, m := range []map[string]vfs.Entry{left, right} {
		for p, e := range m {
			if e.IsDir {
				continue
			}
			for d := vfs.Parent(p); d != ""; d = vfs.Parent(d) {
				full[d] = true
			}
		}
	}
	filter := func(m map[string]vfs.Entry) map[string]vfs.Entry {
		out := make(map[string]vfs.Entry, len(m))
		for p, e := range m {
			if !e.IsDir || full[p] {
				out[p] = e
			}
		}
		return out
	}
	return filter(left), filter(right)
}

// applyInclude ne garde que les éléments inclus et les dossiers qui les contiennent.
func applyInclude(left, right map[string]vfs.Entry, prev State, include []string) (map[string]vfs.Entry, map[string]vfs.Entry, State) {
	keep := map[string]bool{}
	mark := func(m map[string]vfs.Entry) {
		for p := range m {
			if !Included(p, include) {
				continue
			}
			keep[p] = true
			for d := vfs.Parent(p); d != ""; d = vfs.Parent(d) {
				keep[d] = true // dossiers parents nécessaires
			}
		}
	}
	mark(left)
	mark(right)
	filter := func(m map[string]vfs.Entry) map[string]vfs.Entry {
		out := make(map[string]vfs.Entry, len(keep))
		for p, e := range m {
			if keep[p] {
				out[p] = e
			}
		}
		return out
	}
	ns := State{}
	for p, st := range prev {
		if Included(p, include) || keep[p] {
			ns[p] = st
		}
	}
	return filter(left), filter(right), ns
}

// TempPatterns : fichiers temporaires exclus par l'option ExcludeTemp.
var TempPatterns = []string{"*.tmp", "*.temp", "~$*", ".~lock.*", "*.swp", "*.crdownload", "*.part", "*.partial"}

// Skipped : élément ignoré à la lecture, avec la raison (affiché à la demande).
type Skipped struct {
	Entry  vfs.Entry
	Reason string
}

// excludeReason renvoie pourquoi un élément est exclu ("" s'il ne l'est pas).
func excludeReason(e vfs.Entry, opt Options) string {
	parts := strings.Split(e.Path, "/")
	for _, part := range parts {
		if strings.HasPrefix(part, reservedPrefix) {
			return "dossier interne de Synkro"
		}
		if systemNames[strings.ToLower(part)] {
			return "dossier système"
		}
	}
	switch {
	case matchAny(parts, opt.Own):
		return "données de Synkro"
	case matchAny(parts, opt.Exclude):
		return "filtre"
	case opt.ExcludeTemp && matchAny(parts, TempPatterns):
		return "temporaire"
	case opt.ExcludeHidden && e.Hidden:
		return "caché"
	case opt.ExcludeSystem && e.System:
		return "système"
	}
	return ""
}

// ScanCount : avancement de la lecture d'un côté.
type ScanCount struct {
	Dirs  int    `json:"dirs"`
	Files int    `json:"files"`
	Path  string `json:"path"` // dernier élément lu
}

// ScanWith liste un côté en appliquant toutes les exclusions des options du job.
// skipped (facultatif) reçoit les éléments exclus et la raison.
func ScanWith(ctx context.Context, fs vfs.FS, opt Options, onCount func(ScanCount), skipped map[string]Skipped) (map[string]vfs.Entry, error) {
	out := map[string]vfs.Entry{}
	var c ScanCount
	err := fs.Walk(func(e vfs.Entry) (bool, error) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if why := excludeReason(e, opt); why != "" {
			if skipped != nil {
				skipped[e.Path] = Skipped{e, why}
			}
			return true, nil
		}
		out[e.Path] = e
		if e.IsDir {
			c.Dirs++
		} else {
			c.Files++
		}
		if onCount != nil && len(out)%200 == 0 {
			c.Path = e.Path
			onCount(c)
		}
		return false, nil
	})
	if err == nil {
		err = ctx.Err()
	}
	if onCount != nil && err == nil {
		c.Path = ""
		onCount(c)
	}
	return out, err
}

// AddExcluded ajoute au plan la liste des éléments exclus (des deux côtés),
// montrée avec les inchangés.
func (plan *Plan) AddExcluded(left, right map[string]Skipped) {
	all := map[string]bool{}
	for p := range left {
		all[p] = true
	}
	for p := range right {
		all[p] = true
	}
	for p := range all {
		l, lok := left[p]
		r, rok := right[p]
		why := l.Reason
		if !lok || (rok && r.Reason != why && why == "") {
			why = r.Reason
		} else if rok && r.Reason != why {
			why += ", " + r.Reason
		}
		isDir := (lok && l.Entry.IsDir) || (rok && r.Entry.IsDir)
		plan.Same = append(plan.Same, Action{Kind: ExcludedKind, Path: p, IsDir: isDir, Reason: "exclu : " + why,
			Left: sideOf(l.Entry, lok), Right: sideOf(r.Entry, rok)})
	}
}

// Scan liste un côté en appliquant les exclusions. Il s'arrête dès que ctx est
// annulé ; onCount (facultatif) reçoit régulièrement le nombre d'éléments lus.
func Scan(ctx context.Context, fs vfs.FS, exclude []string, onCount func(n int)) (map[string]vfs.Entry, error) {
	return scan(ctx, fs, exclude, nil, onCount)
}

func scan(ctx context.Context, fs vfs.FS, exclude []string, skip func(vfs.Entry) bool, onCount func(n int)) (map[string]vfs.Entry, error) {
	out := map[string]vfs.Entry{}
	err := fs.Walk(func(e vfs.Entry) (bool, error) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if Excluded(e.Path, exclude) || (skip != nil && skip(e)) {
			return true, nil
		}
		out[e.Path] = e
		if onCount != nil && len(out)%250 == 0 {
			onCount(len(out))
		}
		return false, nil
	})
	if err == nil {
		err = ctx.Err()
	}
	return out, err
}

// BuildPlan compare les deux côtés et l'état précédent.
func BuildPlan(left, right map[string]vfs.Entry, prev State, opt Options) (*Plan, error) {
	oL, oR := left, right
	var dropped []droppedItem
	if hasPatterns(opt.Include) {
		left, right, prev = applyInclude(left, right, prev, opt.Include)
		dropped = append(dropped, droppedFrom(oL, oR, left, right, "hors de « inclure seulement »")...)
	}
	if opt.ExcludeEmptyDirs {
		l2, r2 := dropEmptyDirs(left, right)
		dropped = append(dropped, droppedFrom(left, right, l2, r2, "dossier vide")...)
		left, right = l2, r2
	}
	if !opt.AllowEmptySide && opt.Mode == TwoWay && len(prev) > 0 {
		if len(left) == 0 {
			return nil, fmt.Errorf("garde-fou : le côté gauche est vide alors que %d éléments étaient synchronisés (disque débranché ?). Synchronisation annulée", len(prev))
		}
		if len(right) == 0 {
			return nil, fmt.Errorf("garde-fou : le côté droit est vide alors que %d éléments étaient synchronisés (disque débranché ?). Synchronisation annulée", len(prev))
		}
	}
	if !opt.AllowEmptySide && opt.Mode == Mirror && len(left) == 0 && len(right) > 0 {
		return nil, fmt.Errorf("garde-fou : la source est vide, le miroir effacerait %d éléments à droite. Synchronisation annulée", len(right))
	}

	paths := map[string]struct{}{}
	for p := range left {
		paths[p] = struct{}{}
	}
	for p := range right {
		paths[p] = struct{}{}
	}
	if opt.Mode == TwoWay {
		for p := range prev {
			paths[p] = struct{}{}
		}
	}
	sorted := make([]string, 0, len(paths))
	for p := range paths {
		sorted = append(sorted, p)
	}
	sort.Strings(sorted)

	plan := &Plan{Left: left, Right: right, include: opt.Include}
	for _, d := range dropped {
		l, lok := oL[d.path]
		r, rok := oR[d.path]
		plan.Same = append(plan.Same, Action{Kind: ExcludedKind, Path: d.path, IsDir: (lok && l.IsDir) || (rok && r.IsDir),
			Reason: "exclu : " + d.reason, Left: sideOf(l, lok), Right: sideOf(r, rok)})
	}
	var dirActs []Action
	add := func(a Action) {
		if a.IsDir {
			dirActs = append(dirActs, a)
			return
		}
		if a.Kind == CopyLR || a.Kind == CopyRL {
			plan.TotalBytes += a.Size
		}
		if a.Conflict {
			plan.Conflicts++
		}
		plan.Actions = append(plan.Actions, a)
	}

	for _, p := range sorted {
		l, inL := left[p]
		r, inR := right[p]
		s, inS := prev[p]

		if inL && inR && l.IsDir != r.IsDir {
			add(Action{Kind: Skip, Path: p, Conflict: true, Reason: "fichier d'un côté, dossier de l'autre : à régler à la main"})
			continue
		}

		switch opt.Mode {
		case Mirror, Backup:
			switch {
			case inL && inR:
				if l.IsDir || sameEntry(l, r) {
					plan.Unchanged++
					plan.Same = append(plan.Same, sameAction(p, l))
				} else {
					add(Action{Kind: CopyLR, Path: p, Size: l.Size, Reason: "différent à droite"})
				}
			case inL:
				if l.IsDir {
					add(Action{Kind: MkdirR, Path: p, IsDir: true, Reason: "nouveau dossier"})
				} else {
					add(Action{Kind: CopyLR, Path: p, Size: l.Size, Reason: "absent à droite"})
				}
			case inR:
				if opt.Mode == Mirror {
					add(Action{Kind: DelR, Path: p, IsDir: r.IsDir, Reason: "absent de la source"})
				}
			}

		case TwoWay:
			chL := inL && (!inS || changedSince(l, s))
			chR := inR && (!inS || changedSince(r, s))
			switch {
			case inL && inR:
				if l.IsDir || sameEntry(l, r) {
					plan.Unchanged++
					plan.Same = append(plan.Same, sameAction(p, l))
					continue
				}
				switch {
				case chL && !chR:
					add(Action{Kind: CopyLR, Path: p, Size: l.Size, Reason: "modifié à gauche"})
				case chR && !chL:
					add(Action{Kind: CopyRL, Path: p, Size: r.Size, Reason: "modifié à droite"})
				default: // modifié des deux côtés (ou premier passage avec contenus différents)
					if l.ModTime.After(r.ModTime) || (l.ModTime.Equal(r.ModTime) && l.Size >= r.Size) {
						add(Action{Kind: CopyLR, Path: p, Size: l.Size, Conflict: true, Reason: "conflit : la version de gauche est la plus récente", Left: sideOf(l, true), Right: sideOf(r, true)})
					} else {
						add(Action{Kind: CopyRL, Path: p, Size: r.Size, Conflict: true, Reason: "conflit : la version de droite est la plus récente", Left: sideOf(l, true), Right: sideOf(r, true)})
					}
				}
			case inL && !inR:
				if inS && !chL {
					add(Action{Kind: DelL, Path: p, IsDir: l.IsDir, Reason: "supprimé à droite"})
				} else if l.IsDir {
					add(Action{Kind: MkdirR, Path: p, IsDir: true, Reason: "nouveau dossier à gauche"})
				} else {
					reason := "nouveau à gauche"
					if inS {
						reason = "supprimé à droite mais modifié à gauche : on le conserve"
					}
					a := Action{Kind: CopyLR, Path: p, Size: l.Size, Reason: reason, Conflict: inS}
					if inS {
						a.Left, a.Right = sideOf(l, true), sideOf(r, false)
					}
					add(a)
				}
			case !inL && inR:
				if inS && !chR {
					add(Action{Kind: DelR, Path: p, IsDir: r.IsDir, Reason: "supprimé à gauche"})
				} else if r.IsDir {
					add(Action{Kind: MkdirL, Path: p, IsDir: true, Reason: "nouveau dossier à droite"})
				} else {
					reason := "nouveau à droite"
					if inS {
						reason = "supprimé à gauche mais modifié à droite : on le conserve"
					}
					a := Action{Kind: CopyRL, Path: p, Size: r.Size, Reason: reason, Conflict: inS}
					if inS {
						a.Left, a.Right = sideOf(l, false), sideOf(r, true)
					}
					add(a)
				}
			}
		}
	}

	// Dossiers : une suppression de dossier est annulée si quelque chose
	// à l'intérieur est conservé (copié vers l'autre côté ou laissé en place).
	deleted := map[Kind]map[string]bool{DelL: {}, DelR: {}}
	for _, list := range [][]Action{plan.Actions, dirActs} {
		for _, a := range list {
			if a.Kind == DelL || a.Kind == DelR {
				deleted[a.Kind][a.Path] = true
			}
		}
	}
	keepUnder := func(dir string, side Kind) bool {
		prefix := dir + "/"
		for _, a := range plan.Actions {
			if strings.HasPrefix(a.Path, prefix) {
				if side == DelL && (a.Kind == CopyLR || a.Kind == Skip) {
					return true
				}
				if side == DelR && (a.Kind == CopyRL || a.Kind == Skip) {
					return true
				}
			}
		}
		// fichier toujours présent qui ne sera pas supprimé
		src := left
		del := DelL
		if side == DelR {
			src, del = right, DelR
		}
		for p := range src {
			if strings.HasPrefix(p, prefix) && !deleted[del][p] {
				return true
			}
		}
		return false
	}
	var finalDirs []Action
	for _, a := range dirActs {
		switch a.Kind {
		case DelL, DelR:
			if keepUnder(a.Path, a.Kind) {
				// on recrée le dossier de l'autre côté à la place
				if a.Kind == DelL {
					finalDirs = append(finalDirs, Action{Kind: MkdirR, Path: a.Path, IsDir: true, Reason: "dossier conservé (contenu modifié)"})
				} else {
					finalDirs = append(finalDirs, Action{Kind: MkdirL, Path: a.Path, IsDir: true, Reason: "dossier conservé (contenu modifié)"})
				}
				continue
			}
		}
		finalDirs = append(finalDirs, a)
	}
	// ordre d'exécution : mkdir (parents d'abord), fichiers, puis suppressions de dossiers (enfants d'abord)
	var mk, del []Action
	for _, a := range finalDirs {
		if a.Kind == MkdirL || a.Kind == MkdirR {
			mk = append(mk, a)
		} else {
			del = append(del, a)
		}
	}
	sort.Slice(del, func(i, j int) bool { return depth(del[i].Path) > depth(del[j].Path) })
	plan.Actions = append(append(mk, plan.Actions...), del...)
	fillSides(plan)
	return plan, nil
}

// fillSides renseigne, pour chaque élément, sa taille et sa date de chaque côté
// (affichées dans l'interface et utilisées pour changer le sens d'une action).
func fillSides(plan *Plan) {
	fill := func(a *Action) {
		if a.Left == nil {
			e, ok := plan.Left[a.Path]
			a.Left = sideOf(e, ok)
		}
		if a.Right == nil {
			e, ok := plan.Right[a.Path]
			a.Right = sideOf(e, ok)
		}
	}
	for i := range plan.Actions {
		fill(&plan.Actions[i])
	}
	for i := range plan.Same {
		fill(&plan.Same[i])
	}
}

// Sens imposés par l'utilisateur pour un élément.
const (
	DirToRight = "right" // la droite devient comme la gauche (copie →, ou suppression à droite)
	DirToLeft  = "left"  // la gauche devient comme la droite
	DirNone    = "none"  // ne rien faire
)

// ApplyOverrides remplace l'action prévue pour les chemins choisis par
// l'utilisateur (chemin → DirToRight, DirToLeft ou DirNone).
func ApplyOverrides(plan *Plan, ov map[string]string) {
	if len(ov) == 0 {
		return
	}
	for i, a := range plan.Actions {
		d, ok := ov[a.Path]
		if !ok || a.Kind == Skip {
			continue
		}
		l, lok := plan.Left[a.Path]
		r, rok := plan.Right[a.Path]
		n := Action{Path: a.Path, IsDir: a.IsDir, Left: a.Left, Right: a.Right, Reason: "choix manuel"}
		switch d {
		case DirToRight:
			switch {
			case lok && l.IsDir:
				n.Kind, n.IsDir = MkdirR, true
			case lok:
				n.Kind, n.Size = CopyLR, l.Size
			default:
				n.Kind = DelR
			}
		case DirToLeft:
			switch {
			case rok && r.IsDir:
				n.Kind, n.IsDir = MkdirL, true
			case rok:
				n.Kind, n.Size = CopyRL, r.Size
			default:
				n.Kind = DelL
			}
		case DirNone:
			n.Kind = Leave
		default:
			continue
		}
		// un dossier présent des deux côtés n'a pas besoin d'être recréé
		if (n.Kind == MkdirR && rok) || (n.Kind == MkdirL && lok) {
			n.Kind = Leave
		}
		plan.Actions[i] = n
	}
	// Une suppression de dossier n'a lieu que si tout son contenu disparaît aussi.
	for i, a := range plan.Actions {
		if !a.IsDir || (a.Kind != DelL && a.Kind != DelR) {
			continue
		}
		prefix := a.Path + "/"
		for _, b := range plan.Actions {
			if strings.HasPrefix(b.Path, prefix) && b.Kind != a.Kind {
				plan.Actions[i].Kind, plan.Actions[i].Reason = Leave, "dossier conservé (contenu gardé)"
				break
			}
		}
	}
	plan.TotalBytes, plan.Conflicts = 0, 0
	for _, a := range plan.Actions {
		if a.Kind == CopyLR || a.Kind == CopyRL {
			plan.TotalBytes += a.Size
		}
		if a.Conflict {
			plan.Conflicts++
		}
	}
}

// Same est le type des éléments identiques (aucune action).
const Same Kind = "same"

// Excluded : élément exclu (filtre, caché, système…), affiché avec les inchangés.
const ExcludedKind Kind = "excluded"

func sameAction(p string, e vfs.Entry) Action {
	a := Action{Kind: Same, Path: p, IsDir: e.IsDir, Reason: "identique des deux côtés"}
	if !e.IsDir {
		a.Size = e.Size
	}
	return a
}

func depth(p string) int { return strings.Count(p, "/") }

// Choix possibles pour un conflit.
const (
	ChooseLeft  = "left"  // garder la version de gauche (la droite devient pareille)
	ChooseRight = "right" // garder la version de droite
	ChooseLeave = "leave" // ne rien faire pour ce fichier
)

// IsChoosable indique si une action est un conflit où l'on peut choisir un côté.
func IsChoosable(a Action) bool { return a.Conflict && a.Left != nil && a.Right != nil }

// DefaultChoice applique la règle par défaut ("newer", "left" ou "right").
func DefaultChoice(a Action, rule string) string {
	switch rule {
	case ChooseLeft, ChooseRight:
		return rule
	}
	// la plus récente ; un fichier supprimé d'un côté perd face à une version modifiée
	if !a.Left.Exists {
		return ChooseRight
	}
	if !a.Right.Exists {
		return ChooseLeft
	}
	if a.Right.ModTime.After(a.Left.ModTime) {
		return ChooseRight
	}
	return ChooseLeft
}

// ResolveConflicts réécrit les actions en conflit selon le choix de chaque fichier.
func ResolveConflicts(plan *Plan, choose func(a Action) string) {
	totalBytes := int64(0)
	for i, a := range plan.Actions {
		if IsChoosable(a) {
			c := choose(a)
			n := Action{Path: a.Path, Conflict: true, Left: a.Left, Right: a.Right}
			switch {
			case c == ChooseLeft && a.Left.Exists:
				n.Kind, n.Size, n.Reason = CopyLR, a.Left.Size, "conflit : version de gauche retenue"
			case c == ChooseLeft:
				n.Kind, n.Reason = DelR, "conflit : suppression (gauche) retenue"
			case c == ChooseRight && a.Right.Exists:
				n.Kind, n.Size, n.Reason = CopyRL, a.Right.Size, "conflit : version de droite retenue"
			case c == ChooseRight:
				n.Kind, n.Reason = DelL, "conflit : suppression (droite) retenue"
			default:
				n.Kind, n.Reason = Leave, "conflit laissé tel quel"
			}
			plan.Actions[i] = n
			a = n
		}
		if a.Kind == CopyLR || a.Kind == CopyRL {
			totalBytes += a.Size
		}
	}
	plan.TotalBytes = totalBytes
}

// AlignExcluded rend les exclusions symétriques : un élément exclu d'un côté
// (ex. caché à droite mais pas à gauche) est aussi retiré de l'autre côté, avec
// tout son contenu. Sinon il serait recopié à chaque passage.
func AlignExcluded(ls, rs map[string]vfs.Entry, skL, skR map[string]Skipped) {
	drop := func(side map[string]vfs.Entry, otherSk, sideSk map[string]Skipped) {
		if len(otherSk) == 0 {
			return
		}
		for p, e := range side {
			for q := p; ; {
				if s, ok := otherSk[q]; ok {
					delete(side, p)
					if sideSk != nil {
						sideSk[p] = Skipped{e, s.Reason + " (autre côté)"}
					}
					break
				}
				i := strings.LastIndexByte(q, '/')
				if i < 0 {
					break
				}
				q = q[:i]
			}
		}
	}
	drop(ls, skR, skL)
	drop(rs, skL, skR)
}
