package ui

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"
)

const testTimeout = 5 * time.Second

// makeFixture builds a tree with two removable directories (one holding a
// .DS_Store) and one kept directory, returning the root.
func makeFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"empty/nested", "junk", "kept"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{"junk/.DS_Store", "kept/file.txt"} {
		if err := os.WriteFile(filepath.Join(root, file), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	return err == nil
}

func startModel(t *testing.T, options Options) *teatest.TestModel {
	t.Helper()
	return teatest.NewTestModel(t, New(context.Background(), options), teatest.WithInitialTermSize(100, 30))
}

func finalOutcome(t *testing.T, testModel *teatest.TestModel) Outcome {
	t.Helper()
	model, ok := testModel.FinalModel(t, teatest.WithFinalTimeout(testTimeout)).(*Model)
	if !ok {
		t.Fatal("final model has the wrong type")
	}
	return model.Outcome()
}

func waitForPrompt(t *testing.T, testModel *teatest.TestModel) {
	t.Helper()
	teatest.WaitFor(t, testModel.Output(), func(output []byte) bool {
		return bytes.Contains(output, []byte("(y/N)"))
	}, teatest.WithDuration(testTimeout))
}

func TestDryRunRemovesNothing(t *testing.T) {
	root := makeFixture(t)
	outcome := finalOutcome(t, startModel(t, Options{Root: root, DryRun: true}))

	if outcome.Err != nil || outcome.Cancelled {
		t.Fatalf("outcome = %+v", outcome)
	}
	if got := len(outcome.Summary.Candidates); got != 3 {
		t.Errorf("candidates = %d, want 3 (empty/nested, empty, junk)", got)
	}
	if outcome.Summary.Removal != nil {
		t.Error("dry run must not remove")
	}
	if !outcome.Summary.DryRun || outcome.Summary.DirsScanned != 5 {
		t.Errorf("summary = %+v", outcome.Summary)
	}
	for _, dir := range []string{"empty/nested", "junk/.DS_Store"} {
		if !exists(t, filepath.Join(root, dir)) {
			t.Errorf("%s was removed during a dry run", dir)
		}
	}
}

func TestConfirmRemoves(t *testing.T) {
	root := makeFixture(t)
	testModel := startModel(t, Options{Root: root})
	waitForPrompt(t, testModel)
	testModel.Type("y")
	outcome := finalOutcome(t, testModel)

	if outcome.Cancelled || outcome.Summary.Removal == nil {
		t.Fatalf("outcome = %+v", outcome)
	}
	if removal := outcome.Summary.Removal; removal.DirsRemoved != 3 || removal.JunkRemoved != 1 {
		t.Errorf("removal = %+v", removal)
	}
	for _, gone := range []string{"empty", "junk"} {
		if exists(t, filepath.Join(root, gone)) {
			t.Errorf("%s should have been removed", gone)
		}
	}
	if !exists(t, filepath.Join(root, "kept", "file.txt")) {
		t.Error("kept/file.txt must survive")
	}
}

func TestDeclineRemovesNothing(t *testing.T) {
	for _, keys := range []string{"n", "q"} {
		t.Run(keys, func(t *testing.T) {
			root := makeFixture(t)
			testModel := startModel(t, Options{Root: root})
			waitForPrompt(t, testModel)
			testModel.Type(keys)
			outcome := finalOutcome(t, testModel)

			if !outcome.Cancelled || outcome.Summary.Removal != nil {
				t.Errorf("outcome = %+v", outcome)
			}
			if !exists(t, filepath.Join(root, "empty", "nested")) {
				t.Error("nothing should be removed after declining")
			}
		})
	}
}

func TestEscapeAndCtrlCDecline(t *testing.T) {
	keyPresses := map[string]tea.KeyPressMsg{
		"esc":    {Code: tea.KeyEscape},
		"ctrl+c": {Code: 'c', Mod: tea.ModCtrl},
	}
	for name, keyPress := range keyPresses {
		t.Run(name, func(t *testing.T) {
			root := makeFixture(t)
			testModel := startModel(t, Options{Root: root})
			waitForPrompt(t, testModel)
			testModel.Send(keyPress)
			outcome := finalOutcome(t, testModel)
			if !outcome.Cancelled || !exists(t, filepath.Join(root, "junk", ".DS_Store")) {
				t.Errorf("outcome = %+v", outcome)
			}
		})
	}
}

func TestAssumeYesSkipsPrompt(t *testing.T) {
	root := makeFixture(t)
	outcome := finalOutcome(t, startModel(t, Options{Root: root, AssumeYes: true}))

	if outcome.Summary.Removal == nil || outcome.Summary.Removal.DirsRemoved != 3 {
		t.Fatalf("outcome = %+v", outcome)
	}
	if exists(t, filepath.Join(root, "empty")) {
		t.Error("empty should have been removed")
	}
}

func TestNothingToDo(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	outcome := finalOutcome(t, startModel(t, Options{Root: root}))
	if outcome.Err != nil || outcome.Cancelled || len(outcome.Summary.Candidates) != 0 || outcome.Summary.Removal != nil {
		t.Errorf("outcome = %+v", outcome)
	}
}

func TestBadRootReportsError(t *testing.T) {
	outcome := finalOutcome(t, startModel(t, Options{Root: filepath.Join(t.TempDir(), "missing")}))
	if !errors.Is(outcome.Err, fs.ErrNotExist) {
		t.Errorf("Err = %v, want ErrNotExist", outcome.Err)
	}
}

func TestTruncateLeft(t *testing.T) {
	testCases := []struct {
		text  string
		width int
		want  string
	}{
		{text: "/short", width: 20, want: "/short"},
		{text: "/a/very/long/path", width: 10, want: "…long/path"},
		{text: "/exact", width: 6, want: "/exact"},
		{text: "/anything", width: 0, want: "/anything"},
	}
	for _, testCase := range testCases {
		if got := truncateLeft(testCase.text, testCase.width); got != testCase.want {
			t.Errorf("truncateLeft(%q, %d) = %q, want %q", testCase.text, testCase.width, got, testCase.want)
		}
	}
}
