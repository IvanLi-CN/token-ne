// Minimal Docker Engine API stand-in for the docker-free egress lane.
//
// The egress proof needs the real control-plane binary to run its real core
// orchestrator (reconcile, auto-prune, state-directory helper) against real
// PostgreSQL without a Docker daemon. This process implements only the calls
// that orchestrator makes, records every call so the lane can assert on the
// attempted operations, and faithfully performs the state-directory helper's
// three filesystem fragments against a host directory.
//
// It is not a Docker emulator: containers are records, images are unknown, and
// every unsupported endpoint answers 404 so a new orchestrator call fails loudly
// instead of silently passing.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// call is one observed Engine API request. The lane asserts on the exact
// operations a prune performed (container removal, helper directory deletion).
type call struct {
	Sequence int    `json:"sequence"`
	At       string `json:"at"`
	Method   string `json:"method"`
	Path     string `json:"path"`
	Action   string `json:"action"`
	Target   string `json:"target,omitempty"`
	Note     string `json:"note,omitempty"`
}

type container struct {
	ID       string
	Name     string
	Image    string
	State    string
	Labels   map[string]string
	Networks []string
	Cmd      []string
	Binds    []string
	ExitCode int
}

type engine struct {
	mu         sync.Mutex
	sequence   int
	nextID     int
	calls      []call
	containers map[string]*container
	order      []string
	// stateHostDir is where the /state bind mount points; the helper's
	// fragments are rewritten onto it so directory effects are real filesystem
	// changes the lane can observe.
	stateHostDir string
}

// The three fragments the orchestrator issues are matched as named alternatives
// so the operand, not a positional group index, identifies the action.
var stateCommand = regexp.MustCompile(`^(?:(?P<mkdir>mkdir -p /state/(?P<mkdir_id>[0-9a-f-]{36})(?: .*)?)|(?P<test>test -d /state/(?P<test_id>[0-9a-f-]{36}))|(?P<remove>rm -rf /state/(?P<remove_id>[0-9a-f-]{36})))$`)

func (e *engine) record(method, path, action, target, note string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.sequence++
	e.calls = append(e.calls, call{Sequence: e.sequence, At: time.Now().UTC().Format(time.RFC3339Nano),
		Method: method, Path: path, Action: action, Target: target, Note: note})
	return e.sequence
}

func (e *engine) newID(nameHint string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.nextID++
	slug := regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(strings.ToLower(nameHint), "-")
	return fmt.Sprintf("%s%04d", slug, e.nextID)
}

func (e *engine) lookup(id string) (*container, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	c, ok := e.containers[id]
	return c, ok
}

// helperPath maps a helper's /state/<uuid> operand onto the host directory.
func (e *engine) helperPath(uuid string) (string, error) {
	if e.stateHostDir == "" {
		return "", errors.New("fake docker started without a state host dir")
	}
	if !filepath.IsAbs(e.stateHostDir) {
		return "", errors.New("state host dir must be absolute")
	}
	return filepath.Join(e.stateHostDir, uuid), nil
}

// runStateHelper performs the one-shot helper container's command. Only the
// three fragments the orchestrator issues are accepted.
func (e *engine) runStateHelper(cmd []string) (int, error) {
	if len(cmd) != 3 || cmd[0] != "sh" || cmd[1] != "-c" {
		return 1, fmt.Errorf("unsupported helper command shape %v", cmd)
	}
	match := stateCommand.FindStringSubmatch(cmd[2])
	if match == nil {
		return 1, fmt.Errorf("unsupported helper command %q", cmd[2])
	}
	group := func(name string) string { return match[stateCommand.SubexpIndex(name)] }
	switch {
	case group("mkdir") != "":
		path, err := e.helperPath(group("mkdir_id"))
		if err != nil {
			return 1, err
		}
		if err = os.MkdirAll(path, 0o700); err != nil {
			return 1, err
		}
		return 0, nil
	case group("test") != "":
		path, err := e.helperPath(group("test_id"))
		if err != nil {
			return 1, err
		}
		if _, err = os.Stat(path); err != nil {
			// Same semantics as `test -d`: a missing directory is exit 1.
			return 1, nil
		}
		return 0, nil
	case group("remove") != "":
		path, err := e.helperPath(group("remove_id"))
		if err != nil {
			return 1, err
		}
		if err = os.RemoveAll(path); err != nil {
			return 1, err
		}
		return 0, nil
	default:
		return 1, fmt.Errorf("unsupported helper command %q", cmd[2])
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeText(w http.ResponseWriter, status int, text string) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, text)
}

// containerList renders the Engine list payload shape the orchestrator decodes:
// string State, Names array, Labels and NetworkSettings.Networks.
func containerList(c *container) map[string]any {
	names := []string{"/" + strings.TrimPrefix(c.Name, "/")}
	networks := map[string]any{}
	for _, name := range c.Networks {
		networks[name] = map[string]any{"IPAddress": "127.0.0.1"}
	}
	status := "Exited (0)"
	if c.State == "running" {
		status = "Up 1 second"
	}
	return map[string]any{
		"Id": c.ID, "Names": names, "Image": c.Image, "State": c.State, "Status": status,
		"Labels": c.Labels, "NetworkSettings": map[string]any{"Networks": networks},
	}
}

