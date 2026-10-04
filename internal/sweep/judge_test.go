package sweep

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeJudge answers from a table of file name to probability and records
// every request. Unknown names get 0.
type fakeJudge struct {
	probabilities map[string]float64
	// failDirectory makes requests for a Directory ending in this suffix fail.
	failDirectory string
	failWith      error
	// answerCountDelta adds or removes answers to simulate a broken judge.
	answerCountDelta int
	delay            time.Duration

	mutex    sync.Mutex
	requests []JudgeRequest
	inFlight atomic.Int32
	peak     atomic.Int32
}

func (judge *fakeJudge) JunkProbabilities(_ context.Context, request JudgeRequest) ([]float64, error) {
	current := judge.inFlight.Add(1)
	defer judge.inFlight.Add(-1)
	for {
		peak := judge.peak.Load()
		if current <= peak || judge.peak.CompareAndSwap(peak, current) {
			break
		}
	}
	time.Sleep(judge.delay)

	judge.mutex.Lock()
	judge.requests = append(judge.requests, request)
	judge.mutex.Unlock()

	if judge.failDirectory != "" && strings.HasSuffix(request.Directory, judge.failDirectory) {
		return nil, judge.failWith
	}
	answers := make([]float64, 0, len(request.Files))
	for _, file := range request.Files {
		answers = append(answers, judge.probabilities[file.Name])
	}
	if judge.answerCountDelta < 0 {
		answers = answers[:len(answers)+judge.answerCountDelta]
	}
	for range judge.answerCountDelta {
		answers = append(answers, 1)
	}
	return answers, nil
}

func (judge *fakeJudge) requestedDirectories() []string {
	judge.mutex.Lock()
	defer judge.mutex.Unlock()
	var directories []string
	for _, request := range judge.requests {
		directories = append(directories, request.Directory)
	}
	slices.Sort(directories)
	return directories
}

var junkTable = map[string]float64{
	"Thumbs.db":   0.95,
	"desktop.ini": 0.9,
	"lock.tmp":    0.8,
	"notes.txt":   0.1,
	"maybe.dat":   0.79,
}

func scanWithJudge(t *testing.T, root string, judge Judge, threshold float64) Result {
	t.Helper()
	result, err := Scan(context.Background(), root, Options{Judge: judge, JunkThreshold: threshold})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	return result
}

func TestJudgedJunkMakesDirectoriesEmpty(t *testing.T) {
	root := t.TempDir()
	buildTree(t, root, "a/Thumbs.db", "a/b/desktop.ini", "a/b/.DS_Store", "keep/notes.txt", "plain/")
	judge := &fakeJudge{probabilities: junkTable}
	result := scanWithJudge(t, root, judge, 0)

	if got, want := candidatePaths(t, result), []string{"a/b", "a", "plain"}; !slices.Equal(got, want) {
		t.Errorf("candidates = %q, want %q", got, want)
	}
	if got := len(result.Judgements); got != 3 {
		t.Fatalf("judgements = %d, want 3 (a, a/b, keep)", got)
	}
	for _, judgement := range result.Judgements {
		wantEmpty := filepath.Base(judgement.Path) != "keep"
		if judgement.Empty != wantEmpty || judgement.Err != nil || len(judgement.Files) != 1 {
			t.Errorf("judgement %+v", judgement)
		}
	}
	if result.JudgedFileCount() != 2 || result.JunkFileCount() != 3 {
		t.Errorf("judged files = %d (want 2), junk files = %d (want 3)", result.JudgedFileCount(), result.JunkFileCount())
	}
	for _, candidate := range result.Candidates {
		for _, junkFile := range candidate.JunkFiles {
			if junkFile.Judged == (junkFile.Name == ".DS_Store") {
				t.Errorf("%s: Judged = %v", junkFile.Name, junkFile.Judged)
			}
		}
	}
}

func TestJudgeThreshold(t *testing.T) {
	testCases := []struct {
		threshold float64
		file      string
		wantEmpty bool
	}{
		{threshold: 0, file: "lock.tmp", wantEmpty: true},   // default 0.8, equal counts
		{threshold: 0, file: "maybe.dat", wantEmpty: false}, // 0.79 < 0.8
		{threshold: 0.95, file: "desktop.ini", wantEmpty: false},
		{threshold: 0.5, file: "maybe.dat", wantEmpty: true},
	}
	for _, testCase := range testCases {
		t.Run(fmt.Sprintf("%s@%v", testCase.file, testCase.threshold), func(t *testing.T) {
			root := t.TempDir()
			buildTree(t, root, "dir/"+testCase.file)
			result := scanWithJudge(t, root, &fakeJudge{probabilities: junkTable}, testCase.threshold)
			if gotEmpty := len(result.Candidates) == 1; gotEmpty != testCase.wantEmpty {
				t.Errorf("empty = %v, want %v", gotEmpty, testCase.wantEmpty)
			}
		})
	}
}

