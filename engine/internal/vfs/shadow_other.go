//go:build !windows

package vfs

import "os"

func splitVolume(abs string) (string, string, error)  { return "", "", ErrShadowUnsupported }
func createShadow(vol string) (string, string, error) { return "", "", ErrShadowUnsupported }
func deleteShadow(id string) error                    { return nil }
func isLocked(err error) bool                         { return false }

// Elevated : lancé en administrateur ?
func Elevated() bool { return os.Geteuid() == 0 }
