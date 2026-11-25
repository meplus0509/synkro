package app

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"

	"synkro/internal/drives"
	"synkro/internal/syncer"
	"synkro/internal/vfs"
	"time"
)

//go:embed ui
var uiFiles embed.FS

// Server est l'API HTTP locale (127.0.0.1 uniquement, protégée par jeton).
type Server struct {
	Store  *Store
	Runner *Runner
	Token  string
	Port   int
}

type jobView struct {
	Job
	Status Status `json:"status"`
	// Pour un côté « Clé USB / disque » : le lecteur où la clé est branchée en ce moment.
	Drives map[string]driveView `json:"drives,omitempty"`
	// Pour un côté SFTP : informations du serveur relevées au dernier passage.
	Remote map[string]vfs.SFTPInfo `json:"remote,omitempty"`
}

type driveView struct {
	Plugged bool          `json:"plugged"`
	Drive   *drives.Drive `json:"drive,omitempty"`
	Dir     string        `json:"dir,omitempty"` // dossier réel (lecteur + chemin dans la clé)
	Error   string        `json:"error,omitempty"`
}

func driveInfo(e Endpoint) (driveView, bool) {
	if e.Type == "local" || e.Type == "" {
		// dossier local : lecteur qui le contient (lettre, nom, système, espace libre)
		if d, ok := drives.ForPath(e.Path); ok {
			return driveView{Plugged: true, Drive: &d, Dir: e.Path}, true
		}
		return driveView{}, false
	}
	if e.Type != "usb" || e.Volume == nil {
		return driveView{}, false
	}
	d, err := drives.Find(*e.Volume)
	if err != nil {
		return driveView{Error: err.Error()}, true
	}
	dir, _ := e.Resolve()
	return driveView{Plugged: true, Drive: &d, Dir: dir}, true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// guard : jeton obligatoire, et en-tête Host limité à localhost (anti DNS-rebinding).
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Un plantage dans une requête renvoie un message lisible au lieu de couper la connexion.
		defer func() {
			if p := recover(); p != nil {
				log.Printf("PLANTAGE sur %s %s : %v\n%s", r.Method, r.URL.Path, p, debug.Stack())
				writeErr(w, 500, fmt.Sprintf("erreur interne du moteur : %v", p))
			}
		}()
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		if host != "127.0.0.1" && host != "localhost" {
			writeErr(w, http.StatusForbidden, "hôte refusé")
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			tok := r.Header.Get("X-Synkro-Token")
			if subtle.ConstantTimeCompare([]byte(tok), []byte(s.Token)) != 1 {
				writeErr(w, http.StatusUnauthorized, "jeton invalide")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	sub, _ := fs.Sub(uiFiles, "ui")
	mux.Handle("GET /", http.FileServer(http.FS(sub)))

	mux.HandleFunc("GET /api/jobs", func(w http.ResponseWriter, r *http.Request) {
		jobs := s.Store.List()
		out := make([]jobView, 0, len(jobs))
		for _, j := range jobs {
			v := jobView{Job: j, Status: s.Runner.Status(j.ID), Remote: s.Runner.RemoteInfo(j.ID)}
			for side, e := range map[string]Endpoint{"left": j.Left, "right": j.Right} {
				if dv, ok := driveInfo(e); ok {
					if v.Drives == nil {
						v.Drives = map[string]driveView{}
					}
					v.Drives[side] = dv
				}
			}
			out = append(out, v)
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("POST /api/jobs", s.saveJob)
	mux.HandleFunc("PUT /api/jobs/{id}", s.saveJob)
	mux.HandleFunc("DELETE /api/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if s.Runner.Busy(id) {
			writeErr(w, 409, "job en cours : annulez-le d'abord")
			return
		}
		if err := s.Store.Delete(id); err != nil {
			writeErr(w, 404, err.Error())
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/jobs/{id}/analyze", func(w http.ResponseWriter, r *http.Request) { s.start(w, r, false) })
	mux.HandleFunc("POST /api/jobs/{id}/sync", func(w http.ResponseWriter, r *http.Request) { s.start(w, r, true) })
	mux.HandleFunc("POST /api/jobs/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		s.Runner.Cancel(r.PathValue("id"))
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	// Corriger seulement les dates des fichiers identiques à 1 ou 2 s près
	mux.HandleFunc("POST /api/jobs/{id}/fix-dates", func(w http.ResponseWriter, r *http.Request) {
		if err := s.Runner.StartFixDates(r.PathValue("id")); err != nil {
			writeErr(w, 409, err.Error())
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	// Clic droit sur un fichier : voir son contenu (début du fichier, brut)
	mux.HandleFunc("GET /api/jobs/{id}/file", func(w http.ResponseWriter, r *http.Request) {
		data, e, err := s.Runner.ReadItem(r.PathValue("id"), r.URL.Query().Get("path"), r.URL.Query().Get("side"), ViewMax)
		if err != nil {
			writeErr(w, 409, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Size", strconv.FormatInt(e.Size, 10))
		w.Header().Set("X-Modified", e.ModTime.UTC().Format(time.RFC3339))
		w.Header().Set("Cache-Control", "no-store")
		w.Write(data)
	})
	// … l'ouvrir avec son application, ou le montrer dans l'explorateur (côté local)
	mux.HandleFunc("POST /api/jobs/{id}/open-item", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Path, Side string
			Reveal     bool
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, 400, "requête invalide")
			return
		}
		p, err := s.Runner.LocalItem(r.PathValue("id"), body.Path, body.Side)
		if err == nil {
			err = openLocal(p, body.Reveal)
		}
		if err != nil {
			writeErr(w, 409, err.Error())
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	// Clic droit sur un élément : suppression, exclusion, inclusion.
	mux.HandleFunc("POST /api/jobs/{id}/delete-item", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Path, Side string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, 400, "requête invalide")
			return
		}
		msg, err := s.Runner.DeleteItem(r.PathValue("id"), body.Path, body.Side)
		if err != nil {
			writeErr(w, 409, err.Error())
			return
		}
		writeJSON(w, 200, map[string]string{"message": msg})
	})
	mux.HandleFunc("GET /api/jobs/{id}/trash", func(w http.ResponseWriter, r *http.Request) {
		info, err := s.Runner.TrashInfo(r.PathValue("id"))
		if err != nil {
			writeErr(w, 404, err.Error())
			return
		}
		writeJSON(w, 200, info)
	})
	mux.HandleFunc("POST /api/jobs/{id}/trash/empty", func(w http.ResponseWriter, r *http.Request) {
		msg, err := s.Runner.EmptyTrash(r.PathValue("id"))
		if err != nil {
			writeErr(w, 409, err.Error())
			return
		}
		writeJSON(w, 200, map[string]string{"message": msg})
	})
	mux.HandleFunc("POST /api/jobs/{id}/filter", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Kind, Pattern string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, 400, "requête invalide")
			return
		}
		if err := s.Runner.AddFilter(r.PathValue("id"), body.Kind, body.Pattern); err != nil {
			writeErr(w, 409, err.Error())
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/cancel-all", func(w http.ResponseWriter, r *http.Request) {
		s.Runner.CancelAll()
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	// vitesse : appliquée tout de suite à la synchro en cours (sans l'interrompre)
	// et enregistrée dans la tâche pour les fois suivantes
	mux.HandleFunc("POST /api/jobs/{id}/speed", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			MaxKBps int `json:"maxKBps"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.MaxKBps < 0 {
			writeErr(w, 400, "vitesse invalide")
			return
		}
		id := r.PathValue("id")
		j, err := s.Store.Get(id)
		if err != nil {
			writeErr(w, 404, err.Error())
			return
		}
		live := s.Runner.SetSpeed(id, body.MaxKBps) == nil
		j.Options.MaxKBps = body.MaxKBps
		if _, err := s.Store.Save(j); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		lim := "illimitée"
		if body.MaxKBps > 0 {
			lim = fmt.Sprintf("%d Ko/s", body.MaxKBps)
		}
		if live {
			log.Printf("job %q : vitesse changée en cours de synchronisation : %s", j.Name, lim)
			s.Store.AppendLog(id, "Vitesse changée en cours de synchronisation : "+lim)
		}
		writeJSON(w, 200, map[string]any{"maxKBps": body.MaxKBps, "live": live})
	})
	// premier lancement : les Options ont été montrées, on ne les rouvre plus
	mux.HandleFunc("POST /api/first-run-done", func(w http.ResponseWriter, r *http.Request) {
		s.Store.FirstRun = false
		w.WriteHeader(204)
	})
	mux.HandleFunc("POST /api/jobs/{id}/pause", func(w http.ResponseWriter, r *http.Request) {
		if err := s.Runner.Pause(r.PathValue("id")); err != nil {
			writeErr(w, 409, err.Error())
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/jobs/{id}/resume", func(w http.ResponseWriter, r *http.Request) {
		if err := s.Runner.Resume(r.PathValue("id")); err != nil {
			writeErr(w, 409, err.Error())
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	// Le plan complet (envoyé une fois par passage) et l'état de chaque action.
	mux.HandleFunc("GET /api/jobs/{id}/details", func(w http.ResponseWriter, r *http.Request) {
		plan, _ := s.Runner.Details(r.PathValue("id"))
		writeJSON(w, 200, map[string]any{"plan": plan, "marks": s.Runner.Marks(r.PathValue("id"))})
	})
	// Synchroniser plusieurs jobs l'un après l'autre.
	mux.HandleFunc("POST /api/queue", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			IDs []string `json:"ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.IDs) == 0 {
			writeErr(w, 400, "indiquez au moins un job")
			return
		}
		if err := s.Runner.Queue(body.IDs); err != nil {
			writeErr(w, 409, err.Error())
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	// Conflits en attente : les deux versions de chaque fichier et le choix proposé.
	mux.HandleFunc("GET /api/jobs/{id}/conflicts", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, s.Runner.Conflicts(r.PathValue("id")))
	})
	// Reprise (avec un choix par fichier) ou abandon d'une synchro en attente.
	mux.HandleFunc("POST /api/jobs/{id}/resolve", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Abort   bool              `json:"abort"`
			Choices map[string]string `json:"choices"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, 400, "requête invalide")
			return
		}
		for p, c := range body.Choices {
			if c != syncer.ChooseLeft && c != syncer.ChooseRight && c != syncer.ChooseLeave {
				writeErr(w, 400, "choix invalide pour "+p)
				return
			}
		}
		if err := s.Runner.Resolve(r.PathValue("id"), body.Choices, body.Abort); err != nil {
			writeErr(w, 409, err.Error())
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	// Les éléments identiques des deux côtés, chargés seulement si l'utilisateur les affiche.
	mux.HandleFunc("GET /api/jobs/{id}/same", func(w http.ResponseWriter, r *http.Request) {
		same := s.Runner.Same(r.PathValue("id"))
		if same == nil {
			same = []syncer.Action{}
		}
		writeJSON(w, 200, same)
	})
	// L'état seul, léger, interrogé chaque seconde pendant une synchro.
	mux.HandleFunc("GET /api/jobs/{id}/marks", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, s.Runner.Marks(r.PathValue("id")))
	})
	mux.HandleFunc("GET /api/jobs/{id}/log", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte(s.Store.ReadLog(r.PathValue("id"), 256*1024)))
	})
	// Parcourir les dossiers d'un serveur SFTP (avant d'enregistrer le job)
	mux.HandleFunc("POST /api/browse-sftp", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			JobID    string   `json:"jobId"`
			Side     string   `json:"side"`
			Endpoint Endpoint `json:"endpoint"`
			Path     string   `json:"path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Endpoint.SFTP == nil {
			writeErr(w, 400, "requête invalide")
			return
		}
		ep := s.withSecrets(body.JobID, body.Side, body.Endpoint)
		if ep.SFTP.Host == "" || ep.SFTP.User == "" {
			writeErr(w, 400, "indiquez d'abord le serveur et l'utilisateur")
			return
		}
		if ep.SFTP.Port == 0 {
			ep.SFTP.Port = 22
		}
		type res struct {
			l   vfs.SFTPListing
			err error
		}
		done := make(chan res, 1)
		go func() { l, err := vfs.BrowseSFTP(*ep.SFTP, body.Path); done <- res{l, err} }()
		select {
		case x := <-done:
			if x.err != nil {
				writeErr(w, 400, x.err.Error())
				return
			}
			writeJSON(w, 200, x.l)
		case <-time.After(30 * time.Second):
			writeErr(w, 504, "délai dépassé : le serveur ne répond pas (hôte, port ou pare-feu ?)")
		}
	})
	mux.HandleFunc("POST /api/test-endpoint", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			JobID    string   `json:"jobId"`
			Side     string   `json:"side"`
			Endpoint Endpoint `json:"endpoint"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, 400, "requête invalide")
			return
		}
		ep := s.withSecrets(body.JobID, body.Side, body.Endpoint)
		log.Printf("Test de connexion : %s", ep)
		type result struct {
			msg string
			err error
		}
		done := make(chan result, 1)
		go func() {
			defer func() {
				if p := recover(); p != nil {
					log.Printf("PLANTAGE pendant le test : %v\n%s", p, debug.Stack())
					done <- result{err: fmt.Errorf("erreur interne du moteur : %v", p)}
				}
			}()
			m, e := TestEndpoint(ep)
			done <- result{m, e}
		}()
		var res result
		select {
		case res = <-done:
		case <-time.After(45 * time.Second):
			res.err = fmt.Errorf("délai dépassé : le serveur ne répond pas (hôte, port ou pare-feu ?)")
		}
		msg, err := res.msg, res.err
		if err != nil {
			log.Printf("Test de connexion échoué : %v", err)
			writeJSON(w, 200, map[string]any{"ok": false, "message": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "message": msg})
	})
	// Navigation dans les dossiers locaux (utile hors Electron, en mode navigateur).
	mux.HandleFunc("GET /api/browse", func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Query().Get("path")
		if p == "" {
			p, _ = os.UserHomeDir()
		}
		p = filepath.Clean(p)
		// En cas d'erreur sur un élément, ReadDir renvoie quand même ceux lus avant.
		ents, err := os.ReadDir(p)
		if err != nil && len(ents) == 0 {
			writeErr(w, 400, err.Error())
			return
		}
		errMsg := ""
		if err != nil {
			errMsg = err.Error()
		}
		dirs, links := []string{}, []string{}
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			if e.IsDir() {
				dirs = append(dirs, e.Name())
			} else if vfs.IsLinkedDir(p, e) { // jonction ou lien vers un dossier
				dirs = append(dirs, e.Name())
				links = append(links, e.Name())
			}
		}
		sort.Slice(dirs, func(i, j int) bool { return strings.ToLower(dirs[i]) < strings.ToLower(dirs[j]) })
		writeJSON(w, 200, map[string]any{"path": p, "parent": filepath.Dir(p), "dirs": dirs, "links": links, "error": errMsg})
	})
	// Lecteurs branchés (pour choisir une clé USB dans le formulaire).
	mux.HandleFunc("GET /api/drives", func(w http.ResponseWriter, r *http.Request) {
		ds, err := drives.List()
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if ds == nil {
			ds = []drives.Drive{}
		}
		writeJSON(w, 200, ds)
	})
	mux.HandleFunc("GET /api/info", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"dataDir": s.Store.Dir, "version": Version, "build": Build, "warning": s.Store.Warning,
			"os": runtime.GOOS, "admin": vfs.Elevated(), "window": OnSettings != nil, "debugForced": DebugForced, "firstRun": s.Store.FirstRun})
	})
	mux.HandleFunc("GET /api/groups", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, s.Store.Groups())
	})
	mux.HandleFunc("POST /api/groups", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Action, Name, NewName string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, 400, "requête invalide")
			return
		}
		if err := s.Store.EditGroup(body.Action, body.Name, body.NewName); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, s.Store.Groups())
	})
	mux.HandleFunc("POST /api/jobs/{id}/group", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Action, Group string } // action : "add" ou "remove"
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, 400, "requête invalide")
			return
		}
		if err := s.Store.JobGroup(r.PathValue("id"), body.Action, body.Group); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	// Planifications (tâches et groupes à une date et une heure précises)
	mux.HandleFunc("GET /api/schedules", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, s.Store.Schedules())
	})
	mux.HandleFunc("POST /api/schedules", func(w http.ResponseWriter, r *http.Request) {
		var p Schedule
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			writeErr(w, 400, "date ou heure invalide")
			return
		}
		out, err := s.Store.AddSchedule(p)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("PUT /api/schedules/{id}", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Enabled bool }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, 400, "requête invalide")
			return
		}
		if err := s.Store.EditSchedule(r.PathValue("id"), &body.Enabled, false); err != nil {
			writeErr(w, 404, err.Error())
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("DELETE /api/schedules/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := s.Store.EditSchedule(r.PathValue("id"), nil, true); err != nil {
			writeErr(w, 404, err.Error())
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("PUT /api/jobs-order", func(w http.ResponseWriter, r *http.Request) {
		var ids []string
		if err := json.NewDecoder(r.Body).Decode(&ids); err != nil {
			writeErr(w, 400, "requête invalide")
			return
		}
		if err := s.Store.Reorder(ids); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/settings", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, s.Store.Settings())
	})
	mux.HandleFunc("PUT /api/settings", func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeErr(w, 400, "requête invalide")
			return
		}
		out, err := s.Store.SetSettings(in)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, out)
	})
	// raccourcis : existe-t-il ? (GET) / le supprimer (DELETE)
	mux.HandleFunc("GET /api/shortcut", func(w http.ResponseWriter, r *http.Request) {
		res := map[string]any{"exists": false}
		if ShortcutPath != nil {
			if p, err := ShortcutPath(r.URL.Query().Get("where")); err == nil {
				_, e := os.Stat(p)
				res["exists"], res["path"] = e == nil, p
			}
		}
		writeJSON(w, 200, res)
	})
	mux.HandleFunc("DELETE /api/shortcut", func(w http.ResponseWriter, r *http.Request) {
		if ShortcutPath == nil {
			writeErr(w, 400, "raccourcis : Windows seulement")
			return
		}
		p, err := ShortcutPath(r.URL.Query().Get("where"))
		if err == nil {
			err = os.Remove(p)
		}
		if err != nil {
			writeErr(w, 500, "suppression du raccourci : "+err.Error())
			return
		}
		log.Printf("raccourci supprimé : %s", p)
		writeJSON(w, 200, map[string]string{"path": p})
	})
	mux.HandleFunc("POST /api/shortcut", func(w http.ResponseWriter, r *http.Request) {
		if CreateShortcut == nil {
			writeErr(w, 400, "raccourcis : Windows seulement")
			return
		}
		p, err := CreateShortcut(r.URL.Query().Get("where"))
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		log.Printf("raccourci créé : %s", p)
		writeJSON(w, 200, map[string]string{"path": p})
	})
	mux.HandleFunc("POST /api/clientlog", clientLog)
	return logRequests(s.guard(mux))
}

// withSecrets complète un côté reçu de l'interface : un mot de passe masqué est
// remplacé par celui enregistré, et l'empreinte connue du serveur est reprise.
func (s *Server) withSecrets(jobID, side string, ep Endpoint) Endpoint {
	if ep.SFTP == nil || jobID == "" {
		return ep
	}
	j, err := s.Store.Get(jobID)
	if err != nil {
		return ep
	}
	old := j.Left.SFTP
	if side == "right" {
		old = j.Right.SFTP
	}
	if old == nil {
		return ep
	}
	c := *ep.SFTP
	if c.Password == SecretMask {
		c.Password = old.Password
	}
	if c.KeyPassphrase == SecretMask {
		c.KeyPassphrase = old.KeyPassphrase
	}
	if c.Host == old.Host && c.Port == old.Port {
		c.HostKey = old.HostKey
	}
	ep.SFTP = &c
	return ep
}

// Version de l'application.
// Version : numéro affiché (peut être fixé à la compilation, -X synkro/internal/app.Version=…).
var Version = "0.1.1"

// Build : date de compilation (fixée à la compilation, -X synkro/internal/app.Build=…).
var Build = "dev"

func (s *Server) saveJob(w http.ResponseWriter, r *http.Request) {
	var j Job
	if err := json.NewDecoder(r.Body).Decode(&j); err != nil {
		writeErr(w, 400, "requête invalide : "+err.Error())
		return
	}
	if id := r.PathValue("id"); id != "" {
		j.ID = id
		if s.Runner.Busy(id) {
			writeErr(w, 409, "job en cours : attendez la fin pour le modifier")
			return
		}
	} else {
		j.ID = ""
	}
	saved, err := s.Store.Save(j)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, saved)
}

func (s *Server) start(w http.ResponseWriter, r *http.Request, doSync bool) {
	// corps facultatif : {"overrides": {"chemin": "right"|"left"|"none"}}
	var body struct {
		Overrides map[string]string `json:"overrides"`
		Order     []string          `json:"order"` // ordre de copie (liste triée par l'utilisateur)
	}
	if r.ContentLength != 0 {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	for p, d := range body.Overrides {
		if d != syncer.DirToRight && d != syncer.DirToLeft && d != syncer.DirNone {
			writeErr(w, 400, "sens invalide pour "+p)
			return
		}
	}
	if err := s.Runner.StartWith(r.PathValue("id"), doSync, body.Overrides, body.Order); err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
