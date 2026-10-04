package cli

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

// runPlainCommand runs the command non-interactively and returns its output.
func runPlainCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()
	command := NewCommand(func() bool { return false })
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs(args)
	err := command.ExecuteContext(context.Background())
	return output.String(), err
}

func pathExists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	return err == nil
}

func TestPlainRefusesWithoutConfirmation(t *testing.T) {
	root := makeFixture(t)
	_, err := runPlainCommand(t, root)
	if !errors.Is(err, ErrNeedsConfirmation) {
		t.Fatalf("err = %v, want ErrNeedsConfirmation", err)
	}
	if !pathExists(t, filepath.Join(root, "empty", "nested")) {
		t.Error("nothing should be removed without confirmation")
	}
}

func TestPlainDryRun(t *testing.T) {
	root := makeFixture(t)
	output, err := runPlainCommand(t, "--dry-run", root)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"DRY RUN", "Would remove", "3 directories", "✗ empty/", "✗ junk/"} {
		if !strings.Contains(output, fragment) {
			t.Errorf("missing %q in:\n%s", fragment, output)
		}
	}
	if strings.Contains(output, "\x1b[") {
		t.Error("plain output to a non-terminal should carry no ANSI escapes")
	}
	if !pathExists(t, filepath.Join(root, "junk", ".DS_Store")) {
		t.Error("dry run removed something")
	}
}

func TestPlainYesRemoves(t *testing.T) {
	root := makeFixture(t)
	output, err := runPlainCommand(t, "-y", root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "Removed") || !strings.Contains(output, "3 directories") {
		t.Errorf("unexpected output:\n%s", output)
	}
	if pathExists(t, filepath.Join(root, "empty")) || pathExists(t, filepath.Join(root, "junk")) {
		t.Error("empty directories should be gone")
	}
	if !pathExists(t, filepath.Join(root, "kept", "file.txt")) {
		t.Error("kept/file.txt must survive")
	}
}

func TestDefaultsToCurrentDirectory(t *testing.T) {
	root := makeFixture(t)
	t.Chdir(root)
	output, err := runPlainCommand(t, "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, resolvedRoot) && !strings.Contains(output, root) {
		t.Errorf("output should name the current directory %s:\n%s", root, output)
	}
}

func TestRejectsBadArguments(t *testing.T) {
	root := makeFixture(t)
	if _, err := runPlainCommand(t, "-n", filepath.Join(root, "missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing dir: err = %v", err)
	}
	if _, err := runPlainCommand(t, "-n", filepath.Join(root, "kept", "file.txt")); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("file: err = %v", err)
	}
	if _, err := runPlainCommand(t, "-n", root, root); err == nil {
		t.Error("two arguments should be rejected")
	}
}

func TestProblemsFailTheRun(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read everything")
	}
	root := makeFixture(t)
	locked := filepath.Join(root, "locked")
	if err := os.Mkdir(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	output, err := runPlainCommand(t, "--yes", root)
	if err == nil || !strings.Contains(err.Error(), "1 problem") {
		t.Errorf("err = %v, want a problem count", err)
	}
	if !strings.Contains(output, "permission denied") {
		t.Errorf("report should list the problem:\n%s", output)
	}
	if pathExists(t, filepath.Join(root, "empty")) {
		t.Error("other empty directories should still be removed")
	}
}

func TestExitCode(t *testing.T) {
	testCases := map[error]int{
		nil:                  0,
		ErrCancelled:         130,
		ErrNeedsConfirmation: 1,
		errors.New("other"):  1,
	}
	for err, want := range testCases {
		if got := ExitCode(err); got != want {
			t.Errorf("ExitCode(%v) = %d, want %d", err, got, want)
		}
	}
}