func TestJudgeNotAskedWhenAChildIsKept(t *testing.T) {
	root := t.TempDir()
	buildTree(t, root, "parent/Thumbs.db", "parent/child/notes.txt", "parent/link-target.txt")
	judge := &fakeJudge{probabilities: junkTable}
	result := scanWithJudge(t, root, judge, 0)

	base := filepath.Base(root)
	if got, want := judge.requestedDirectories(), []string{base + "/parent/child"}; !slices.Equal(got, want) {
		t.Errorf("asked about %q, want only %q", got, want)
	}
	if len(result.Candidates) != 0 {
		t.Errorf("candidates = %q", candidatePaths(t, result))
	}
}

func TestJudgeNotAskedAboutBlockedDirectories(t *testing.T) {
	root := t.TempDir()
	buildTree(t, root, "linked/Thumbs.db")
	if err := os.Symlink(root, filepath.Join(root, "linked", "link")); err != nil {
		t.Fatal(err)
	}
	judge := &fakeJudge{probabilities: junkTable}
	scanWithJudge(t, root, judge, 0)
	if got := judge.requestedDirectories(); len(got) != 0 {
		t.Errorf("a directory holding a symlink must not be judged, asked about %q", got)
	}
}

func TestJudgeRequestContents(t *testing.T) {
	root := t.TempDir()
	buildTree(t, root, "outer/inner/empty/", "outer/inner/Thumbs.db", "outer/inner/.DS_Store")
	judge := &fakeJudge{probabilities: junkTable}
	scanWithJudge(t, root, judge, 0)

	if len(judge.requests) != 1 {
		t.Fatalf("requests = %+v", judge.requests)
	}
	request := judge.requests[0]
	if want := filepath.Base(root) + "/outer/inner"; request.Directory != want {
		t.Errorf("Directory = %q, want %q", request.Directory, want)
	}
	wantFiles := []FileInfo{{Name: "Thumbs.db", Size: int64(len("outer/inner/Thumbs.db"))}}
	if !slices.Equal(request.Files, wantFiles) {
		t.Errorf("Files = %+v, want %+v (.DS_Store is never sent)", request.Files, wantFiles)
	}
	if !slices.Equal(request.EmptySubdirectories, []string{"empty"}) {
		t.Errorf("EmptySubdirectories = %q", request.EmptySubdirectories)
	}
}

func TestJudgeErrorKeepsOnlyThatDirectory(t *testing.T) {
	root := t.TempDir()
	buildTree(t, root, "broken/Thumbs.db", "fine/Thumbs.db")
	judge := &fakeJudge{probabilities: junkTable, failDirectory: "/broken", failWith: errors.New("HTTP 500: boom")}
	result := scanWithJudge(t, root, judge, 0)

	if got := candidatePaths(t, result); !slices.Equal(got, []string{"fine"}) {
		t.Errorf("candidates = %q, want [fine]", got)
	}
	if len(result.Errors) != 1 || !strings.Contains(result.Errors[0].Error(), "boom") {
		t.Errorf("Errors = %v", result.Errors)
	}
	if result.Judgements[0].Err == nil || result.Judgements[0].Empty {
		t.Errorf("broken judgement = %+v", result.Judgements[0])
	}
}

func TestJudgeUnavailableStopsTheScan(t *testing.T) {
	root := t.TempDir()
	buildTree(t, root, "a/Thumbs.db")
	judge := &fakeJudge{failDirectory: "/a", failWith: fmt.Errorf("%w: HTTP 401", ErrJudgeUnavailable)}
	_, err := Scan(context.Background(), root, Options{Judge: judge})
	if !errors.Is(err, ErrJudgeUnavailable) {
		t.Errorf("err = %v, want ErrJudgeUnavailable", err)
	}
}

