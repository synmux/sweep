package judgecache

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/synmux/sweep/internal/sweep"
)

func openStore(t *testing.T, path string) *Store {
	t.Helper()
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func tempStorePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "nested", "judgements.db")
}

func TestDefaultPath(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "/xdg/cache")
	if path, err := DefaultPath(); err != nil || path != "/xdg/cache/sweep/judgements.db" {
		t.Errorf("with XDG_CACHE_HOME: %q, %v", path, err)
	}
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("HOME", "/home/someone")
	if path, err := DefaultPath(); err != nil || path != "/home/someone/.cache/sweep/judgements.db" {
		t.Errorf("without XDG_CACHE_HOME: %q, %v", path, err)
	}
}

func TestOpenCreatesPrivateDirectoryAndSchema(t *testing.T) {
	path := tempStorePath(t)
	store := openStore(t, path)
	info, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if permissions := info.Mode().Perm(); permissions != 0o700 {
		t.Errorf("cache directory permissions = %o, want 700", permissions)
	}
	var version int
	if err := store.database.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != schemaVersion {
		t.Errorf("user_version = %d, %v", version, err)
	}
	// Reopening an existing cache works.
	openStore(t, path)
}

func TestOpenRejectsUnknownSchema(t *testing.T) {
	path := tempStorePath(t)
	store := openStore(t, path)
	if _, err := store.database.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), path); err == nil {
		t.Error("an unknown schema version must be an error")
	}
}

func TestGetAndPut(t *testing.T) {
	ctx := context.Background()
	store := openStore(t, tempStorePath(t))
	if _, found, err := store.Get(ctx, "missing"); found || err != nil {
		t.Errorf("miss: found=%v err=%v", found, err)
	}
	if err := store.Put(ctx, "key", "/dir", "judge", []float64{0.9, 0.1}); err != nil {
		t.Fatal(err)
	}
	if probabilities, found, err := store.Get(ctx, "key"); !found || err != nil || !slices.Equal(probabilities, []float64{0.9, 0.1}) {
		t.Errorf("hit: %v %v %v", probabilities, found, err)
	}
	if err := store.Put(ctx, "key", "/dir", "judge", []float64{0.2}); err != nil {
		t.Fatal(err)
	}
	if probabilities, _, _ := store.Get(ctx, "key"); !slices.Equal(probabilities, []float64{0.2}) {
		t.Errorf("overwrite: %v", probabilities)
	}
}

func TestDamagedRowIsAMiss(t *testing.T) {
	ctx := context.Background()
	store := openStore(t, tempStorePath(t))
	if _, err := store.database.Exec(`INSERT INTO judgements VALUES ('key', '/d', 'j', 'not json', 0)`); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Get(ctx, "key"); found || err != nil {
		t.Errorf("damaged row: found=%v err=%v", found, err)
	}
}

func TestOpenPrunesOldEntries(t *testing.T) {
	ctx := context.Background()
	path := tempStorePath(t)
	store := openStore(t, path)
	realNow := time.Now()
	store.now = func() time.Time { return realNow.Add(-MaxAge - time.Hour) }
	if err := store.Put(ctx, "old", "/d", "j", []float64{1}); err != nil {
		t.Fatal(err)
	}
	store.now = time.Now
	if err := store.Put(ctx, "fresh", "/d", "j", []float64{1}); err != nil {
		t.Fatal(err)
	}

	reopened := openStore(t, path)
	if _, found, _ := reopened.Get(ctx, "old"); found {
		t.Error("an entry older than MaxAge should be pruned on Open")
	}
	if _, found, _ := reopened.Get(ctx, "fresh"); !found {
		t.Error("a fresh entry must survive pruning")
	}
}

func TestAwkwardPath(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "dir with spaces #1", "judgements.db")
	store := openStore(t, path)
	if err := store.Put(ctx, "key", "/d", "j", []float64{0.5}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("database not at the exact path: %v", err)
	}
}

var baseRequest = sweep.JudgeRequest{
	Path:                "/data/photos",
	Directory:           "data/photos",
	Files:               []sweep.FileInfo{{Name: "Thumbs.db", Size: 10, ModTime: time.Unix(1000, 5)}},
	EmptySubdirectories: []string{"previews"},
}

