package app

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"synkro/internal/drives"
)

// Debug active le journal détaillé : réglage « Détaillé » des Options, ou forcé
// par un fichier « debug » à côté de l'exécutable / SYNKRO_DEBUG=1 (DebugForced).
// Tout va dans <données>/logs/synkrod.log.
var Debug bool

// DebugForced : journal détaillé imposé (fichier « debug » ou SYNKRO_DEBUG=1),
// quel que soit le réglage des Options.
var DebugForced bool

// Niveaux du journal du moteur (réglage « logLevel » des Options).
const (
	LogErrors = 1 // erreurs, plantages, avertissements (et démarrages)
	LogInfo   = 2 // + lancements, fins de tâches, planification…
	LogDebug  = 3 // + détail de chaque fichier, appels de l'interface
)

var (
	logOn    atomic.Bool  // réglage « logs » : écrire le journal du moteur (non par défaut)
	logLevel atomic.Int32 // LogErrors / LogInfo / LogDebug
)

func init() { logOn.Store(false); logLevel.Store(LogInfo) }

// ApplyLogSettings applique les réglages du journal (au démarrage et à chaque
// changement dans les Options).
func ApplyLogSettings(m map[string]string) {
	on := m["logs"] == "on" // désactivé par défaut : à activer dans les Options
	lvl := int32(LogInfo)
	switch m["logLevel"] {
	case "errors":
		lvl = LogErrors
	case "debug":
		lvl = LogDebug
	}
	if DebugForced {
		on, lvl = true, LogDebug
	}
	changed := logOn.Load() != on || logLevel.Load() != lvl
	wasOn := logOn.Load()
	logOn.Store(on)
	logLevel.Store(lvl)
	Debug = on && lvl == LogDebug
	// trace du changement (pas au démarrage : la ligne « exécutable » le dit déjà)
	if changed && logStarted.Load() {
		names := map[int32]string{LogErrors: "erreurs seulement", LogInfo: "normal", LogDebug: "détaillé"}
		switch {
		case !on && wasOn:
			logOn.Store(true) // dernière ligne écrite avant l'arrêt
			log.Printf("journal : arrêté depuis les Options")
			logOn.Store(false)
		case on && !wasOn: // la ligne de démarrage n'a pas été écrite : on la résume
			log.Printf("journal : activé dans les Options (niveau « %s ») · Synkro %s compilé le %s · %s/%s", names[lvl], Version, Build, runtime.GOOS, runtime.GOARCH)
		case on:
			log.Printf("journal : niveau « %s » choisi dans les Options", names[lvl])
		}
	}
}

// logStarted : le journal a démarré (les changements suivants sont tracés).
var logStarted atomic.Bool

// LevelWriter filtre les lignes du journal du moteur selon les réglages.
type LevelWriter struct{ W io.Writer }

// requêtes de l'interface : en échec (4xx, 5xx) ou lentes (plus de 3 s, voir logRequests)
var (
	apiFail = regexp.MustCompile(` API \S+ \S+ → [45]\d\d `)
	apiSlow = regexp.MustCompile(` API \S+ \S+ → \d+ en (\d+(\.\d+)?m\d|[3-9](\.\d+)?s|\d\d+(\.\d+)?s)`)
)

var errLine = regexp.MustCompile(`(?i)PLANTAGE|ERREUR|échec|échoué|ATTENTION|impossible|indisponible|manquée|=== Synkro|exécutable :|journal :`)

func lineLevel(p []byte) int32 {
	s := string(p)
	switch {
	case errLine.MatchString(s) || apiFail.MatchString(s):
		return LogErrors
	case apiSlow.MatchString(s):
		return LogInfo // requête de l'interface très lente : utile même hors débogage
	case strings.Contains(s, "[détail]") || strings.Contains(s, " API "):
		return LogDebug
	}
	return LogInfo
}

func (w LevelWriter) Write(p []byte) (int, error) {
	if !logOn.Load() || lineLevel(p) > logLevel.Load() {
		return len(p), nil // ligne écartée : pas une erreur pour le module log
	}
	return w.W.Write(p)
}

