package cores

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDockerManagedContainerContract(t *testing.T) {
	var request map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if e := json.NewDecoder(r.Body).Decode(&request); e != nil {
			t.Error(e)
		}
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"Id":"new"}`))
	}))
	defer srv.Close()
	engine := &Engine{HTTP: srv.Client(), Base: srv.URL}
	spec := ContainerSpec{Instance: uuid.NewString(), Version: "0.2.0", Name: "core-new", Image: "core@sha256:abc", Project: "project", Networks: []string{"edge", "core-control"}, StateHostDir: "/srv/core", ControlURL: "http://api:8080", Token: "secret"}
	if _, e := engine.Create(context.Background(), spec); e != nil {
		t.Fatal(e)
	}
	host := request["HostConfig"].(map[string]any)
	if host["RestartPolicy"].(map[string]any)["Name"] != "no" || len(host["PortBindings"].(map[string]any)) != 0 {
		t.Fatal(host)
	}
	if request["Healthcheck"] == nil || host["Healthcheck"] != nil {
		t.Fatal("healthcheck in wrong Docker schema location")
	}
	labels := request["Labels"].(map[string]any)
	for _, key := range []string{RoleLabel, VersionLabel, InstanceLabel, "com.docker.compose.project", "com.docker.compose.service", "com.docker.compose.oneoff", "com.docker.compose.config-hash"} {
		if labels[key] == nil {
			t.Fatal("missing label", key)
		}
	}
	hash := labels["com.docker.compose.config-hash"]
	spec.Token = "rotated"
	if _, e := engine.Create(context.Background(), spec); e != nil {
		t.Fatal(e)
	}
	if request["Labels"].(map[string]any)["com.docker.compose.config-hash"] == hash {
		t.Fatal("hash ignored runtime configuration")
	}
	networks := request["NetworkingConfig"].(map[string]any)["EndpointsConfig"].(map[string]any)
	if len(networks) != 2 {
		t.Fatal(networks)
	}
}
func TestDockerInspectStateAndHealth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"Id":"a","Name":"/core","State":{"Status":"running","Health":{"Status":"healthy"}},"NetworkSettings":{"Networks":{}}}`))
	}))
	defer srv.Close()
	engine := Engine{HTTP: srv.Client(), Base: srv.URL}
	c, e := engine.Inspect(context.Background(), "a")
	if e != nil || c.State != "running" || c.Health != "healthy" {
		t.Fatalf("inspect: %#v %v", c, e)
	}
}
func TestDockerHostEmptyUsesUnixDefault(t *testing.T) {
	engine, e := NewEngine("")
	if e != nil || engine.HTTP == nil || engine.Base != "http://docker/v1.44" {
		t.Fatalf("default Docker host: %v", e)
	}
	for _, host := range []string{"https://docker:2376", "tcp://", "unix://relative", "invalid"} {
		if _, e := NewEngine(host); e == nil {
			t.Fatal("invalid host accepted", host)
		}
	}
}

func TestEnsureStateDirRunsHelper(t *testing.T) {
	var create map[string]any
	wait := false
	removed := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/containers/create"):
			if e := json.NewDecoder(r.Body).Decode(&create); e != nil {
				t.Error(e)
			}
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"Id":"helper"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/containers/helper/start":
			w.WriteHeader(204)
		case r.Method == http.MethodPost && r.URL.Path == "/containers/helper/wait":
			wait = true
			_, _ = w.Write([]byte(`{"StatusCode":0}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/containers/helper":
			removed = true
			w.WriteHeader(204)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	engine := Engine{HTTP: srv.Client(), Base: srv.URL, StateInitImage: "busybox:1.37"}
	id := uuid.NewString()
	if e := engine.EnsureStateDir(context.Background(), "/srv/cores", id); e != nil {
		t.Fatal(e)
	}
	if !wait || !removed {
		t.Fatalf("helper lifecycle incomplete: wait=%t removed=%t", wait, removed)
	}
	host := create["HostConfig"].(map[string]any)
	binds := host["Binds"].([]any)
	if len(binds) != 1 || binds[0] != "/srv/cores:/state" || host["NetworkMode"] != "none" {
		t.Fatalf("unsafe helper mount: %#v", host)
	}
	cmd := create["Cmd"].([]any)
	if !strings.Contains(cmd[2].(string), id) || !strings.Contains(cmd[2].(string), "chown 10001:10001") {
		t.Fatalf("helper command: %#v", cmd)
	}
}

func TestEnsureStateDirRejectsUnsafeInvocation(t *testing.T) {
	engine := &Engine{HTTP: http.DefaultClient, Base: "http://127.0.0.1:1", StateInitImage: "busybox:1.37"}
	for _, tc := range []struct{ hostDir, instance string }{
		{"relative/state", uuid.NewString()},
		{"/srv/cores", "not-a-uuid"},
		{"/srv/cores", ""},
		{"", uuid.NewString()},
	} {
		if e := engine.EnsureStateDir(context.Background(), tc.hostDir, tc.instance); !errors.Is(e, ErrInvalid) {
			t.Fatalf("guard %q %q: %v", tc.hostDir, tc.instance, e)
		}
	}
	engine.StateInitImage = ""
	if e := engine.EnsureStateDir(context.Background(), "/srv/cores", uuid.NewString()); !errors.Is(e, ErrInvalid) {
		t.Fatalf("missing helper image: %v", e)
	}
}

func TestEnsureStateDirPropagatesHelperFailure(t *testing.T) {
	createFailure := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer createFailure.Close()
	engine := &Engine{HTTP: createFailure.Client(), Base: createFailure.URL, StateInitImage: "busybox:1.37"}
	if e := engine.EnsureStateDir(context.Background(), "/srv/cores", uuid.NewString()); e == nil {
		t.Fatal("container create failure ignored")
	}

	helperFailure := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/containers/create"):
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"Id":"helper"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/containers/helper/start":
			w.WriteHeader(204)
		case r.Method == http.MethodPost && r.URL.Path == "/containers/helper/wait":
			_, _ = w.Write([]byte(`{"StatusCode":1}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/containers/helper":
			w.WriteHeader(204)
		default:
			http.NotFound(w, r)
		}
	}))
	defer helperFailure.Close()
	engine = &Engine{HTTP: helperFailure.Client(), Base: helperFailure.URL, StateInitImage: "busybox:1.37"}
	e := engine.EnsureStateDir(context.Background(), "/srv/cores", uuid.NewString())
	if e == nil || !strings.Contains(e.Error(), "exited with status 1") {
		t.Fatalf("helper exit status ignored: %v", e)
	}
}
