//go:build windows

package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"

	"synkro/internal/app"
)

func init() { runtime.LockOSThread() } // la fenêtre doit rester sur le fil principal

// openWindow affiche l'interface dans une fenêtre Windows (moteur Edge WebView2,
// présent sur Windows 10 et 11) et rend la main quand on la ferme.
func openWindow(url string, store *app.Store, runner *app.Runner) {
	mk := func(dir string) webview2.WebView {
		return webview2.NewWithOptions(webview2.WebViewOptions{
			// cache du moteur d'affichage : dans le dossier temporaire de Windows,
			// pour ne pas remplir la clé USB de centaines de petits fichiers
			DataPath:  dir,
			AutoFocus: true,
			WindowOptions: webview2.WindowOptions{
				Title: "Synkro", Width: 1400, Height: 860, Center: true,
				IconId: 1, // icône intégrée (rsrc_windows_amd64.syso, groupe d'icônes n° 1)
			},
		})
	}
	log.Printf("fenêtre : ouverture (WebView2)")
	w := mk(filepath.Join(os.TempDir(), "Synkro-fenetre"))
	if w == nil {
		// dossier du moteur d'affichage occupé (autre Synkro ouvert, ou restes
		// d'une session précédente) : nouvel essai avec un dossier à part
		dir := filepath.Join(os.TempDir(), fmt.Sprintf("Synkro-fenetre-%d", os.Getpid()))
		log.Printf("fenêtre : échec du premier essai, nouvel essai avec %s", dir)
		w = mk(dir)
		if w != nil {
			defer os.RemoveAll(dir)
		}
	}
	if w == nil {
		// WebView2 absent : on se rabat sur le navigateur
		log.Printf("WebView2 indisponible : interface ouverte dans le navigateur")
		openBrowser(url)
		messageBox("Synkro", "Le composant Microsoft Edge WebView2 est absent sur ce PC : l'interface s'est ouverte dans votre navigateur.\n\nCliquez sur OK pour arrêter Synkro (seulement quand vous avez fini).")
		return
	}
	defer w.Destroy()
	// fenêtre agrandie : s'adapte à tous les écrans
	syscall.NewLazyDLL("user32.dll").NewProc("ShowWindow").Call(uintptr(w.Window()), 3 /* SW_MAXIMIZE */)
	w.Navigate(url)
	log.Printf("fenêtre : affichée")
	shell := newWinShell(w, store, runner)
	defer shell.close()
	w.Run()
	log.Printf("fenêtre : fermée")
}

func messageBox(title, text string) {
	t, _ := syscall.UTF16PtrFromString(title)
	m, _ := syscall.UTF16PtrFromString(text)
	syscall.NewLazyDLL("user32.dll").NewProc("MessageBoxW").Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), 0x40)
}
