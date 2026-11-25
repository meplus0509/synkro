package vfs

import (
	"errors"
	"syscall"
)

// Hint ajoute une explication aux erreurs Windows peu parlantes.
func Hint(err error) string {
	var en syscall.Errno
	if !errors.As(err, &en) {
		return ""
	}
	switch uint32(en) {
	case 1117: // ERROR_IO_DEVICE
		return " — erreur d'entrée/sortie : le plus souvent un antivirus qui bloque ou met le fichier en quarantaine, un fichier OneDrive non téléchargé, ou un disque défaillant"
	case 225: // ERROR_VIRUS_INFECTED
		return " — fichier bloqué par l'antivirus"
	case 362, 389: // fichier dans le cloud non disponible
		return " — fichier stocké seulement dans le cloud (OneDrive…) : à télécharger d'abord"
	}
	return ""
}
