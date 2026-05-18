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

// bkp is a local helper for sizeBackups, holding a backup file's metadata.
type bkp struct {
	name string
	t    time.Time
	size int64
}

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

// sizeBackups enforces the backup-size retention policy using a Time
// Machine-style density thinning algorithm. It runs before count and age
// pruning and is a no-op when s.backupSize == 0.
//
// Density rules (never violated by the thinning):
//   - Last hour: every backup is kept.
//   - Previous 23 hours: at most one backup per hour bucket (most recent).
//   - Older: at most one backup per 24-hour day bucket (most recent).
//
// When the total backup size exceeds s.backupSize, candidates are deleted in
// priority order until the limit is met:
//  1. Extra files within each hourly bucket (bucket 1 first), oldest first.
//  2. Extra files within each daily bucket (newest day first), oldest first.
//  3. The hourly-23 keeper, if daily-1 already exists (it is about to age into
//     the daily zone anyway).
//  4. The oldest daily keeper, repeated until the limit is met.
func (s *srv) sizeBackups(backupDir string) {
	if s.backupSize <= 0 {
		return
	}

	files, err := listBackups(backupDir)
	if err != nil || len(files) == 0 {
		return
	}

	// Build slice with timestamps and sizes; accumulate total.
	backups := make([]bkp, 0, len(files))

	var totalSize int64

	for _, name := range files {
		fi, statErr := os.Stat(filepath.Join(backupDir, name))
		if statErr != nil {
			continue
		}

		t, parseErr := parseBackupTime(name)
		if parseErr != nil {
			continue
		}

		backups = append(backups, bkp{name, t, fi.Size()})
		totalSize += fi.Size()
	}

	if totalSize <= s.backupSize {
		return
	}

	now := time.Now().UTC()

	// Categorize into hourly[1..23] and daily[1..N] buckets.
	// listBackups returns oldest-first, so slices within buckets are oldest-first.
	hourly := make(map[int][]bkp)
	daily := make(map[int][]bkp)

	for _, b := range backups {
		h := int(now.Sub(b.t) / time.Hour)

		switch {
		case h == 0:
			continue // last hour — never touched
		case h >= 1 && h <= 23:
			hourly[h] = append(hourly[h], b)
		case h >= 24:
			daily[h/24] = append(daily[h/24], b)
		}
	}

	remove := func(b bkp) {
		if err := os.Remove(filepath.Join(backupDir, b.name)); err != nil {
			log.Printf("backup: removing %s (size limit): %v", b.name, err)

			return
		}

		totalSize -= b.size
		log.Printf("backup: removed %s (size limit %d bytes)", b.name, s.backupSize)
	}

	// Phase 1: extras within hourly buckets, newest bucket first (1 → 23).
	for h := 1; h <= 23 && totalSize > s.backupSize; h++ {
		bucket := hourly[h]
		// Keep bucket[last] (newest); delete bucket[0..last-1] oldest-first.
		for i := 0; i < len(bucket)-1 && totalSize > s.backupSize; i++ {
			remove(bucket[i])
		}
	}

	if totalSize <= s.backupSize {
		return
	}

	// Phase 2: extras within daily buckets, newest day first.
	dayKeys := make([]int, 0, len(daily))
	for d := range daily {
		dayKeys = append(dayKeys, d)
	}

	sort.Ints(dayKeys) // ascending: day 1 (most recent) first

	for _, d := range dayKeys {
		if totalSize <= s.backupSize {
			break
		}

		bucket := daily[d]
		for i := 0; i < len(bucket)-1 && totalSize > s.backupSize; i++ {
			remove(bucket[i])
		}
	}

	if totalSize <= s.backupSize {
		return
	}

	// Phase 3: hourly-to-daily bridge. The oldest hourly bucket (23) is about
	// to age into daily-1. If daily-1 already has a backup, the hourly-23
	// keeper is redundant and can be dropped.
	if bucket23 := hourly[23]; len(bucket23) > 0 {
		if _, hasDailyOne := daily[1]; hasDailyOne {
			remove(bucket23[len(bucket23)-1]) // the remaining keeper
		}
	}

	if totalSize <= s.backupSize {
		return
	}

	// Phase 4: delete oldest daily keepers until the limit is met.
	for i := len(dayKeys) - 1; i >= 0 && totalSize > s.backupSize; i-- {
		bucket := daily[dayKeys[i]]
		if len(bucket) > 0 {
			remove(bucket[len(bucket)-1]) // keeper = newest remaining in bucket
		}
	}
}

// ageBackups enforces all active retention policies in priority order:
// size-based thinning first, then count-based, then age-based.
func (s *srv) ageBackups(backupDir string) {
	s.sizeBackups(backupDir)

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

	// Age-based pruning: delete files whose embedded timestamp predates the cutoff.
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