// Debugf écrit dans le journal du moteur seulement en mode détaillé.
func Debugf(format string, args ...any) {
	if Debug {
		log.Printf("[détail] "+format, args...)
	}
}

// LogStartup note l'environnement au démarrage (version, système, lecteurs).
func LogStartup(dataDir string) {
	exe, _ := os.Executable()
	host, _ := os.Hostname()
	log.Printf("=== Synkro %s (compilé le %s) démarré · %s/%s · Go %s · machine %q · %d processeurs", Version, Build, runtime.GOOS, runtime.GOARCH, runtime.Version(), host, runtime.NumCPU())
	logStarted.Store(true)
	log.Printf("exécutable : %s · données : %s · journal : %s%s", exe, dataDir,
		map[int32]string{LogErrors: "erreurs seulement", LogInfo: "normal", LogDebug: "détaillé"}[logLevel.Load()],
		map[bool]string{true: " (imposé par le fichier « debug »)", false: ""}[DebugForced])
	if osv := osVersion(); osv != "" {
		log.Printf("système : %s", osv)
	}
	// en arrière-plan : un disque qui se réveille peut prendre plus de 10 s à
	// répondre, il ne doit pas retarder l'ouverture de la fenêtre
	go func() {
		t := time.Now()
		if ds, err := drives.List(); err == nil {
			for _, d := range ds {
				log.Printf("lecteur %s « %s » n° %s %s amovible=%v libre=%d/%d", d.Root, d.Label, d.Serial, d.FS, d.Removable, d.Free, d.Total)
			}
		} else {
			log.Printf("lecteurs : %v", err)
		}
		if d := time.Since(t); d > 2*time.Second {
			log.Printf("liste des lecteurs lente : %s", d.Round(100*time.Millisecond))
		}
	}()
}

// statusWriter retient le code de réponse pour le journal.
type statusWriter struct {
	http.ResponseWriter
	code int
}

func (s *statusWriter) WriteHeader(c int) { s.code = c; s.ResponseWriter.WriteHeader(c) }

// logRequests journalise les requêtes de l'interface : toujours les erreurs et
// les requêtes lentes, et en mode détaillé toutes les actions (pas les
// interrogations répétées chaque seconde).
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, code: 200}
		next.ServeHTTP(sw, r)
		d := time.Since(start)
		poll := r.Method == http.MethodGet
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			return
		}
		if sw.code >= 400 || d > 3*time.Second || (Debug && !poll) {
			log.Printf("API %s %s → %d en %v", r.Method, r.URL.Path, sw.code, d.Round(time.Millisecond))
		}
	})
}

// clientLog reçoit les erreurs de l'interface (JavaScript) pour les garder dans le journal.
func clientLog(w http.ResponseWriter, r *http.Request) {
	var b [4096]byte
	n, _ := r.Body.Read(b[:])
	msg := strings.TrimSpace(string(b[:n]))
	if msg != "" {
		log.Printf("[interface] %s", msg)
	}
	w.WriteHeader(http.StatusNoContent)
}

func fmtDur(d time.Duration) string { return fmt.Sprint(d.Round(time.Millisecond)) }

func itoa(n int) string { return fmt.Sprint(n) }

// ScrubLogs retire des anciens journaux les noms d'utilisateur SFTP
// (« sftp://moi@hôte » devient « sftp://hôte »). Fait une seule fois.
func ScrubLogs(dataDir string) {
	dir := filepath.Join(dataDir, "logs")
	mark := filepath.Join(dir, ".sans-utilisateur")
	if _, err := os.Stat(mark); err == nil {
		return
	}
	re := regexp.MustCompile(`sftp://[^@\s/]+@`)
	files, _ := filepath.Glob(filepath.Join(dir, "*.log"))
	old, _ := filepath.Glob(filepath.Join(dir, "*.log.1"))
	files = append(files, old...)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil || !re.Match(b) {
			continue
		}
		os.WriteFile(f, re.ReplaceAll(b, []byte("sftp://")), 0o600)
	}
	os.WriteFile(mark, []byte("noms d'utilisateur SFTP retirés des journaux\n"), 0o600)
}
