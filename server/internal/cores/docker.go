package cores

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	RoleLabel       = "com.token-ne.role"
	VersionLabel    = "com.token-ne.version"
	InstanceLabel   = "com.token-ne.instance"
	DeploymentLabel = "com.token-ne.deployment"
)

type Container struct {
	Health          string            `json:"-"`
	ID              string            `json:"Id"`
	Name            string            `json:"Name"`
	Names           []string          `json:"Names"`
	Image           string            `json:"Image"`
	State           string            `json:"State"`
	Status          string            `json:"Status"`
	Labels          map[string]string `json:"Labels"`
	NetworkSettings struct {
		Networks map[string]struct{ IPAddress string } `json:"Networks"`
	} `json:"NetworkSettings"`
}

// Image is the Docker Engine image inspect subset the control plane needs. The
// registry digest is not part of a local image identity: it is recorded per
// repository when the image was pulled.
type Image struct {
	ID          string   `json:"Id"`
	RepoTags    []string `json:"RepoTags"`
	RepoDigests []string `json:"RepoDigests"`
}

// RepositoryDigest returns the digest Docker recorded for repo, if any.
func (i Image) RepositoryDigest(repo string) (string, bool) {
	for _, entry := range i.RepoDigests {
		if name, digest, ok := strings.Cut(entry, "@"); ok && name == repo && imageDigestPattern.MatchString(digest) {
			return digest, true
		}
	}
	return "", false
}

type ContainerSpec struct {
	Instance, Version, Deployment, Name, Image, Project    string
	Networks                                               []string
	StateHostDir                                           string
	ControlURL, Token, SigningKey, SignatureMode, CacheKey string
	AllowPrivateUpstreams                                  string
	ConfigHash                                             string
}

type Engine struct {
	HTTP *http.Client
	// PullHTTP carries the long pull timeout; HTTP keeps the short deadline used
	// by every other Engine call.
	PullHTTP       *http.Client
	Base           string
	StateInitImage string
}

func NewEngine(dockerHost string) (*Engine, error) {
	if dockerHost == "" {
		dockerHost = "unix:///var/run/docker.sock"
	}
	u, parseErr := url.Parse(dockerHost)
	if parseErr != nil || (u.Scheme != "unix" && u.Scheme != "tcp") || u.Scheme == "unix" && !filepath.IsAbs(u.Path) || u.Scheme == "tcp" && u.Host == "" {
		return nil, ErrInvalid
	}
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		if strings.HasPrefix(dockerHost, "unix://") {
			return (&net.Dialer{}).DialContext(ctx, "unix", strings.TrimPrefix(dockerHost, "unix://"))
		}
		u, e := url.Parse(dockerHost)
		if e != nil || u.Host == "" || u.Scheme != "tcp" {
			return nil, errors.New("DOCKER_HOST must be a Unix socket or TCP socket-proxy URL")
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", u.Host)
	}}
	redirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	base := &http.Client{Transport: tr, Timeout: 15 * time.Second, CheckRedirect: redirect}
	pull := &http.Client{Transport: tr, Timeout: imagePullTimeout, CheckRedirect: redirect}
	return &Engine{HTTP: base, PullHTTP: pull, Base: "http://docker/v1.44", StateInitImage: "busybox:1.37"}, nil
}

// EnsureStateDir creates the per-instance host directory with the core UID.
// Docker creates missing bind-mount directories as root, which the non-root core
// cannot write. A short-lived helper runs with the parent directory mounted.
func (e *Engine) EnsureStateDir(ctx context.Context, hostDir, instance string) error {
	if !filepath.IsAbs(hostDir) || !UUID(instance, false) || e.StateInitImage == "" {
		return ErrInvalid
	}
	name := "token-ne-state-init-" + uuid.NewString()
	spec := struct {
		Image      string            `json:"Image"`
		Cmd        []string          `json:"Cmd"`
		Labels     map[string]string `json:"Labels"`
		HostConfig struct {
			Binds       []string `json:"Binds"`
			NetworkMode string   `json:"NetworkMode"`
			AutoRemove  bool     `json:"AutoRemove"`
		} `json:"HostConfig"`
	}{Image: e.StateInitImage, Cmd: []string{"sh", "-c",
		"mkdir -p /state/" + instance + " && chown 10001:10001 /state/" + instance + " && chmod 700 /state/" + instance},
		Labels: map[string]string{"com.token-ne.role": "state-init"}}
	spec.HostConfig.Binds = []string{filepath.Clean(hostDir) + ":/state"}
	spec.HostConfig.NetworkMode = "none"
	var created Container
	if err := e.call(ctx, http.MethodPost, "/containers/create?name="+url.QueryEscape(name), spec, &created); err != nil {
		return err
	}
	defer e.Remove(context.WithoutCancel(ctx), created.ID, true)
	if err := e.Start(ctx, created.ID); err != nil {
		return err
	}
	var result struct {
		StatusCode int `json:"StatusCode"`
	}
	if err := e.call(ctx, http.MethodPost, "/containers/"+url.PathEscape(created.ID)+"/wait", nil, &result); err != nil {
		return err
	}
	if result.StatusCode != 0 {
		return fmt.Errorf("state directory helper exited with status %d", result.StatusCode)
	}
	return nil
}

