//go:build !linux && !windows

package database

import (
	"errors"
	"os"
)

func openGeneratedDomainDatabaseFile(string, bool) (*os.File, bool, error) {
	return nil, false, errors.New("generated domain database platform is unsupported")
}

func validateGeneratedDomainDatabaseFile(*os.File, string) (os.FileInfo, error) {
	return nil, errors.New("generated domain database platform is unsupported")
}
