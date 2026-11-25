// synkrod est le moteur de Synkro : serveur API local pour l'interface,
// et ligne de commande pour lancer les jobs (tâches planifiées, scripts).
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"text/tabwriter"
	"time"

	"synkro/internal/app"
	"synkro/internal/syncer"
)

const usage = `Synkro %s — synchronisation de dossiers (local, clé USB, SFTP)

Utilisation :
  synkrod [gui]                               ouvre l'interface dans le navigateur (double-clic)
  synkrod window                              ouvre l'interface dans une fenêtre Synkro (Windows)
  synkrod serve [--port N] [--data DOSSIER]   démarre le serveur local (utilisé par l'app)
  synkrod list  [--data DOSSIER]              liste les jobs
  synkrod analyze <nom|id> [--data DOSSIER]   affiche ce qui serait fait
  synkrod sync    <nom|id> [--data DOSSIER]   synchronise maintenant

Mode portable : posez un fichier vide nommé "portable" à côté de l'exécutable,
les données iront dans ./synkro-data (idéal sur clé USB).
`

func main() {
	// Double-clic sur l'exécutable (aucun argument) : on ouvre l'interface graphique.
	if len(os.Args) < 2 {
		os.Args = append(os.Args, defaultCmd)
	}
	cmd := os.Args[1]
	fl := flag.NewFlagSet(cmd, flag.ExitOnError)
	data := fl.String("data", "", "dossier de données")
	port := fl.Int("port", 0, "port d'écoute (0 = automatique)")
	// accepte « synkrod sync MonJob --data X » comme « synkrod sync --data X MonJob »
	var pos []string
	args := os.Args[2:]
	for len(args) > 0 {
		fl.Parse(args)
		if fl.NArg() == 0 {
			break
		}
		pos = append(pos, fl.Arg(0))
		args = fl.Args()[1:]
	}

	dir, err := app.DataDir(*data)
	check(err)
	// un seul Synkro à la fois par dossier de données (sinon deux instances
	// écriraient data.db et les états en même temps)
	switch cmd {
	case "serve", "gui", "window", "analyze", "sync":
		os.MkdirAll(dir, 0o700)
		if !lockDataDir(dir) {
			if cmd == "window" || cmd == "gui" {
				if !focusExisting() {
					messageBox("Synkro", "Synkro est déjà ouvert avec ce dossier de données :\n"+dir)
				}
				os.Exit(0)
			}
			fmt.Fprintln(os.Stderr, "Synkro est déjà en cours d'exécution avec ce dossier de données : "+dir)
			os.Exit(1)
		}
	}
	store, err := app.OpenStore(dir)
	check(err)
	// Journal du moteur (logs/synkrod.log) + traces de plantage fatales (logs/crash.log).
	app.ScrubLogs(dir) // anciens journaux : sans les noms d'utilisateur SFTP
	// au-delà de 20 Mo, l'ancien journal est gardé à côté (synkrod.log.1)
	if st, err := os.Stat(filepath.Join(dir, "logs", "synkrod.log")); err == nil && st.Size() > 20<<20 {
		os.Rename(filepath.Join(dir, "logs", "synkrod.log"), filepath.Join(dir, "logs", "synkrod.log.1"))
	}
	if lf, err := os.OpenFile(filepath.Join(dir, "logs", "synkrod.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		log.SetOutput(app.LevelWriter{W: teeWriter{lf, os.Stderr}})
	}
	if cf, err := os.OpenFile(filepath.Join(dir, "logs", "crash.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		fmt.Fprintf(cf, "--- démarrage %s (v%s)\n", time.Now().Format(time.RFC3339), app.Version)
		debug.SetCrashOutput(cf, debug.CrashOptions{})
	}
	// Journal détaillé : fichier « debug » à côté de l'exécutable, ou SYNKRO_DEBUG=1
	if exe, err := os.Executable(); err == nil {
		if _, err := os.Stat(filepath.Join(filepath.Dir(exe), "debug")); err == nil {
			app.DebugForced = true
		}
	}
	if os.Getenv("SYNKRO_DEBUG") == "1" {
		app.DebugForced = true
	}
	app.ApplyLogSettings(store.Settings()) // réglages du journal (Options)
	syncer.Logf = log.Printf
	if cmd == "serve" || cmd == "gui" || cmd == "window" {
		app.LogStartup(dir)
		if store.Warning != "" {
			log.Printf("ATTENTION : %s", store.Warning)
		}
	}
	runner := app.NewRunner(store)

	switch cmd {
	case "serve":
		serve(store, runner, *port, uiNone)
	case "gui":
		serve(store, runner, *port, uiBrowser)
	case "window":
		serve(store, runner, *port, uiWindow)
	case "list":
		tw := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tNOM\tMODE\tGAUCHE\tDROITE\tDERNIER PASSAGE")
		for _, j := range store.List() {
			last := "jamais"
			if !j.LastRun.IsZero() {
				last = j.LastRun.Format("2006-01-02 15:04") + " (" + j.LastResult + ")"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", j.ID, j.Name, j.Options.Mode, j.Left, j.Right, last)
		}
		tw.Flush()
	case "analyze", "sync":
		if len(pos) != 1 {
			fmt.Fprintln(os.Stderr, "indiquez le nom ou l'id du job")
			os.Exit(2)
		}
		doSync := cmd == "sync"
		var lastLine time.Time
		st, plan, res := runner.RunBlocking(pos[0], doSync, func(p syncer.Progress) {
			if time.Since(lastLine) > time.Second {
				lastLine = time.Now()
				fmt.Fprintf(os.Stderr, "\r%d/%d actions  %s / %s   ", p.DoneActions, p.TotalActions, human(p.DoneBytes), human(p.TotalBytes))
			}
		})
		fmt.Fprintln(os.Stderr)
		if !doSync {
			for _, a := range plan {
				fmt.Printf("%-8s %s   (%s)\n", a.Kind, a.Path, a.Reason)
			}
		}
		for _, r := range res {
			if r.Err != "" {
				fmt.Printf("ERREUR  %s %s : %s\n", r.Kind, r.Path, r.Err)
			}
		}
		fmt.Println(st.Message)
		if st.State == "error" {
			os.Exit(1)
		}
	default:
		fmt.Printf(usage, app.Version)
		os.Exit(2)
	}
}

// openBrowser ouvre l'URL dans le navigateur par défaut.
func openBrowser(url string) {
	var c *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		// rundll32 évite que « cmd /c start » coupe l'URL au caractère « & »
		c = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		c = exec.Command("open", url)
	default:
		c = exec.Command("xdg-open", url)
	}
	if err := c.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "Ouvrez cette adresse dans votre navigateur :", url)
	}
}