func (e *Engine) call(ctx context.Context, method, path string, body any, dst any, allowed ...int) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, e.Base+path, r)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := e.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	okay := res.StatusCode/100 == 2
	for _, status := range allowed {
		if res.StatusCode == status {
			okay = true
		}
	}
	raw, readErr := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if readErr != nil {
		return readErr
	}
	if len(raw) > 1<<20 {
		return errors.New("Docker Engine response too large")
	}
	if !okay {
		return fmt.Errorf("Docker Engine %s %s: HTTP %d", method, path, res.StatusCode)
	}
	if dst != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, dst); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) List(ctx context.Context) ([]Container, error) {
	filter, _ := json.Marshal(map[string][]string{"label": {"com.token-ne.role=core"}})
	var found []Container
	err := e.call(ctx, http.MethodGet, "/containers/json?all=true&filters="+url.QueryEscape(string(filter)), nil, &found)
	return found, err
}

func (e *Engine) Create(ctx context.Context, s ContainerSpec) (Container, error) {
	if !UUID(s.Instance, false) || s.Version == "" || s.Image == "" || s.Name == "" || len(s.Networks) != 2 || !filepath.IsAbs(s.StateHostDir) || s.Project == "" {
		return Container{}, ErrInvalid
	}
	labels := map[string]string{
		RoleLabel: "core", VersionLabel: s.Version, InstanceLabel: s.Instance,
		"com.docker.compose.project": s.Project,
		"com.docker.compose.service": "core-" + slug(s.Version),
		"com.docker.compose.oneoff":  "False",
	}
	if s.Deployment != "" {
		labels[DeploymentLabel] = s.Deployment
	}
	spec := struct {
		Image       string            `json:"Image"`
		Env         []string          `json:"Env"`
		Labels      map[string]string `json:"Labels"`
		Healthcheck map[string]any    `json:"Healthcheck"`
		HostConfig  struct {
			RestartPolicy map[string]string `json:"RestartPolicy"`
			Binds         []string          `json:"Binds"`
			NetworkMode   string            `json:"NetworkMode"`
			PortBindings  map[string]any    `json:"PortBindings"`
			LogConfig     struct {
				Type   string            `json:"Type"`
				Config map[string]string `json:"Config"`
			} `json:"LogConfig"`
		} `json:"HostConfig"`
		NetworkingConfig struct {
			Endpoints map[string]any `json:"EndpointsConfig"`
		} `json:"NetworkingConfig"`
	}{Image: s.Image, Labels: labels}

	spec.HostConfig.RestartPolicy = map[string]string{"Name": "no"}
	spec.HostConfig.Binds = []string{filepath.Join(s.StateHostDir, s.Instance) + ":/var/lib/token-ne/state"}
	spec.HostConfig.NetworkMode = s.Networks[0]
	spec.HostConfig.PortBindings = map[string]any{}
	spec.Healthcheck = map[string]any{"Test": []string{"CMD", "/app/core", "healthcheck"}, "Interval": int64(5e9), "Timeout": int64(2e9), "Retries": 3, "StartPeriod": int64(10e9)}
	spec.HostConfig.LogConfig.Type = "json-file"
	spec.HostConfig.LogConfig.Config = map[string]string{}
	spec.NetworkingConfig.Endpoints = map[string]any{}
	for _, network := range s.Networks {
		spec.NetworkingConfig.Endpoints[network] = map[string]any{}
	}
	env := map[string]string{"CORE_VERSION": s.Version, "CORE_INSTANCE_ID": s.Instance, "CORE_CONTROL_PLANE_URL": s.ControlURL,
		"CORE_CONTROL_TOKEN": s.Token, "CORE_CONFIG_SIGNING_KEY": s.SigningKey, "CORE_CONFIG_SIGNATURE_MODE": s.SignatureMode,
		"CORE_CACHE_KEY": s.CacheKey, "CORE_STATE_DIR": "/var/lib/token-ne/state", "CORE_CONFIG_OVERRIDE": "/var/lib/token-ne/state/config.override.json",
		"CORE_LISTEN": "0.0.0.0:8080", "CORE_METRICS_LISTEN": "0.0.0.0:9091",
		"CORE_ALLOW_PRIVATE_UPSTREAMS": s.AllowPrivateUpstreams}
	for k, v := range env {
		spec.Env = append(spec.Env, k+"="+v)
	}
	sort.Strings(spec.Env)
	hash, err := configHash(spec)
	if err != nil {
		return Container{}, err
	}
	spec.Labels["com.docker.compose.config-hash"] = hash
	var result Container
	path := "/containers/create?name=" + url.QueryEscape(s.Name)
	if err = e.call(ctx, http.MethodPost, path, spec, &result); err != nil {
		return Container{}, err
	}
	return result, nil
}

