package harness

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// WithFileLock runs fn while holding an exclusive advisory lock on lockPath (created if absent), so concurrent
// processes serialize a read-modify-write of a shared file. Adapters use it around user-config trust writes (claude
// ~/.claude.json, codex ~/.codex/config.toml): without it two parallel dispatches read the same snapshot and one write
// drops the other's trust entry. The lock is released when fn returns.
func WithFileLock(lockPath string, fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return fmt.Errorf("lock mkdir: %w", err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open lock %s: %w", lockPath, err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock %s: %w", lockPath, err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}

// AtomicWriteFile writes data to path atomically: a UNIQUE temp file in the same directory, then rename. Unlike a fixed
// ".tmp" name, a unique temp is safe under concurrent writers (each rename is atomic and no two share a temp path).
func AtomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".cox-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}
