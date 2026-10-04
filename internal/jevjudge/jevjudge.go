// Package jevjudge implements sweep.Judge with TypeSafe's Jev model. Each file
// becomes one Noul question ("is this junk?") over a state describing the
// whole directory, so Jev sees each file in context and code combines the
// answers. Only names and sizes are sent, never file contents.
package jevjudge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"

	"github.com/mattn/go-jev"

	"github.com/synmux/sweep/internal/sweep"
)

// APIKeyVariables are the environment variables searched for the API key, in
// order.
var APIKeyVariables = []string{"TYPESAFE_API_KEY", "JEV_API_KEY", "__TYPESAFE_API_KEY"}

// ErrNoAPIKey means none of APIKeyVariables holds a key.
var ErrNoAPIKey = errors.New("no API key for --jev: set $TYPESAFE_API_KEY, $JEV_API_KEY or $__TYPESAFE_API_KEY")

// APIKeyFromEnvironment returns the first non-empty key from APIKeyVariables.
// lookup is os.LookupEnv outside tests.
func APIKeyFromEnvironment(lookup func(string) (string, bool)) (string, error) {
	for _, name := range APIKeyVariables {
		if value, found := lookup(name); found && value != "" {
			return value, nil
		}
	}
	return "", ErrNoAPIKey
}

// questionCriteria defines junk for every file question. It was tuned against
// live answers: without it, real documents and .gitkeep score far closer to
// the threshold.
var questionCriteria = map[string]string{
	"true": "The file has no value of its own and is safe to delete: operating-system metadata " +
		"(.DS_Store, Thumbs.db, desktop.ini, ._ AppleDouble files, .localized, Icon\\r), stale editor or " +
		"office lock and temp files, crash dumps, or other leftover clutter.",
	"false": "The file could matter to someone: a document, photo, video, music, source code, configuration, " +
		"data, archive, log someone may want, or note, even if small, empty or oddly named. Includes files " +
		"that exist to keep a directory, such as .gitkeep or .keep.",
}

// instructionsTemplate is the per-file question; %d is the file's index.
const instructionsTemplate = "Is `files[%d]` junk that can be deleted without losing anything a person would want to keep?"

// promptVersion changes whenever the state layout changes, so that cached
// answers to the old prompt are not reused. The template and criteria are
// hashed directly.
const promptVersion = 1

// State is the JSON state sent for one directory.
type State struct {
	Directory           string      `json:"directory"`
	Files               []StateFile `json:"files"`
	EmptySubdirectories []string    `json:"empty_subdirectories"`
}

// StateFile is one file in the State.
type StateFile struct {
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes"`
}

// BuildEvaluation turns a request into Jev state and questions. Question
// "file_N" asks about request.Files[N].
func BuildEvaluation(request sweep.JudgeRequest) (State, map[string]jev.Question) {
	state := State{
		Directory:           request.Directory,
		Files:               make([]StateFile, 0, len(request.Files)),
		EmptySubdirectories: append([]string{}, request.EmptySubdirectories...),
	}
	questions := make(map[string]jev.Question, len(request.Files))
	for index, file := range request.Files {
		state.Files = append(state.Files, StateFile{Name: file.Name, SizeBytes: file.Size})
		questions[questionID(index)] = jev.Question{
			Type:         "noul",
			Instructions: fmt.Sprintf(instructionsTemplate, index),
			Criteria:     questionCriteria,
		}
	}
	return state, questions
}

func questionID(index int) string {
	return "file_" + strconv.Itoa(index)
}

// evaluator is the part of *jev.Client that Judge uses.
type evaluator interface {
	Evaluate(ctx context.Context, state, questions any) (*jev.Response, error)
}

// Judge asks Jev whether files are junk.
type Judge struct {
	client evaluator
	model  string
}

// New returns a Judge using apiKey. Extra options go to jev.NewClient, for
// example jev.WithURL in tests.
func New(apiKey string, options ...jev.ClientOption) *Judge {
	options = append([]jev.ClientOption{jev.WithAPIKey(apiKey)}, options...)
	client := jev.NewClient(options...)
	return &Judge{client: client, model: client.Model()}
}

// NewFromEnvironment returns a Judge with the key from APIKeyFromEnvironment.
func NewFromEnvironment() (*Judge, error) {
	apiKey, err := APIKeyFromEnvironment(os.LookupEnv)
	if err != nil {
		return nil, err
	}
	return New(apiKey), nil
}

// Fingerprint identifies everything that shapes this Judge's answers apart
// from the request: the model and the question wording. Cached answers are
// only reused under the same fingerprint.
func (judge *Judge) Fingerprint() string {
	criteria, _ := json.Marshal(questionCriteria) // map keys marshal sorted, so this is stable
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "jev\x00%d\x00%s\x00%s\x00%s", promptVersion, judge.model, instructionsTemplate, criteria) // hash writes never fail
	return hex.EncodeToString(hash.Sum(nil))
}

// JunkProbabilities implements sweep.Judge with one request per directory.
func (judge *Judge) JunkProbabilities(ctx context.Context, request sweep.JudgeRequest) (sweep.JudgeAnswer, error) {
	if len(request.Files) == 0 {
		return sweep.JudgeAnswer{}, nil
	}
	state, questions := BuildEvaluation(request)
	response, err := judge.client.Evaluate(ctx, state, questions)
	if err != nil {
		return sweep.JudgeAnswer{}, classify(err)
	}

	probabilities := make([]float64, len(request.Files))
	for index := range request.Files {
		answer := response.Answers[questionID(index)]
		if answer == nil {
			return sweep.JudgeAnswer{}, fmt.Errorf("jev returned no answer for %s", request.Files[index].Name)
		}
		if answer.Noul < 0 || answer.Noul > 1 {
			return sweep.JudgeAnswer{}, fmt.Errorf("jev returned probability %v for %s", answer.Noul, request.Files[index].Name)
		}
		probabilities[index] = answer.Noul
	}
	return sweep.JudgeAnswer{Probabilities: probabilities}, nil
}

// classify marks errors that will hit every request (bad key, no access, a
// request the API rejects as malformed) so the scan stops at the first one.
func classify(err error) error {
	var apiError *jev.APIError
	if errors.As(err, &apiError) {
		switch apiError.Status {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusUnprocessableEntity:
			return fmt.Errorf("%w: jev %w", sweep.ErrJudgeUnavailable, err)
		}
	}
	return fmt.Errorf("jev: %w", err)
}
