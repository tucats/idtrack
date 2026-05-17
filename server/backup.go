package server

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	backupDirName    = "idtrack-backups"
	backupFilePrefix = "idtrack-"
	backupFileSuffix = ".db"
	backupTimeLayout = "20060102T150405"
)

// startBackups creates the backup directory, performs an immediate startup
// backup (no quiescing needed — the server is not yet serving requests), then
// launches a goroutine that repeats the backup every s.backupInterval.
func (s *srv) startBackups() {
	backupDir := filepath.Join(filepath.Dir(s.dbPath), backupDirName)
	if err := os.MkdirAll(backupDir, 0700); err != nil {
		log.Printf("backup: cannot create directory %q: %v", backupDir, err)

		return
	}

	dst := newBackupPath(backupDir)
	if err := copyFile(s.dbPath, dst); err != nil {
		log.Printf("backup: startup backup failed: %v", err)
	} else {
		log.Printf("backup: created %s (startup)", filepath.Base(dst))
		go s.ageBackups(backupDir)
	}

	go func() {
		ticker := time.NewTicker(s.backupInterval)
		defer ticker.Stop()

		for range ticker.C {
			if err := s.doBackup(backupDir); err != nil {
				log.Printf("backup: periodic backup failed: %v", err)
			}
		}
	}()
}

// doBackup acquires the write lock (quiescing all in-flight requests), copies
// the database file into the backup directory, then releases the lock and
// starts aging in the background.
func (s *srv) doBackup(backupDir string) error {
	s.backupMu.Lock()
	defer s.backupMu.Unlock()

	dst := newBackupPath(backupDir)
	if err := copyFile(s.dbPath, dst); err != nil {
		return fmt.Errorf("copying %s: %w", s.dbPath, err)
	}

	log.Printf("backup: created %s", filepath.Base(dst))
	go s.ageBackups(backupDir)

	return nil
}

// newBackupPath returns the full path for a new backup file whose name encodes
// the current UTC time (e.g. "idtrack-20260517T143000.db"). Alphabetical order
// equals chronological order for this naming scheme.
func newBackupPath(dir string) string {
	name := backupFilePrefix + time.Now().UTC().Format(backupTimeLayout) + backupFileSuffix

	return filepath.Join(dir, name)
}

// copyFile copies src to dst with an fsync before close to ensure the data
// reaches disk before the backup is considered complete.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}

	return out.Sync()
}

// listBackups returns the filenames of all backup files in dir, sorted
// alphabetically (= chronologically for our timestamp-based naming scheme).
func listBackups(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var names []string

	for _, e := range entries {
		n := e.Name()
		if !e.IsDir() && strings.HasPrefix(n, backupFilePrefix) && strings.HasSuffix(n, backupFileSuffix) {
			names = append(names, n)
		}
	}

	sort.Strings(names)

	return names, nil
}

// parseBackupTime extracts the timestamp from a backup filename of the form
// "idtrack-20060102T150405.db". The timestamp is parsed as UTC.
func parseBackupTime(name string) (time.Time, error) {
	stripped := strings.TrimSuffix(strings.TrimPrefix(name, backupFilePrefix), backupFileSuffix)

	return time.ParseInLocation(backupTimeLayout, stripped, time.UTC)
}

// ageBackups enforces the backup-count and backup-age retention policies.
// Count pruning runs first (oldest files deleted), then age pruning removes
// any remaining files whose embedded timestamp predates the cutoff.
func (s *srv) ageBackups(backupDir string) {
	files, err := listBackups(backupDir)
	if err != nil {
		log.Printf("backup: listing backups for aging: %v", err)

		return
	}

	// Count-based pruning: keep only the most recent s.backupCount files.
	if s.backupCount > 0 && len(files) > s.backupCount {
		excess := files[:len(files)-s.backupCount]

		for _, name := range excess {
			path := filepath.Join(backupDir, name)
			if err := os.Remove(path); err != nil {
				log.Printf("backup: removing %s (count limit): %v", name, err)
			} else {
				log.Printf("backup: removed %s (count limit %d)", name, s.backupCount)
			}
		}

		// Re-list after count pruning so age pruning works on the updated set.
		files, err = listBackups(backupDir)
		if err != nil {
			log.Printf("backup: re-listing after count pruning: %v", err)

			return
		}
	}

	// Age-based pruning: delete files whose embedded timestamp is older than
	// the cutoff. The filename is the authoritative age source, not the mtime.
	if s.backupAge > 0 {
		cutoff := time.Now().UTC().Add(-s.backupAge)

		for _, name := range files {
			t, err := parseBackupTime(name)
			if err != nil {
				continue // skip files that do not match our naming scheme
			}

			if t.Before(cutoff) {
				path := filepath.Join(backupDir, name)
				if err := os.Remove(path); err != nil {
					log.Printf("backup: removing %s (age limit): %v", name, err)
				} else {
					log.Printf("backup: removed %s (age > %s)", name, s.backupAge)
				}
			}
		}
	}
}

// quiesce wraps each HTTP request in a read-lock on backupMu. Backup
// operations take the write lock, so they block until in-flight requests
// finish, and new requests block until the backup releases the write lock.
// When backup is disabled the mutex is never write-locked and RLock is
// essentially free.
func (s *srv) quiesce(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.backupMu.RLock()
		defer s.backupMu.RUnlock()
		next.ServeHTTP(w, r)
	})
}
