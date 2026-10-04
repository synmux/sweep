// Package judgecache keeps Judge answers in SQLite, so a real run after a dry
// run reuses the dry run's decisions instead of asking again.
//
// An answer is keyed to exactly what it was based on: the directory's path,
// each judged file's name, size and modification time, the empty
// subdirectories, and the Judge's fingerprint (model and question wording).
// Checking an entry is therefore as cheap as the stat calls the scan already
// makes; any change to a file, or to the question, gives a different key.
package judgecache

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	// Registers the pure-Go "sqlite" driver, so builds need no cgo.
	_ "modernc.org/sqlite"

	"github.com/synmux/sweep/internal/sweep"
)

const (
	// keyVersion changes whenever the key layout changes.
	keyVersion = 1
	// schemaVersion is stored in PRAGMA user_version.
	schemaVersion = 1
	// MaxAge is how long an answer is kept. The model behind an alias such
	// as jev-latest can change, so old answers expire.
	MaxAge = 30 * 24 * time.Hour
	// busyTimeout lets concurrent sweep runs wait for each other's writes.
	busyTimeout = 5 * time.Second
)

// DefaultPath returns the cache database path: $XDG_CACHE_HOME/sweep when
// that is set, otherwise ~/.cache/sweep, with the file judgements.db.
func DefaultPath() (string, error) {
	cacheHome := os.Getenv("XDG_CACHE_HOME")
	if cacheHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("finding the cache directory: %w", err)
		}
		cacheHome = filepath.Join(home, ".cache")
	}
	return filepath.Join(cacheHome, "sweep", "judgements.db"), nil
}

// EnsureDirectory creates the directory that will hold the database at
// path, private to the user, if it does not already exist.
func EnsureDirectory(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating the cache directory: %w", err)
	}
	return nil
}

// Store is an open cache database.
type Store struct {
	database *sql.DB
	now      func() time.Time
}

// Open opens or creates the cache at path, creating its directory, and
// removes entries older than MaxAge.
func Open(ctx context.Context, path string) (*Store, error) {
	if err := EnsureDirectory(path); err != nil {
		return nil, err
	}
	// Escape the path: a '#' or '?' in it would otherwise end the filename.
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(%d)&_pragma=journal_mode(WAL)",
		(&url.URL{Path: path}).EscapedPath(), busyTimeout.Milliseconds())
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening the cache %s: %w", path, err)
	}
	store := &Store{database: database, now: time.Now}
	if err := store.migrate(ctx); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("preparing the cache %s: %w", path, err)
	}
	if err := store.prune(ctx); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("pruning the cache %s: %w", path, err)
	}
	return store, nil
}

// Close closes the database.
func (store *Store) Close() error {
	return store.database.Close()
}

func (store *Store) migrate(ctx context.Context) error {
	var version int
	if err := store.database.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	switch version {
	case schemaVersion:
		return nil
	case 0:
		_, err := store.database.ExecContext(ctx, `
			CREATE TABLE IF NOT EXISTS judgements (
				key           TEXT PRIMARY KEY,
				directory     TEXT NOT NULL,
				judge         TEXT NOT NULL,
				probabilities TEXT NOT NULL,
				created_at    INTEGER NOT NULL
			);
			CREATE INDEX IF NOT EXISTS judgements_created_at ON judgements (created_at);
			PRAGMA user_version = 1;`)
		return err
	default:
		return fmt.Errorf("unknown cache schema version %d; delete the file to start afresh", version)
	}
}

func (store *Store) prune(ctx context.Context) error {
	cutoff := store.now().Add(-MaxAge).Unix()
	_, err := store.database.ExecContext(ctx, "DELETE FROM judgements WHERE created_at < ?", cutoff)
	return err
}

// Get returns the probabilities stored under key, if any.
func (store *Store) Get(ctx context.Context, key string) ([]float64, bool, error) {
	var encoded string
	err := store.database.QueryRowContext(ctx, "SELECT probabilities FROM judgements WHERE key = ?", key).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("reading the cache: %w", err)
	}
	var probabilities []float64
	if err := json.Unmarshal([]byte(encoded), &probabilities); err != nil {
		// A damaged row is a miss; the fresh answer overwrites it.
		return nil, false, nil
	}
	return probabilities, true, nil
}

// Put stores probabilities under key, replacing any older entry.
func (store *Store) Put(ctx context.Context, key, directory, judgeFingerprint string, probabilities []float64) error {
	encoded, err := json.Marshal(probabilities)
	if err != nil {
		return err
	}
	_, err = store.database.ExecContext(ctx, `
		INSERT INTO judgements (key, directory, judge, probabilities, created_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET
			directory = excluded.directory, judge = excluded.judge,
			probabilities = excluded.probabilities, created_at = excluded.created_at`,
		key, directory, judgeFingerprint, string(encoded), store.now().Unix())
	if err != nil {
		return fmt.Errorf("writing the cache: %w", err)
	}
	return nil
}

// keyMaterial is hashed into a cache key. Field order is fixed by the struct.
type keyMaterial struct {
	Version             int       `json:"version"`
	Judge               string    `json:"judge"`
	Path                string    `json:"path"`
	Directory           string    `json:"directory"`
	Files               []keyFile `json:"files"`
	EmptySubdirectories []string  `json:"empty_subdirectories"`
}

type keyFile struct {
	Name           string `json:"name"`
	Size           int64  `json:"size"`
	ModTimeNanosec int64  `json:"mtime_ns"`
}

// Key derives the cache key for a request under a judge fingerprint.
func Key(judgeFingerprint string, request sweep.JudgeRequest) string {
	material := keyMaterial{
		Version:             keyVersion,
		Judge:               judgeFingerprint,
		Path:                request.Path,
		Directory:           request.Directory,
		Files:               make([]keyFile, 0, len(request.Files)),
		EmptySubdirectories: append([]string{}, request.EmptySubdirectories...),
	}
	for _, file := range request.Files {
		material.Files = append(material.Files, keyFile{Name: file.Name, Size: file.Size, ModTimeNanosec: file.ModTime.UnixNano()})
	}
	encoded, _ := json.Marshal(material) // plain strings and numbers cannot fail
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// Fingerprinter is a Judge that can say what shapes its answers.
type Fingerprinter interface {
	sweep.Judge
	Fingerprint() string
}

// Judge wraps another Judge with the cache.
type Judge struct {
	inner       sweep.Judge
	fingerprint string
	store       *Store
}

// Wrap returns a Judge answering from store when it can, and asking inner
// (then storing the answer) when it cannot.
func Wrap(inner Fingerprinter, store *Store) *Judge {
	return &Judge{inner: inner, fingerprint: inner.Fingerprint(), store: store}
}

// JunkProbabilities implements sweep.Judge.
func (judge *Judge) JunkProbabilities(ctx context.Context, request sweep.JudgeRequest) (sweep.JudgeAnswer, error) {
	key := Key(judge.fingerprint, request)
	probabilities, found, err := judge.store.Get(ctx, key)
	if err != nil {
		return sweep.JudgeAnswer{}, err
	}
	if found && len(probabilities) == len(request.Files) {
		return sweep.JudgeAnswer{Probabilities: probabilities, Cached: true}, nil
	}

	answer, err := judge.inner.JunkProbabilities(ctx, request)
	if err != nil {
		return sweep.JudgeAnswer{}, err
	}
	if err := judge.store.Put(ctx, key, request.Path, judge.fingerprint, answer.Probabilities); err != nil {
		return sweep.JudgeAnswer{}, err
	}
	return answer, nil
}
