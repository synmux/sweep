package jevjudge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mattn/go-jev"

	"github.com/synmux/sweep/internal/sweep"
)

func lookupFrom(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, found := values[name]
		return value, found
	}
}

func TestAPIKeyFromEnvironmentOrder(t *testing.T) {
	testCases := []struct {
		name    string
		values  map[string]string
		wantKey string
	}{
		{name: "first wins", values: map[string]string{"TYPESAFE_API_KEY": "one", "JEV_API_KEY": "two", "__TYPESAFE_API_KEY": "three"}, wantKey: "one"},
		{name: "second", values: map[string]string{"JEV_API_KEY": "two", "__TYPESAFE_API_KEY": "three"}, wantKey: "two"},
		{name: "third", values: map[string]string{"__TYPESAFE_API_KEY": "three"}, wantKey: "three"},
		{name: "empty values are skipped", values: map[string]string{"TYPESAFE_API_KEY": "", "JEV_API_KEY": "", "__TYPESAFE_API_KEY": "three"}, wantKey: "three"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			key, err := APIKeyFromEnvironment(lookupFrom(testCase.values))
			if err != nil || key != testCase.wantKey {
				t.Errorf("key = %q, err = %v, want %q", key, err, testCase.wantKey)
			}
		})
	}
	if _, err := APIKeyFromEnvironment(lookupFrom(nil)); !errors.Is(err, ErrNoAPIKey) {
		t.Errorf("no key: err = %v", err)
	}
}

var sampleRequest = sweep.JudgeRequest{
	Directory:           "photos/raw",
	Files:               []sweep.FileInfo{{Name: "Thumbs.db", Size: 12288}, {Name: "notes.txt", Size: 10}},
	EmptySubdirectories: []string{"previews"},
}