func configHash(value any) (string, error) {
	b, e := json.Marshal(value)
	if e != nil {
		return "", e
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}
func slug(v string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(v) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func (e *Engine) Start(ctx context.Context, id string) error {
	return e.call(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/start", nil, nil, 304)
}
func (e *Engine) Stop(ctx context.Context, id string, timeout time.Duration) error {
	return e.call(ctx, http.MethodPost, fmt.Sprintf("/containers/%s/stop?t=%d", url.PathEscape(id), int(timeout.Seconds())), nil, nil, 304, 404)
}
func (e *Engine) Remove(ctx context.Context, id string, force bool) error {
	return e.call(ctx, http.MethodDelete, fmt.Sprintf("/containers/%s?force=%t&v=false", url.PathEscape(id), force), nil, nil, 404)
}

func (e *Engine) Inspect(ctx context.Context, id string) (Container, error) {
	var c Container
	err := e.call(ctx, http.MethodGet, "/containers/"+url.PathEscape(id)+"/json", nil, &c)
	return c, err
}

// UnmarshalJSON accepts Docker's list string State and inspect object State.
func (c *Container) UnmarshalJSON(raw []byte) error {
	var payload struct {
		ID              string                             `json:"Id"`
		Name            string                             `json:"Name"`
		Names           []string                           `json:"Names"`
		Image           string                             `json:"Image"`
		Labels          map[string]string                  `json:"Labels"`
		Status          string                             `json:"Status"`
		State           json.RawMessage                    `json:"State"`
		Config          struct{ Labels map[string]string } `json:"Config"`
		NetworkSettings struct {
			Networks map[string]struct{ IPAddress string } `json:"Networks"`
		} `json:"NetworkSettings"`
	}
	if e := json.Unmarshal(raw, &payload); e != nil {
		return e
	}
	c.ID, c.Name, c.Names, c.Image, c.Labels, c.Status = payload.ID, payload.Name, payload.Names, payload.Image, payload.Labels, payload.Status
	if c.Labels == nil {
		c.Labels = payload.Config.Labels
	}
	c.NetworkSettings = payload.NetworkSettings
	if len(payload.State) == 0 || string(payload.State) == "null" {
		return nil
	}
	if len(payload.State) > 0 && payload.State[0] == '"' {
		return json.Unmarshal(payload.State, &c.State)
	}
	var state struct {
		Status string
		Health struct{ Status string }
	}
	if e := json.Unmarshal(payload.State, &state); e != nil {
		return e
	}
	c.State = state.Status
	c.Health = state.Health.Status
	return nil
}
func (e *Engine) Health(ctx context.Context, id string) (string, error) {
	c, err := e.Inspect(ctx, id)
	return c.Health, err
}

// InspectImage resolves a reference against the local engine. A missing image is
// an error, so callers can tell presence from absence.
func (e *Engine) InspectImage(ctx context.Context, ref string) (Image, error) {
	parsed, err := ParseImageReference(ref)
	if err != nil {
		return Image{}, err
	}
	var image Image
	err = e.call(ctx, http.MethodGet, "/images/"+parsed.String()+"/json", nil, &image)
	return image, err
}

// PullImage pulls ref anonymously and returns the inspected image so callers can
// read the resolved registry digest. The control plane intentionally sends no
// registry credentials: private registries are a deployment-host concern.
func (e *Engine) PullImage(ctx context.Context, ref string) (Image, error) {
	parsed, err := ParseImageReference(ref)
	if err != nil {
		return Image{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, imagePullTimeout)
	defer cancel()
	query := url.Values{}
	if parsed.Digest != "" {
		query.Set("fromImage", parsed.Repository+"@"+parsed.Digest)
	} else {
		query.Set("fromImage", parsed.Repository)
		if parsed.Tag != "" {
			query.Set("tag", parsed.Tag)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.Base+"/images/create?"+query.Encode(), nil)
	if err != nil {
		return Image{}, err
	}
	client := e.PullHTTP
	if client == nil {
		client = e.HTTP
	}
	res, err := client.Do(req)
	if err != nil {
		return Image{}, err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		return Image{}, fmt.Errorf("Docker Engine pull %s: HTTP %d", parsed.String(), res.StatusCode)
	}
	if err = consumePull(res.Body); err != nil {
		return Image{}, err
	}
	return e.InspectImage(ctx, parsed.String())
}

// consumePull drains Docker's progress stream and surfaces the first error
// record. Progress lines carry no value for the control plane.
func consumePull(body io.Reader) error {
	decoder := json.NewDecoder(io.LimitReader(body, 8<<20))
	for {
		var record struct {
			Error       string `json:"error"`
			ErrorDetail *struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		if err := decoder.Decode(&record); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if record.Error != "" {
			return fmt.Errorf("image pull failed: %s", record.Error)
		}
		if record.ErrorDetail != nil && record.ErrorDetail.Message != "" {
			return fmt.Errorf("image pull failed: %s", record.ErrorDetail.Message)
		}
	}
}
