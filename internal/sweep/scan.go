// Package sweep finds and removes empty directories below a root.
//
// A directory is empty when it holds nothing at all, or nothing but junk files
// and directories that are themselves empty. Junk is always .DS_Store; with a
// Judge, other regular files can be judged junk too. The root is never treated
// as a candidate for removal.
package sweep

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// junkFiles names the regular files that are always junk, whatever a Judge
// would say. They are deleted along with the directory.
var junkFiles = map[string]struct{}{
	".DS_Store": {},
}

// IsJunk reports whether a regular file with this name is always junk.
func IsJunk(name string) bool {
	_, found := junkFiles[name]
	return found
}

// JunkFile is a file that is deleted along with its directory.
type JunkFile struct {
	Name    string
	Size    int64
	ModTime time.Time
	// Judged is true when a Judge decided the file is junk, rather than its
	// name. Judged files are only deleted if they are unchanged since the scan.
	Judged bool
}

// Candidate is a directory that Scan found empty.
type Candidate struct {
	// Path is the absolute path of the directory.
	Path string
	// JunkFiles are deleted before the directory.
	JunkFiles []JunkFile
}

// JudgedFileCount returns how many of the candidate's junk files a Judge chose.
func (candidate Candidate) JudgedFileCount() int {
	total := 0
	for _, junkFile := range candidate.JunkFiles {
		if junkFile.Judged {
			total++
		}
	}
	return total
}

// Result is the outcome of a scan.
type Result struct {
	// Root is the absolute, cleaned root directory.
	Root string
	// Candidates are ordered so every directory follows all of its descendants,
	// which is the order they must be removed in.
	Candidates []Candidate
	// DirsScanned counts every directory visited, including the root.
	DirsScanned int
	// Judgements records every directory a Judge was asked about, or would
	// have been asked about, ordered by path. Empty without a Judge.
	Judgements []Judgement
	// Errors holds per-directory read and judging failures. Those
	// directories count as non-empty, so they and their ancestors are kept.
	Errors []error
}

// JunkFileCount returns the number of junk files inside all candidates.
func (result Result) JunkFileCount() int {
	total := 0
	for _, candidate := range result.Candidates {
		total += len(candidate.JunkFiles)
	}
	return total
}

// JudgedFileCount returns how many junk files inside candidates a Judge chose.
func (result Result) JudgedFileCount() int {
	total := 0
	for _, candidate := range result.Candidates {
		total += candidate.JudgedFileCount()
	}
	return total
}

// Phase says what a running scan is doing.
type Phase int

const (
	// PhaseWalking is the walk over the directory tree.
	PhaseWalking Phase = iota
	// PhaseJudging is asking the Judge about directories holding other files.
	PhaseJudging
)

// Progress is a snapshot of a running scan.
type Progress struct {
	Phase Phase
	// DirsScanned counts directories entered so far, including the current one.
	DirsScanned int
	// CandidatesFound counts directories found empty without judging so far.
	CandidatesFound int
	// CurrentDir is the directory being entered, or the last one judged.
	CurrentDir string
	// DirsJudged counts directories the Judge has answered for.
	DirsJudged int
	// DirsToJudge is the most directories the Judge may be asked about. Fewer
	// are asked when a directory's children turn out not to be empty.
	DirsToJudge int
}

// ProgressFunc receives progress snapshots. Calls never overlap.
type ProgressFunc func(progress Progress)

// Options configure a scan. The zero value scans without a Judge.
type Options struct {
	Progress ProgressFunc
	// Judge decides whether other regular files are junk. Nil means only
	// .DS_Store counts as junk.
	Judge Judge
	// JunkThreshold is the probability at or above which a judged file is
	// junk. Zero means DefaultJunkThreshold.
	JunkThreshold float64
	// JudgeConcurrency caps simultaneous Judge calls. Zero means
	// DefaultJudgeConcurrency.
	JudgeConcurrency int
}

// dirNode is one directory in the walked tree.
type dirNode struct {
	path     string
	depth    int
	children []*dirNode
	// knownJunk holds files that are always junk, such as .DS_Store.
	knownJunk []JunkFile
	// otherFiles holds other regular files; only a Judge can clear them.
	otherFiles []JunkFile
	// blocked is set by content that can never be junk: symlinks, devices,
	// sockets, or the directory being unreadable.
	blocked bool
	// removable is the directory's final verdict.
	removable bool
	// judgedJunk holds the otherFiles a Judge decided are junk.
	judgedJunk []JunkFile
}

