package sweep

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

const (
	// DefaultJunkThreshold is the probability at or above which a judged file
	// counts as junk. Deleting a real file is worse than keeping clutter, so it
	// sits well above the middle.
	DefaultJunkThreshold = 0.8
	// DefaultJudgeConcurrency caps simultaneous Judge calls.
	DefaultJudgeConcurrency = 8
	// MaxJudgedFiles is the most files a directory may hold for the Judge to
	// be asked about it. Above this the directory is kept: one with that many
	// files is rarely all junk, and the question would be large.
	MaxJudgedFiles = 32
)

// ErrJudgeUnavailable marks a Judge failure that will affect every request,
// such as a rejected API key. Scan stops instead of recording it per directory.
var ErrJudgeUnavailable = errors.New("judge unavailable")

// FileInfo describes one file in a JudgeRequest.
type FileInfo struct {
	Name string
	Size int64
	// ModTime is for cache keys only; judges must not send it anywhere.
	ModTime time.Time
}

// JudgeRequest asks about the files in one directory.
type JudgeRequest struct {
	// Path is the directory's absolute path. Like FileInfo.ModTime, it is for
	// cache keys only.
	Path string
	// Directory is the directory's path relative to the scan root, starting
	// with the root's own name, using forward slashes.
	Directory string
	// Files are the regular files in the directory other than known junk.
	Files []FileInfo
	// EmptySubdirectories names subdirectories already found empty.
	EmptySubdirectories []string
}

// JudgeAnswer is a Judge's reply for one directory.
type JudgeAnswer struct {
	// Probabilities holds, for each of the request's files in order, the
	// probability that the file is junk that can be deleted without losing
	// anything a person would want.
	Probabilities []float64
	// Cached is true when the answer came from a cache, not a fresh call.
	Cached bool
}

// Judge decides whether files are junk.
type Judge interface {
	JunkProbabilities(ctx context.Context, request JudgeRequest) (JudgeAnswer, error)
}

// FileVerdict is the Judge's answer for one file.
type FileVerdict struct {
	Name            string
	Size            int64
	JunkProbability float64
	// Junk is true when JunkProbability reached the threshold.
	Junk bool
}

// Judgement records the Judge's decision about one directory.
type Judgement struct {
	// Path is the absolute path of the directory.
	Path string
	// Files holds a verdict per file, in directory order. Empty when the
	// Judge was not asked.
	Files []FileVerdict
	// Empty is true when every file was judged junk.
	Empty bool
	// Cached is true when the Judge answered from its cache.
	Cached bool
	// Skipped explains why the Judge was not asked, such as too many files.
	Skipped string
	// Err is set when the Judge failed for this directory, which is kept.
	Err error
}

// judgeTree resolves directories holding other files, deepest level first.
// A directory is only judged once all its children are known to be removable,
// so no question is wasted on a directory that would be kept anyway.
func (walker *scanner) judgeTree() error {
	threshold := walker.options.JunkThreshold
	if threshold == 0 {
		threshold = DefaultJunkThreshold
	}
	concurrency := walker.options.JudgeConcurrency
	if concurrency <= 0 {
		concurrency = DefaultJudgeConcurrency
	}

	judging := &judgingState{walker: walker, toJudge: walker.countPossibleJudgements()}
	for depth := len(walker.levels) - 1; depth >= 1; depth-- {
		var pending []*dirNode
		for _, node := range walker.levels[depth] {
			switch {
			case node.blocked || !allRemovable(node.children):
				node.removable = false
			case len(node.otherFiles) == 0:
				node.removable = true
			case len(node.otherFiles) > MaxJudgedFiles:
				node.removable = false
				judging.record(Judgement{
					Path:    node.path,
					Skipped: fmt.Sprintf("%d files is more than the %d sweep asks about", len(node.otherFiles), MaxJudgedFiles),
				})
			default:
				pending = append(pending, node)
			}
		}
		if err := judging.judgeLevel(pending, threshold, concurrency); err != nil {
			return err
		}
	}

	sort.Slice(walker.result.Judgements, func(left, right int) bool {
		return walker.result.Judgements[left].Path < walker.result.Judgements[right].Path
	})
	return nil
}

