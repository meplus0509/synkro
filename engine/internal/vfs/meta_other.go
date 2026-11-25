//go:build !windows

package vfs

import "errors"

var errNoMeta = errors.New("date de création et attributs : Windows seulement")

func getMeta(string) (Meta, error)           { return Meta{}, errNoMeta }
func setMeta(string, Meta, bool, bool) error { return errNoMeta }