// defaultCmd : commande lancée par un double-clic (« window » pour Synkro.exe,
// fixée à la compilation avec -ldflags "-X main.defaultCmd=window").
var defaultCmd = "gui"

// Mode d'affichage de l'interface.
const (
	uiNone    = iota // serveur seul (application Electron)
	uiBrowser        // dans le navigateur par défaut
	uiWindow         // dans une fenêtre Synkro (WebView2), sans navigateur
)

func serve(store *app.Store, runner *app.Runner, port int, ui int) {
	gui := ui == uiBrowser
	tok := os.Getenv("SYNKRO_TOKEN")
	if tok == "" {
		b := make([]byte, 24)
		rand.Read(b)
		tok = hex.EncodeToString(b)
	}
	var ln net.Listener
	var err error
	if port == 0 && ui == uiWindow {
		// Port stable pour la fenêtre : l'interface garde ses réglages (largeur des
		// colonnes, tri…) d'un lancement à l'autre, car le navigateur intégré les
		// range par adresse. Il dépend du dossier de données : la version portable
		// et la version installée ne se gênent pas. Occupé : port au hasard.
		h := fnv.New32a()
		h.Write([]byte(strings.ToLower(store.Dir)))
		ln, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", 47900+int(h.Sum32()%900)))
	}
	if ln == nil {
		ln, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	}
	check(err)
	actual := ln.Addr().(*net.TCPAddr).Port
	srv := &app.Server{Store: store, Runner: runner, Token: tok, Port: actual}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go runner.Scheduler(ctx)

	// Ligne lue par l'application Electron pour se connecter.
	fmt.Printf("SYNKRO_READY port=%d token=%s\n", actual, tok)
	uiURL := fmt.Sprintf("http://127.0.0.1:%d/#token=%s", actual, tok)
	fmt.Fprintf(os.Stderr, "Interface : %s\nDonnées : %s\n", uiURL, store.Dir)
	if gui {
		fmt.Fprintln(os.Stderr, "\nSynkro est ouvert dans votre navigateur.\nLaissez cette fenêtre ouverte : la fermer arrête Synkro (et les synchros automatiques).")
		openBrowser(uiURL)
	}

	hs := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sh, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		hs.Shutdown(sh)
	}()
	// Arrêt quand l'application parente ferme l'entrée standard.
	if os.Getenv("SYNKRO_PARENT_STDIN") == "1" {
		go func() {
			buf := make([]byte, 64)
			for {
				if _, err := os.Stdin.Read(buf); err != nil {
					stop()
					return
				}
			}
		}()
	}
	if ui == uiWindow {
		go func() {
			if err := hs.Serve(ln); err != nil && err != http.ErrServerClosed {
				log.Printf("serveur : %v", err)
			}
		}()
		// la fenêtre tourne sur le fil principal ; la fermer arrête Synkro
		openWindow(uiURL, store, runner)
		stop()
		sh, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		hs.Shutdown(sh)
		return
	}
	if err := hs.Serve(ln); err != nil && err != http.ErrServerClosed {
		check(err)
	}
}

func human(n int64) string {
	units := []string{"o", "Ko", "Mo", "Go", "To"}
	f := float64(n)
	i := 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d %s", n, units[0])
	}
	return strings.Replace(fmt.Sprintf("%.1f %s", f, units[i]), ".", ",", 1)
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "erreur :", err)
		os.Exit(1)
	}
}

// teeWriter écrit dans le journal puis, si possible, sur la console. Une console
// absente (Synkro.exe sans fenêtre noire) ne doit pas empêcher d'écrire le journal
// (io.MultiWriter s'arrêterait à la première erreur).
type teeWriter struct{ file, console io.Writer }

func (t teeWriter) Write(p []byte) (int, error) {
	n, err := t.file.Write(p)
	t.console.Write(p)
	return n, err
}