func TestJudgeWrongAnswerCountIsAnError(t *testing.T) {
	for _, delta := range []int{-1, 1} {
		root := t.TempDir()
		buildTree(t, root, "a/Thumbs.db", "a/desktop.ini")
		result := scanWithJudge(t, root, &fakeJudge{probabilities: junkTable, answerCountDelta: delta}, 0)
		if len(result.Candidates) != 0 || len(result.Errors) != 1 {
			t.Errorf("delta %d: candidates %q, errors %v", delta, candidatePaths(t, result), result.Errors)
		}
	}
}

func TestJudgeSkipsCrowdedDirectories(t *testing.T) {
	root := t.TempDir()
	var files []string
	for index := range MaxJudgedFiles + 1 {
		files = append(files, fmt.Sprintf("crowded/file%02d.tmp", index))
	}
	buildTree(t, root, files...)
	judge := &fakeJudge{}
	result := scanWithJudge(t, root, judge, 0)

	if len(judge.requests) != 0 {
		t.Error("a directory over MaxJudgedFiles must not be sent")
	}
	if len(result.Judgements) != 1 || result.Judgements[0].Skipped == "" || len(result.Errors) != 0 {
		t.Errorf("judgements %+v, errors %v", result.Judgements, result.Errors)
	}
}

func TestJudgeConcurrencyIsCapped(t *testing.T) {
	root := t.TempDir()
	var files []string
	for index := range 12 {
		files = append(files, fmt.Sprintf("dir%02d/Thumbs.db", index))
	}
	buildTree(t, root, files...)
	judge := &fakeJudge{probabilities: junkTable, delay: 20 * time.Millisecond}

	var snapshots []Progress
	result, err := Scan(context.Background(), root, Options{
		Judge:            judge,
		JudgeConcurrency: 3,
		Progress: func(progress Progress) {
			if progress.Phase == PhaseJudging {
				snapshots = append(snapshots, progress)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if peak := judge.peak.Load(); peak > 3 || peak < 2 {
		t.Errorf("peak concurrency = %d, want 2-3", peak)
	}
	if len(result.Candidates) != 12 {
		t.Errorf("candidates = %d, want 12", len(result.Candidates))
	}
	if len(snapshots) != 12 || snapshots[11].DirsJudged != 12 || snapshots[11].DirsToJudge != 12 {
		t.Errorf("judging progress = %+v", snapshots)
	}
	if !slices.IsSortedFunc(result.Judgements, func(left, right Judgement) int { return strings.Compare(left.Path, right.Path) }) {
		t.Error("judgements must be sorted by path")
	}
}

func TestWithoutJudgeOtherFilesKeepDirectories(t *testing.T) {
	root := t.TempDir()
	buildTree(t, root, "a/Thumbs.db")
	result := mustScan(t, root)
	if len(result.Candidates) != 0 || len(result.Judgements) != 0 {
		t.Errorf("result = %+v", result)
	}
}

func TestRemoveDeletesUnchangedJudgedFiles(t *testing.T) {
	root := t.TempDir()
	buildTree(t, root, "a/Thumbs.db", "a/.DS_Store")
	result := scanWithJudge(t, root, &fakeJudge{probabilities: junkTable}, 0)
	removal, err := Remove(context.Background(), result.Candidates, nil)
	if err != nil {
		t.Fatal(err)
	}
	if removal.DirsRemoved != 1 || removal.JunkRemoved != 2 || len(removal.Errors) != 0 {
		t.Errorf("removal = %+v", removal)
	}
}

func TestRemoveKeepsJudgedFilesThatChanged(t *testing.T) {
	root := t.TempDir()
	buildTree(t, root, "a/Thumbs.db", "a/.DS_Store")
	result := scanWithJudge(t, root, &fakeJudge{probabilities: junkTable}, 0)

	// Someone writes to the judged file between the scan and the removal.
	if err := os.WriteFile(filepath.Join(root, "a", "Thumbs.db"), []byte("now it matters"), 0o644); err != nil {
		t.Fatal(err)
	}
	removal, err := Remove(context.Background(), result.Candidates, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(removal.Errors) != 1 || !strings.Contains(removal.Errors[0].Error(), "changed since it was judged") {
		t.Errorf("Errors = %v", removal.Errors)
	}
	for _, kept := range []string{"a/Thumbs.db", "a/.DS_Store"} {
		if _, err := os.Stat(filepath.Join(root, kept)); err != nil {
			t.Errorf("%s must be untouched: %v", kept, err)
		}
	}
}
