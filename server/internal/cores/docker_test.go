package cores

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
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