func TestKeyChangesWithEveryInput(t *testing.T) {
	base := Key("judge", baseRequest)
	if Key("judge", baseRequest) != base {
		t.Fatal("Key must be deterministic")
	}
	mutations := map[string]func(*sweep.JudgeRequest) string{
		"path":      func(request *sweep.JudgeRequest) string { request.Path = "/other"; return "judge" },
		"directory": func(request *sweep.JudgeRequest) string { request.Directory = "other"; return "judge" },
		"name":      func(request *sweep.JudgeRequest) string { request.Files[0].Name = "x"; return "judge" },
		"size":      func(request *sweep.JudgeRequest) string { request.Files[0].Size = 11; return "judge" },
		"mtime": func(request *sweep.JudgeRequest) string {
			request.Files[0].ModTime = time.Unix(1000, 6)
			return "judge"
		},
		"extra file": func(request *sweep.JudgeRequest) string {
			request.Files = append(request.Files, sweep.FileInfo{Name: "y"})
			return "judge"
		},
		"subdirs": func(request *sweep.JudgeRequest) string { request.EmptySubdirectories = nil; return "judge" },
		"judge":   func(*sweep.JudgeRequest) string { return "other judge" },
	}
	for name, mutate := range mutations {
		request := baseRequest
		request.Files = slices.Clone(baseRequest.Files)
		request.EmptySubdirectories = slices.Clone(baseRequest.EmptySubdirectories)
		fingerprint := mutate(&request)
		if Key(fingerprint, request) == base {
			t.Errorf("changing %s must change the key", name)
		}
	}
}

// countingJudge answers 0.9 for every file and counts calls.
type countingJudge struct {
	calls atomic.Int32
	err   error
}

func (judge *countingJudge) JunkProbabilities(_ context.Context, request sweep.JudgeRequest) (sweep.JudgeAnswer, error) {
	judge.calls.Add(1)
	if judge.err != nil {
		return sweep.JudgeAnswer{}, judge.err
	}
	probabilities := make([]float64, len(request.Files))
	for index := range probabilities {
		probabilities[index] = 0.9
	}
	return sweep.JudgeAnswer{Probabilities: probabilities}, nil
}

func (*countingJudge) Fingerprint() string { return "counting" }

func TestWrapAnswersFromCache(t *testing.T) {
	ctx := context.Background()
	inner := &countingJudge{}
	judge := Wrap(inner, openStore(t, tempStorePath(t)))

	first, err := judge.JunkProbabilities(ctx, baseRequest)
	if err != nil || first.Cached || inner.calls.Load() != 1 {
		t.Fatalf("first: %+v, %v, calls %d", first, err, inner.calls.Load())
	}
	second, err := judge.JunkProbabilities(ctx, baseRequest)
	if err != nil || !second.Cached || !slices.Equal(second.Probabilities, first.Probabilities) || inner.calls.Load() != 1 {
		t.Errorf("second: %+v, %v, calls %d", second, err, inner.calls.Load())
	}

	touched := baseRequest
	touched.Files = []sweep.FileInfo{{Name: "Thumbs.db", Size: 10, ModTime: time.Unix(2000, 0)}}
	if third, _ := judge.JunkProbabilities(ctx, touched); third.Cached || inner.calls.Load() != 2 {
		t.Errorf("a modified file must miss the cache: %+v, calls %d", third, inner.calls.Load())
	}
}

func TestWrapIgnoresMismatchedEntries(t *testing.T) {
	ctx := context.Background()
	store := openStore(t, tempStorePath(t))
	inner := &countingJudge{}
	if err := store.Put(ctx, Key(inner.Fingerprint(), baseRequest), "/d", "counting", []float64{0.1, 0.2}); err != nil {
		t.Fatal(err)
	}
	answer, err := Wrap(inner, store).JunkProbabilities(ctx, baseRequest)
	if err != nil || answer.Cached || len(answer.Probabilities) != 1 || inner.calls.Load() != 1 {
		t.Errorf("answer %+v, err %v, calls %d", answer, err, inner.calls.Load())
	}
}

func TestWrapDoesNotCacheErrors(t *testing.T) {
	ctx := context.Background()
	store := openStore(t, tempStorePath(t))
	failing := &countingJudge{err: errors.New("HTTP 500")}
	if _, err := Wrap(failing, store).JunkProbabilities(ctx, baseRequest); err == nil {
		t.Fatal("the inner error must come through")
	}
	if _, found, _ := store.Get(ctx, Key(failing.Fingerprint(), baseRequest)); found {
		t.Error("a failed answer must not be cached")
	}
}

func TestConcurrentUse(t *testing.T) {
	ctx := context.Background()
	judge := Wrap(&countingJudge{}, openStore(t, tempStorePath(t)))
	var group sync.WaitGroup
	errs := make(chan error, 64)
	for index := range 64 {
		group.Go(func() {
			request := baseRequest
			request.Path = fmt.Sprintf("/data/%d", index%16)
			if _, err := judge.JunkProbabilities(ctx, request); err != nil {
				errs <- err
			}
		})
	}
	group.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
