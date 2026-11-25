//go:build !windows

package vfs

import (
	"io/fs"
	"strings"
)

// fileAttrs : hors Windows, un élément caché est un nom qui commence par « . ».
func fileAttrs(info fs.FileInfo) (hidden, system bool) {
	return strings.HasPrefix(info.Name(), "."), false
}

func clearReadOnly(string) bool { return false }
