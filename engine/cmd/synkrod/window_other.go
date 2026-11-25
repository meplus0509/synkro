//go:build !windows

package main

import "synkro/internal/app"

// openWindow : hors Windows, l'interface s'ouvre dans le navigateur et Synkro
// tourne jusqu'à Ctrl+C.
func openWindow(url string, _ *app.Store, _ *app.Runner) {
	openBrowser(url)
	select {}
}

func messageBox(title, text string) { println(title + " : " + text) }
