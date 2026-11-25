//go:build !windows

package app

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// watchDir (hors Windows) : relecture périodique légère (dates et tailles).
func watchDir(ctx context.Context, dir string, onChange func(name string)) error {
	if _, err := os.Stat(dir); err != nil {
		return err
	}
	sig := func() map[string]string {
		m := map[string]string{}
		filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err == nil {
				if i, e := d.Info(); e == nil {
					m[p] = i.ModTime().String() + "|" + string(rune(i.Size()%1000003))
				}
			}
			return nil
		})
		return m
	}
	go func() {
		prev := sig()
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			cur := sig()
			for p, v := range cur {
				if prev[p] != v {
					onChange(p)
				}
			}
			for p := range prev {
				if _, ok := cur[p]; !ok {
					onChange(p)
				}
			}
			prev = cur
		}
	}()
	return nil
}