// Scan walks root and returns the directories that can be removed. Symbolic
// links are never followed and count as content. It returns an error only when
// root is not a usable directory, ctx is cancelled, or the Judge reports
// ErrJudgeUnavailable.
func Scan(ctx context.Context, root string, options Options) (Result, error) {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return Result{}, fmt.Errorf("resolving %q: %w", root, err)
	}
	info, err := os.Stat(absoluteRoot)
	if err != nil {
		return Result{}, err
	}
	if !info.IsDir() {
		return Result{}, fmt.Errorf("%s is not a directory", absoluteRoot)
	}

	walker := &scanner{ctx: ctx, options: options, result: Result{Root: absoluteRoot}}
	rootNode, err := walker.walk(absoluteRoot, 0)
	if err != nil {
		return walker.result, err
	}
	if options.Judge != nil {
		if err := walker.judgeTree(); err != nil {
			return walker.result, err
		}
	}
	walker.collectCandidates(rootNode)
	return walker.result, nil
}

type scanner struct {
	ctx     context.Context
	options Options
	result  Result
	// levels holds the nodes at each depth, for judging deepest first.
	levels [][]*dirNode
	// candidatesWithoutJudging counts walk-time removable directories, for progress.
	candidatesWithoutJudging int
}

// walk builds the node for dir and everything below it. The only error it
// returns is context cancellation.
func (walker *scanner) walk(dir string, depth int) (*dirNode, error) {
	if err := walker.ctx.Err(); err != nil {
		return nil, err
	}
	walker.result.DirsScanned++
	if walker.options.Progress != nil {
		walker.options.Progress(Progress{
			Phase:           PhaseWalking,
			DirsScanned:     walker.result.DirsScanned,
			CandidatesFound: walker.candidatesWithoutJudging,
			CurrentDir:      dir,
		})
	}

	node := &dirNode{path: dir, depth: depth}
	for len(walker.levels) <= depth {
		walker.levels = append(walker.levels, nil)
	}
	walker.levels[depth] = append(walker.levels[depth], node)

	entries, err := os.ReadDir(dir)
	if err != nil {
		walker.result.Errors = append(walker.result.Errors, err)
		node.blocked = true
		return node, nil
	}

	// Every child directory is visited even once dir is known to be kept, so
	// that empty directories nested anywhere below are still found.
	for _, entry := range entries {
		switch {
		case entry.IsDir():
			child, err := walker.walk(filepath.Join(dir, entry.Name()), depth+1)
			if err != nil {
				return nil, err
			}
			node.children = append(node.children, child)
		case entry.Type().IsRegular():
			walker.addFile(node, entry)
		default:
			node.blocked = true
		}
	}

	node.removable = !node.blocked && len(node.otherFiles) == 0 && allRemovable(node.children)
	if node.removable && depth > 0 {
		walker.candidatesWithoutJudging++
	}
	return node, nil
}

// addFile files a regular file under known junk or other files. Size and
// modification time are only read when they will be used.
func (walker *scanner) addFile(node *dirNode, entry fs.DirEntry) {
	isKnownJunk := IsJunk(entry.Name())
	if !isKnownJunk && walker.options.Judge == nil {
		node.otherFiles = append(node.otherFiles, JunkFile{Name: entry.Name()})
		return
	}
	info, err := entry.Info()
	if err != nil {
		// The file vanished or cannot be inspected; treat it as content.
		walker.result.Errors = append(walker.result.Errors, err)
		node.blocked = true
		return
	}
	file := JunkFile{Name: entry.Name(), Size: info.Size(), ModTime: info.ModTime()}
	if isKnownJunk {
		node.knownJunk = append(node.knownJunk, file)
	} else {
		node.otherFiles = append(node.otherFiles, file)
	}
}

func allRemovable(nodes []*dirNode) bool {
	for _, node := range nodes {
		if !node.removable {
			return false
		}
	}
	return true
}

// collectCandidates appends removable directories post-order, so children
// always precede their parents. The root is never a candidate.
func (walker *scanner) collectCandidates(node *dirNode) {
	for _, child := range node.children {
		walker.collectCandidates(child)
	}
	if node.removable && node.depth > 0 {
		junk := append(append([]JunkFile{}, node.knownJunk...), node.judgedJunk...)
		walker.result.Candidates = append(walker.result.Candidates, Candidate{Path: node.path, JunkFiles: junk})
	}
}