// countPossibleJudgements counts directories that might need the Judge: not
// blocked and holding a judgeable number of other files. It is an upper bound.
func (walker *scanner) countPossibleJudgements() int {
	total := 0
	for depth := 1; depth < len(walker.levels); depth++ {
		for _, node := range walker.levels[depth] {
			if !node.blocked && len(node.otherFiles) > 0 && len(node.otherFiles) <= MaxJudgedFiles {
				total++
			}
		}
	}
	return total
}

// judgingState serialises results and progress from concurrent Judge calls.
type judgingState struct {
	walker  *scanner
	mutex   sync.Mutex
	judged  int
	toJudge int
}

func (judging *judgingState) record(judgement Judgement) {
	judging.mutex.Lock()
	defer judging.mutex.Unlock()
	judging.walker.result.Judgements = append(judging.walker.result.Judgements, judgement)
	if judgement.Err != nil {
		judging.walker.result.Errors = append(judging.walker.result.Errors, judgement.Err)
	}
}

func (judging *judgingState) reportProgress(dir string) {
	judging.mutex.Lock()
	defer judging.mutex.Unlock()
	judging.judged++
	if progress := judging.walker.options.Progress; progress != nil {
		progress(Progress{
			Phase:       PhaseJudging,
			DirsScanned: judging.walker.result.DirsScanned,
			CurrentDir:  dir,
			DirsJudged:  judging.judged,
			DirsToJudge: judging.toJudge,
		})
	}
}

// judgeLevel asks the Judge about every pending node of one depth at once.
func (judging *judgingState) judgeLevel(pending []*dirNode, threshold float64, concurrency int) error {
	group, groupContext := errgroup.WithContext(judging.walker.ctx)
	group.SetLimit(concurrency)
	for _, node := range pending {
		group.Go(func() error {
			return judging.judgeNode(groupContext, node, threshold)
		})
	}
	return group.Wait()
}

// judgeNode asks about one directory and sets its verdict. Only cancellation
// and ErrJudgeUnavailable are returned; other failures keep the directory.
func (judging *judgingState) judgeNode(ctx context.Context, node *dirNode, threshold float64) error {
	request := judging.walker.buildRequest(node)
	answer, err := judging.walker.options.Judge.JunkProbabilities(ctx, request)
	probabilities := answer.Probabilities
	if err == nil && len(probabilities) != len(request.Files) {
		err = fmt.Errorf("judge returned %d answers for %d files", len(probabilities), len(request.Files))
	}
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(err, ErrJudgeUnavailable):
		return err
	case err != nil:
		node.removable = false
		judging.record(Judgement{Path: node.path, Err: fmt.Errorf("judging %s: %w", node.path, err)})
		judging.reportProgress(node.path)
		return nil
	}

	judgement := Judgement{Path: node.path, Empty: true, Cached: answer.Cached}
	var judgedJunk []JunkFile
	for index, file := range node.otherFiles {
		isJunk := probabilities[index] >= threshold
		judgement.Files = append(judgement.Files, FileVerdict{
			Name:            file.Name,
			Size:            file.Size,
			JunkProbability: probabilities[index],
			Junk:            isJunk,
		})
		if !isJunk {
			judgement.Empty = false
			continue
		}
		file.Judged = true
		judgedJunk = append(judgedJunk, file)
	}

	// Each node is written by exactly one goroutine, and its children were
	// settled in an earlier level, so no lock is needed here.
	node.removable = judgement.Empty
	if judgement.Empty {
		node.judgedJunk = judgedJunk
	}
	judging.record(judgement)
	judging.reportProgress(node.path)
	return nil
}

// buildRequest describes a node for the Judge, relative to the scan root.
func (walker *scanner) buildRequest(node *dirNode) JudgeRequest {
	relativePath, err := filepath.Rel(walker.result.Root, node.path)
	if err != nil {
		relativePath = node.path
	}
	request := JudgeRequest{
		Path:      node.path,
		Directory: filepath.ToSlash(filepath.Join(filepath.Base(walker.result.Root), relativePath)),
	}
	for _, file := range node.otherFiles {
		request.Files = append(request.Files, FileInfo{Name: file.Name, Size: file.Size, ModTime: file.ModTime})
	}
	for _, child := range node.children {
		request.EmptySubdirectories = append(request.EmptySubdirectories, filepath.Base(child.path))
	}
	return request
}