// containerInspect renders the inspect shape with an object State.
func containerInspect(c *container) map[string]any {
	payload := containerList(c)
	payload["Name"] = "/" + strings.TrimPrefix(c.Name, "/")
	payload["Config"] = map[string]any{"Labels": c.Labels, "Cmd": c.Cmd, "Image": c.Image}
	payload["State"] = map[string]any{"Status": c.State, "Running": c.State == "running",
		"ExitCode": c.ExitCode, "Health": map[string]any{"Status": "healthy"}}
	return payload
}

func (e *engine) serve(mux *http.ServeMux) {
	mux.HandleFunc("GET /containers/json", func(w http.ResponseWriter, r *http.Request) {
		labels := parseLabelFilter(r.URL.Query().Get("filters"))
		e.record(http.MethodGet, r.URL.RequestURI(), "containers.list", "", r.URL.Query().Get("filters"))
		e.mu.Lock()
		out := []map[string]any{}
		for _, id := range e.order {
			c := e.containers[id]
			if !labelsMatch(c.Labels, labels) {
				continue
			}
			out = append(out, containerList(c))
		}
		e.mu.Unlock()
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("POST /containers/create", func(w http.ResponseWriter, r *http.Request) {
		var spec struct {
			Image      string            `json:"Image"`
			Cmd        []string          `json:"Cmd"`
			Env        []string          `json:"Env"`
			Labels     map[string]string `json:"Labels"`
			HostConfig struct {
				Binds       []string `json:"Binds"`
				NetworkMode string   `json:"NetworkMode"`
			} `json:"HostConfig"`
			NetworkingConfig struct {
				Endpoints map[string]any `json:"EndpointsConfig"`
			} `json:"NetworkingConfig"`
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil || json.Unmarshal(body, &spec) != nil {
			writeJSON(w, 400, map[string]any{"message": "invalid create payload"})
			return
		}
		name := r.URL.Query().Get("name")
		id := e.newID(name)
		c := &container{ID: id, Name: name, Image: spec.Image, State: "created", Labels: spec.Labels,
			Cmd: spec.Cmd, Binds: spec.HostConfig.Binds}
		if spec.HostConfig.NetworkMode != "" {
			c.Networks = append(c.Networks, spec.HostConfig.NetworkMode)
		}
		for network := range spec.NetworkingConfig.Endpoints {
			c.Networks = append(c.Networks, network)
		}
		e.mu.Lock()
		e.containers[id] = c
		e.order = append(e.order, id)
		e.mu.Unlock()
		e.record(http.MethodPost, r.URL.RequestURI(), "containers.create", id, name)
		writeJSON(w, 201, map[string]any{"Id": id, "Warnings": []string{}})
	})
	mux.HandleFunc("POST /containers/{id}/start", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		c, ok := e.lookup(id)
		if !ok {
			writeJSON(w, 404, map[string]any{"message": "no such container"})
			return
		}
		if len(c.Cmd) == 3 && c.Cmd[1] == "-c" {
			code, err := e.runStateHelper(c.Cmd)
			c.ExitCode = code
			// The exit status is part of the evidence: a helper that returned
			// non-zero is why a directory survived, and the lane must be able to
			// tell that from "the orchestrator never asked".
			note := fmt.Sprintf("exit=%d %s", code, c.Cmd[2])
			if err != nil {
				note = fmt.Sprintf("exit=%d error=%s %s", code, err, c.Cmd[2])
			}
			e.record(http.MethodPost, r.URL.RequestURI(), "state_helper.run", id, note)
			w.WriteHeader(204)
			return
		}
		c.State = "running"
		e.record(http.MethodPost, r.URL.RequestURI(), "containers.start", id, c.Name)
		w.WriteHeader(204)
	})
	mux.HandleFunc("POST /containers/{id}/stop", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		c, ok := e.lookup(id)
		if !ok {
			writeJSON(w, 404, map[string]any{"message": "no such container"})
			return
		}
		c.State = "exited"
		e.record(http.MethodPost, r.URL.RequestURI(), "containers.stop", id, c.Name)
		w.WriteHeader(204)
	})
	mux.HandleFunc("POST /containers/{id}/wait", func(w http.ResponseWriter, r *http.Request) {
		c, ok := e.lookup(r.PathValue("id"))
		if !ok {
			writeJSON(w, 404, map[string]any{"message": "no such container"})
			return
		}
		writeJSON(w, 200, map[string]any{"StatusCode": c.ExitCode})
	})
	mux.HandleFunc("DELETE /containers/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		e.mu.Lock()
		_, ok := e.containers[id]
		if ok {
			delete(e.containers, id)
			for i, kept := range e.order {
				if kept == id {
					e.order = append(e.order[:i], e.order[i+1:]...)
					break
				}
			}
		}
		e.mu.Unlock()
		if !ok {
			// The orchestrator treats 404 as already-removed.
			writeJSON(w, 404, map[string]any{"message": "no such container"})
			return
		}
		e.record(http.MethodDelete, r.URL.RequestURI(), "containers.remove", id, r.URL.Query().Get("force"))
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET /containers/{id}/json", func(w http.ResponseWriter, r *http.Request) {
		c, ok := e.lookup(r.PathValue("id"))
		if !ok {
			writeJSON(w, 404, map[string]any{"message": "no such container"})
			return
		}
		e.record(http.MethodGet, r.URL.RequestURI(), "containers.inspect", c.ID, c.State)
		writeJSON(w, 200, containerInspect(c))
	})

	// Control surface for the lane. It lives outside /v1.44 so a real engine
	// client can never reach it.
	mux.HandleFunc("GET /__fake/calls", func(w http.ResponseWriter, _ *http.Request) {
		e.mu.Lock()
		defer e.mu.Unlock()
		writeJSON(w, 200, map[string]any{"calls": append([]call(nil), e.calls...)})
	})
	mux.HandleFunc("GET /__fake/containers", func(w http.ResponseWriter, _ *http.Request) {
		e.mu.Lock()
		defer e.mu.Unlock()
		out := []map[string]any{}
		for _, id := range e.order {
			out = append(out, containerInspect(e.containers[id]))
		}
		writeJSON(w, 200, map[string]any{"containers": out})
	})
	mux.HandleFunc("POST /__fake/containers", func(w http.ResponseWriter, r *http.Request) {
		// Seeds a container the orchestrator must adopt, so reconcile sees a
		// steady state instead of inventing one.
		var spec struct {
			ID       string            `json:"id"`
			Name     string            `json:"name"`
			Image    string            `json:"image"`
			State    string            `json:"state"`
			Labels   map[string]string `json:"labels"`
			Networks []string          `json:"networks"`
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil || json.Unmarshal(body, &spec) != nil || spec.ID == "" {
			writeJSON(w, 400, map[string]any{"message": "invalid seed payload"})
			return
		}
		if spec.State == "" {
			spec.State = "running"
		}
		e.mu.Lock()
		if _, exists := e.containers[spec.ID]; !exists {
			e.order = append(e.order, spec.ID)
		}
		e.containers[spec.ID] = &container{ID: spec.ID, Name: spec.Name, Image: spec.Image,
			State: spec.State, Labels: spec.Labels, Networks: spec.Networks}
		e.mu.Unlock()
		writeJSON(w, 201, map[string]any{"id": spec.ID})
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		e.record(r.Method, r.URL.RequestURI(), "unsupported", "", "")
		writeJSON(w, 404, map[string]any{"message": "fake engine does not implement " + r.URL.Path})
	})
}

// parseLabelFilter decodes Docker's JSON filter map and returns the label list.
func parseLabelFilter(raw string) []string {
	if raw == "" {
		return nil
	}
	var filters map[string][]string
	if json.Unmarshal([]byte(raw), &filters) != nil {
		return nil
	}
	return filters["label"]
}

func labelsMatch(labels map[string]string, wanted []string) bool {
	if len(wanted) == 0 {
		return true
	}
	for _, want := range wanted {
		name, value, hasValue := strings.Cut(want, "=")
		actual, present := labels[name]
		if !present || (hasValue && actual != value) {
			return false
		}
	}
	return true
}

// listen binds either a TCP address or "unix:///abs/path". The Unix socket is
// the shape the control plane's network check supports end to end; the Engine
// style client accepts both.
func listen(raw string) (net.Listener, error) {
	if path, ok := strings.CutPrefix(raw, "unix://"); ok {
		_ = os.Remove(path)
		return net.Listen("unix", path)
	}
	return net.Listen("tcp", raw)
}

func main() {
	address := strings.TrimSpace(os.Getenv("FAKE_DOCKER_LISTEN"))
	if address == "" {
		address = "127.0.0.1:23750"
	}
	e := &engine{containers: map[string]*container{}, stateHostDir: strings.TrimSpace(os.Getenv("FAKE_DOCKER_STATE_HOST_DIR"))}
	mux := http.NewServeMux()
	e.serve(mux)
	listener, err := listen(address)
	if err != nil {
		slog.Error("fake docker failed to listen", "address", address, "error", err)
		os.Exit(1)
	}
	slog.Info("fake docker engine listening", "address", address, "state_host_dir", e.stateHostDir)
	// Both shapes hit this process: the Engine prefixes /v1.44 and the egress
	// network check dials plain /containers/json.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/v1.44")
		if r.URL.Path == "" {
			r.URL.Path = "/"
		}
		mux.ServeHTTP(w, r)
	})
	if err := http.Serve(listener, handler); err != nil {
		slog.Error("fake docker stopped", "error", err)
		os.Exit(1)
	}
}