func TestBuildEvaluation(t *testing.T) {
	state, questions := BuildEvaluation(sampleRequest)
	if state.Directory != "photos/raw" || len(state.Files) != 2 || state.Files[0] != (StateFile{Name: "Thumbs.db", SizeBytes: 12288}) ||
		!slices.Equal(state.EmptySubdirectories, []string{"previews"}) {
		t.Errorf("state = %+v", state)
	}
	if len(questions) != 2 {
		t.Fatalf("questions = %+v", questions)
	}
	for index := range 2 {
		question := questions[fmt.Sprintf("file_%d", index)]
		if question.Type != "noul" || !strings.Contains(question.Instructions.(string), fmt.Sprintf("`files[%d]`", index)) {
			t.Errorf("question %d = %+v", index, question)
		}
		if question.Criteria == nil {
			t.Errorf("question %d has no criteria", index)
		}
	}

	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"directory":"photos/raw","files":[{"name":"Thumbs.db","size_bytes":12288},{"name":"notes.txt","size_bytes":10}],"empty_subdirectories":["previews"]}`; string(encoded) != want {
		t.Errorf("state JSON = %s", encoded)
	}
	emptyState, _ := BuildEvaluation(sweep.JudgeRequest{Directory: "x", Files: []sweep.FileInfo{{Name: "a"}}})
	if encoded, _ := json.Marshal(emptyState); !strings.Contains(string(encoded), `"empty_subdirectories":[]`) {
		t.Errorf("empty_subdirectories must encode as [] not null: %s", encoded)
	}
}

// fakeServer serves the Jev HTTP API from a handler, recording request bodies.
func fakeServer(t *testing.T, handler func(body map[string]any) (int, string)) (*Judge, *[]map[string]any) {
	t.Helper()
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q", got)
		}
		raw, _ := io.ReadAll(request.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("request body is not JSON: %s", raw)
		}
		bodies = append(bodies, body)
		status, response := handler(body)
		writer.WriteHeader(status)
		_, _ = io.WriteString(writer, response)
	}))
	t.Cleanup(server.Close)
	return New("test-key", jev.WithURL(server.URL), jev.WithMaxRetries(0)), &bodies
}

func TestJunkProbabilitiesParsesAnswers(t *testing.T) {
	judge, bodies := fakeServer(t, func(map[string]any) (int, string) {
		return http.StatusOK, `{"model":"jev-1.13.0","answers":{"file_0":{"type":"noul","noul":0.96},"file_1":{"type":"noul","noul":0.13}},"usage":{"input_tokens":1,"output_tokens":1}}`
	})
	answer, err := judge.JunkProbabilities(context.Background(), sampleRequest)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(answer.Probabilities, []float64{0.96, 0.13}) || answer.Cached {
		t.Errorf("answer = %+v", answer)
	}
	body := (*bodies)[0]
	if body["model"] != jev.DefaultModel {
		t.Errorf("model = %v", body["model"])
	}
	if questions, ok := body["questions"].(map[string]any); !ok || len(questions) != 2 {
		t.Errorf("questions = %v", body["questions"])
	}
}

func TestJunkProbabilitiesRejectsBadAnswers(t *testing.T) {
	responses := map[string]string{
		"missing answer": `{"answers":{"file_0":{"type":"noul","noul":0.9}}}`,
		"out of range":   `{"answers":{"file_0":{"type":"noul","noul":0.9},"file_1":{"type":"noul","noul":1.5}}}`,
		"not json":       `oops`,
	}
	for name, response := range responses {
		t.Run(name, func(t *testing.T) {
			judge, _ := fakeServer(t, func(map[string]any) (int, string) { return http.StatusOK, response })
			_, err := judge.JunkProbabilities(context.Background(), sampleRequest)
			if err == nil || errors.Is(err, sweep.ErrJudgeUnavailable) {
				t.Errorf("err = %v, want a per-directory error", err)
			}
		})
	}
}

func TestJunkProbabilitiesClassifiesHTTPErrors(t *testing.T) {
	testCases := map[int]bool{
		http.StatusUnauthorized:        true,
		http.StatusForbidden:           true,
		http.StatusUnprocessableEntity: true,
		http.StatusInternalServerError: false,
		http.StatusTooManyRequests:     false,
	}
	for status, wantUnavailable := range testCases {
		t.Run(http.StatusText(status), func(t *testing.T) {
			judge, _ := fakeServer(t, func(map[string]any) (int, string) { return status, `{"error":"nope"}` })
			_, err := judge.JunkProbabilities(context.Background(), sampleRequest)
			if err == nil || errors.Is(err, sweep.ErrJudgeUnavailable) != wantUnavailable {
				t.Errorf("err = %v, want unavailable = %v", err, wantUnavailable)
			}
		})
	}
}

func TestJunkProbabilitiesWithNoFilesMakesNoRequest(t *testing.T) {
	judge, bodies := fakeServer(t, func(map[string]any) (int, string) { return http.StatusOK, `{}` })
	answer, err := judge.JunkProbabilities(context.Background(), sweep.JudgeRequest{Directory: "x"})
	if err != nil || len(answer.Probabilities) != 0 || len(*bodies) != 0 {
		t.Errorf("answer %+v, err %v, requests %d", answer, err, len(*bodies))
	}
}

// TestLiveJev calls the real API when a key is configured. It checks the
// calibrated question still separates obvious junk from obvious content.
func TestLiveJev(t *testing.T) {
	if testing.Short() {
		t.Skip("live API test skipped in -short mode")
	}
	apiKey, err := APIKeyFromEnvironment(os.LookupEnv)
	if err != nil {
		t.Skip("no Jev API key configured")
	}
	request := sweep.JudgeRequest{
		Directory: "Photos/2019/raw",
		Files: []sweep.FileInfo{
			{Name: "Thumbs.db", Size: 12288},
			{Name: "desktop.ini", Size: 282},
			{Name: "IMG_0001.JPG", Size: 3481920},
			{Name: "invoice-2024.pdf", Size: 88213},
			{Name: ".gitkeep", Size: 0},
		},
	}
	answer, err := New(apiKey).JunkProbabilities(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	probabilities := answer.Probabilities
	for index, file := range request.Files {
		wantJunk := index < 2
		isJunk := probabilities[index] >= sweep.DefaultJunkThreshold
		t.Logf("%.2f %s", probabilities[index], file.Name)
		if isJunk != wantJunk {
			t.Errorf("%s: probability %.2f, want junk = %v", file.Name, probabilities[index], wantJunk)
		}
	}
}

func TestRequestNeverSendsCacheOnlyFields(t *testing.T) {
	request := sampleRequest
	request.Path = "/Users/someone/secret/photos/raw"
	request.Files = []sweep.FileInfo{{Name: "Thumbs.db", Size: 1, ModTime: time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC)}}
	judge, bodies := fakeServer(t, func(map[string]any) (int, string) {
		return http.StatusOK, `{"answers":{"file_0":{"type":"noul","noul":0.9}}}`
	})
	if _, err := judge.JunkProbabilities(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal((*bodies)[0])
	for _, leaked := range []string{"/Users/someone", "2026"} {
		if strings.Contains(string(encoded), leaked) {
			t.Errorf("request body leaks %q: %s", leaked, encoded)
		}
	}
}

func TestFingerprintIsStable(t *testing.T) {
	first, second := New("key-one").Fingerprint(), New("key-two").Fingerprint()
	if first == "" || first != second {
		t.Errorf("fingerprints %q and %q must match and not depend on the key", first, second)
	}
	if other := New("key", jev.WithModel("jev-other")).Fingerprint(); other == first {
		t.Error("a different model must change the fingerprint")
	}
}
