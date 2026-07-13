//go:build !windows && !unix

package fileutil

import (
	"fmt"
	"os"
	"runtime"
)

func ReadPrivateFile(path string) ([]byte, error) {
	return nil, fmt.Errorf("private file validation is unsupported on %s: %s", runtime.GOOS, path)
}

func SyncDir(string) error {
	return fmt.Errorf("directory sync is unsupported on %s", runtime.GOOS)
}

func ReplaceFile(_, _ string) error {
	return fmt.Errorf("durable file replacement is unsupported on %s", runtime.GOOS)
}

func SecureWriteFile(path string, data []byte, perm os.FileMode) error {
	return os.WriteFile(path, data, perm)
}

func SecureMkdirAll(path string, perm os.FileMode) error {
	return os.MkdirAll(path, perm)
}

func SecureChmod(path string, perm os.FileMode) error {
	return os.Chmod(path, perm)
}

func SecureOpenFile(path string, flag int, perm os.FileMode) (*os.File, error) {
	return os.OpenFile(path, flag, perm)
}
