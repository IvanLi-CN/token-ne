package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The state helpers are the only place this stand-in touches the filesystem, so
// their three shapes are pinned: a wrong dispatch would either delete a state
// directory that must be retained or retain one the contract says is gone.
func TestStateHelperFragments(t *testing.T) {
	root := t.TempDir()
	e := &engine{containers: map[string]*container{}, stateHostDir: root}
	instance := "0193f0c2-7b1a-7c3e-9f00-0000000000aa"
	path := filepath.Join(root, instance)

	code, err := e.runStateHelper([]string{"sh", "-c", "mkdir -p /state/" + instance})
	if err != nil || code != 0 {
		t.Fatalf("mkdir: code=%d err=%v", code, err)
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatalf("mkdir did not create %s: %v", path, err)
	}
	if err = os.WriteFile(filepath.Join(path, "segment"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, err = e.runStateHelper([]string{"sh", "-c", "test -d /state/" + instance})
	if err != nil || code != 0 {
		t.Fatalf("test existing: code=%d err=%v", code, err)
	}
	code, _ = e.runStateHelper([]string{"sh", "-c", "test -d /state/0193f0c2-7b1a-7c3e-9f00-0000000000bb"})
	if code != 1 {
		t.Fatalf("test missing: code=%d", code)
	}

	// A retained directory must survive a stat: if the remove branch ran here the
	// egress lane's nonzero-spool assertion would silently pass for the wrong
	// reason.
	if _, err = os.Stat(path); err != nil {
		t.Fatal("stat removed the directory")
	}

	code, err = e.runStateHelper([]string{"sh", "-c", "rm -rf /state/" + instance})
	if err != nil || code != 0 {
		t.Fatalf("remove: code=%d err=%v", code, err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("remove left %s in place: %v", path, err)
	}
}

func TestStateHelperRejectsUnknownCommands(t *testing.T) {
	e := &engine{containers: map[string]*container{}, stateHostDir: t.TempDir()}
	if _, err := e.runStateHelper([]string{"sh", "-c", "rm -rf /"}); err == nil {
		t.Fatal("accepted a destructive command outside the /state contract")
	}
	if _, err := e.runStateHelper([]string{"rm", "-rf", "/"}); err == nil {
		t.Fatal("accepted a non-shell helper shape")
	}
}

// The Engine list payload uses string State; the egress network check reads the
// same list. Both must decode from one renderer.
func TestContainerListShape(t *testing.T) {
	c := &container{ID: "id1", Name: "/core-x", Image: "img", State: "exited",
		Labels: map[string]string{"com.token-ne.role": "core"}, Networks: []string{"token_ne_egress"}}
	payload := containerList(c)
	if payload["State"] != "exited" {
		t.Fatalf("state: %v", payload["State"])
	}
	names, _ := payload["Names"].([]string)
	if len(names) != 1 || names[0] != "/core-x" {
		t.Fatalf("names: %v", payload["Names"])
	}
}

func TestLabelFilterMatchesAllRequestedLabels(t *testing.T) {
	filter := parseLabelFilter(`{"label":["com.token-ne.role=core","com.token-ne.instance=abc"]}`)
	labels := map[string]string{"com.token-ne.role": "core", "com.token-ne.instance": "abc"}
	if !labelsMatch(labels, filter) {
		t.Fatal("matching labels were rejected")
	}
	if labelsMatch(labels, []string{"com.token-ne.role=core", "com.token-ne.instance=other"}) {
		t.Fatal("a mismatched label value was accepted")
	}
	if labelsMatch(labels, []string{"com.token-ne.deployment=x"}) {
		t.Fatal("a missing label was accepted")
	}
	if !labelsMatch(labels, []string{"com.token-ne.role"}) {
		t.Fatal("a presence-only label filter was rejected")
	}
}

// The control surface must not be reachable through the versioned Engine prefix
// in a way that lets a real client mutate it; it is served only at its own path.
func TestSeedAndCallsEndpoints(t *testing.T) {
	e := &engine{containers: map[string]*container{}, stateHostDir: t.TempDir()}
	mux := http.NewServeMux()
	e.serve(mux)
	seed := httptest.NewRequest(http.MethodPost, "/__fake/containers", strings.NewReader(
		`{"id":"ctr-1","name":"core-1","image":"img","state":"exited","labels":{"com.token-ne.role":"core"}}`))
	seedRec := httptest.NewRecorder()
	mux.ServeHTTP(seedRec, seed)
	if seedRec.Code != 201 {
		t.Fatalf("seed: %d %s", seedRec.Code, seedRec.Body.String())
	}
	calls := httptest.NewRecorder()
	mux.ServeHTTP(calls, httptest.NewRequest(http.MethodGet, "/__fake/containers", nil))
	if !strings.Contains(calls.Body.String(), "ctr-1") {
		t.Fatalf("seeded container missing: %s", calls.Body.String())
	}
}
