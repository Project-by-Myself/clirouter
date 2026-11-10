package server

import (
	"os"
	"time"
)

func statPath(p string) (time.Time, error) {
	fi, err := os.Stat(p)
	if err != nil {
		return time.Time{}, err
	}
	return fi.ModTime(), nil
}
