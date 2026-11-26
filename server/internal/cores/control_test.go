package cores

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/IvanLi-CN/token-ne/server/internal/caddy"
	"github.com/IvanLi-CN/token-ne/server/internal/metrics"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type fakeRow struct {
	values []any
	err    error
}

func (r fakeRow) Scan(dst ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dst) != len(r.values) {
		return errors.New("scan field count mismatch")
	}
	for n, v := range r.values {
		d := reflect.ValueOf(dst[n]).Elem()
		if v == nil {
			d.SetZero()
			continue
		}
		x := reflect.ValueOf(v)
		if x.Type().AssignableTo(d.Type()) {
			d.Set(x)
			continue
		}
		raw, e := json.Marshal(v)
		if e != nil {
			return e
		}
		if e = json.Unmarshal(raw, dst[n]); e != nil {
			return e
		}
	}
	return nil
}

type fakeRows struct {
	pgx.Rows
	data  [][]any
	index int
}

func (r *fakeRows) Next() bool            { r.index++; return r.index <= len(r.data) }
func (r *fakeRows) Scan(dst ...any) error { return (fakeRow{values: r.data[r.index-1]}).Scan(dst...) }
func (r *fakeRows) Close()                {}
func (r *fakeRows) Err() error            { return nil }

type fakeDB struct {
	row    func(string, []any) fakeRow
	rows   func(string, []any) [][]any
	exec   func(string, []any) error
	writes []string
	// args collects every Exec argument tuple so tests can assert on the values
	// a statement persisted, not only on its SQL text.
	args []any
}

func (d *fakeDB) Begin(context.Context) (pgx.Tx, error) { return &fakeTx{db: d}, nil }
func (d *fakeDB) QueryRow(_ context.Context, q string, args ...any) pgx.Row {
	if d.row != nil {
		return d.row(q, args)
	}
	return fakeRow{err: pgx.ErrNoRows}
}
func (d *fakeDB) Query(_ context.Context, q string, args ...any) (pgx.Rows, error) {
	var data [][]any
	if d.rows != nil {
		data = d.rows(q, args)
	}
	return &fakeRows{data: data}, nil
}
func (d *fakeDB) Exec(_ context.Context, q string, args ...any) (pgconn.CommandTag, error) {
	d.writes = append(d.writes, q)
	d.args = append(d.args, args...)
	if d.exec != nil {
		if e := d.exec(q, args); e != nil {
			return pgconn.CommandTag{}, e
		}
	}
	return pgconn.NewCommandTag("UPDATE 1"), nil
}

type fakeTx struct {
	pgx.Tx
	db *fakeDB
}

func (t *fakeTx) Commit(context.Context) error   { return nil }
func (t *fakeTx) Rollback(context.Context) error { return nil }
func (t *fakeTx) Exec(c context.Context, q string, a ...any) (pgconn.CommandTag, error) {
	return t.db.Exec(c, q, a...)
}
func (t *fakeTx) Query(c context.Context, q string, a ...any) (pgx.Rows, error) {
	return t.db.Query(c, q, a...)
}
func (t *fakeTx) QueryRow(c context.Context, q string, a ...any) pgx.Row {
	return t.db.QueryRow(c, q, a...)
}

type fakeDocker struct {
	starts, stops, removes, pulls, inspects int
	ensureErr, inspectErr, pullErr          error
	inspectFailures                         int
	image                                   Image
	created                                 []ContainerSpec
	containers                              []Container
	ensures                                 int
	dirExists                               bool
	dirErr, removeDirErr, removeErr         error
	removedDirs                             []string
}

func (d *fakeDocker) EnsureStateDir(context.Context, string, string) error {
	d.ensures++
	return d.ensureErr
}
func (d *fakeDocker) RemoveStateDir(_ context.Context, _ string, instance string) error {
	if d.removeDirErr != nil {
		return d.removeDirErr
	}
	d.removedDirs = append(d.removedDirs, instance)
	return nil
}
func (d *fakeDocker) StateDirExists(context.Context, string, string) (bool, error) {
	return d.dirExists, d.dirErr
}
func (d *fakeDocker) Create(_ context.Context, s ContainerSpec) (Container, error) {
	d.created = append(d.created, s)
	return Container{ID: "created"}, nil
}
func (d *fakeDocker) Start(context.Context, string) error                { d.starts++; return nil }
func (d *fakeDocker) Stop(context.Context, string, time.Duration) error  { d.stops++; return nil }
func (d *fakeDocker) Remove(context.Context, string, bool) error         { d.removes++; return d.removeErr }
func (d *fakeDocker) List(context.Context) ([]Container, error)          { return d.containers, nil }
func (d *fakeDocker) Inspect(context.Context, string) (Container, error) { return Container{}, nil }
func (d *fakeDocker) InspectImage(context.Context, string) (Image, error) {
	d.inspects++
	if d.inspectErr != nil {
		return Image{}, d.inspectErr
	}
	if d.inspectFailures > 0 {
		d.inspectFailures--
		return Image{}, errors.New("No such image")
	}
	return d.image, nil
}
func (d *fakeDocker) PullImage(context.Context, string) (Image, error) {
	d.pulls++
	if d.pullErr != nil {
		return Image{}, d.pullErr
	}
	return d.image, nil
}

type fakePeer struct {
	status   Status
	pushes   int
	pushErr  error
	meta     map[string]string // core address → version
	statuses map[string]Status // instance ID → status
}

func (p *fakePeer) Meta(_ context.Context, address string) (Meta, error) {
	m := BrokerMeta()
	if version, ok := p.meta[address]; ok {
		m.Version = version
	} else {
		m.Version = p.status.Version
	}
	return m, nil
}
func (p *fakePeer) Live(context.Context, string) bool  { return true }
func (p *fakePeer) Ready(context.Context, string) bool { return true }
func (p *fakePeer) Status(_ context.Context, _ string, id string) (Status, error) {
	s, ok := p.statuses[id]
	if !ok {
		s = p.status
	}
	s.Instance = id
	return s, nil
}
func (p *fakePeer) Push(context.Context, string, string, Snapshot) error {
	p.pushes++
	return p.pushErr
}
func ptr[T any](v T) *T { return &v }
func validStatus() Status {
	now := time.Now().UTC()
	return Status{Instance: uuid.NewString(), Version: "0.1.0", Config: &ConfigStatus{Revision: 1, Hash: Hash([]byte("one")), Schema: 1, Source: "control_plane", Signature: "off", Override: ptr(false)}, ControlledAt: &now, Inflight: ptr(int64(0)), Spool: Spool{Bytes: ptr(int64(0)), Unacked: ptr(int64(0)), AckSeq: ptr(int64(0)), Dropped: ptr(int64(0))}, Draining: ptr(false), StartedAt: now.Add(-time.Hour), Features: append([]string{}, features...)}
}
func instanceValues(i instanceRow) []any {
	return []any{i.ID, i.Version, i.VersionID, i.ContainerID, i.Name, i.Address, i.Weight, i.State, i.Inflight, i.Desired, i.Applied, i.DesiredHash, i.Controlled, i.Heartbeat, i.Status, i.Observed, i.Spool, i.Ack, i.Deployment, i.ZeroSamples, i.DrainStarted}
}

func TestSnapshotAdmissionGates(t *testing.T) {
	s := validStatus()
	snapshot := Snapshot{Revision: 1, Hash: s.Config.Hash}
	if !snapshot.Matches(s) {
		t.Fatal("healthy controlled snapshot rejected")
	}
	for _, tc := range []struct {
		name   string
		change func(*Status)
	}{
		{"hash", func(s *Status) { s.Config.Hash = Hash([]byte("wrong")) }},
		{"revision", func(s *Status) { s.Config.Revision++ }},
		{"override", func(s *Status) { s.Config.Override = ptr(true) }},
		{"cache", func(s *Status) { s.Config.Source = "cache" }},
		{"uncontrolled", func(s *Status) { s.ControlledAt = nil }},
		{"features", func(s *Status) { s.Features = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := validStatus()
			tc.change(&s)
			if snapshot.Matches(s) {
				t.Fatal("unsafe admission")
			}
		})
	}
	p := &fakePeer{pushErr: ErrOverride}
	r := Runtime{Peer: p}
	if e := r.applySnapshot(context.Background(), instanceRow{ID: s.Instance}, snapshot, Status{}); !errors.Is(e, ErrOverride) {
		t.Fatalf("override push: %v", e)
	}
}

func TestDeploymentStagesAndRollbackWeights(t *testing.T) {
	now := time.Now()
	p := progress{Stage: 0, From: map[string]int{"old": 100}, To: map[string]int{"new": 100}, Started: now, Minimum: 0}
	if p.canAdvance(now.Add(59 * time.Second)) {
		t.Fatal("minimum duration bypass")
	}
	for stage, want := range []int{0, 10, 50, 100} {
		weights := p.weights(stage)
		if weights["new"] != want || weights["old"] != 100-want {
			t.Fatal(weights)
		}
	}
	for p.Stage = 0; p.Stage < 3; p.Stage++ {
		if !p.canAdvance(now.Add(time.Minute)) {
			t.Fatal("stage stuck")
		}
	}
	if p.canAdvance(now.Add(time.Hour)) {
		t.Fatal("advanced beyond 100")
	}
	// Restart serialization preserves the rollback baseline and timer.
	raw, _ := json.Marshal(p)
	var restarted progress
	if e := json.Unmarshal(raw, &restarted); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(restarted.weights(0), map[string]int{"old": 100, "new": 0}) {
		t.Fatal("rollback lost baseline")
	}
}

func TestDeploymentNextWeights(t *testing.T) {
	p := progress{Stage: 0, From: map[string]int{"old": 100}, To: map[string]int{"new": 100}}
	raw, _ := json.Marshal(p)
	d := Deployment{State: "ready"}
	setNextWeights(&d, raw)
	if !reflect.DeepEqual(d.Next, map[string]int{"old": 90, "new": 10}) {
		t.Fatalf("ready next weights: %#v", d.Next)
	}
	if !d.AutoAdvance {
		t.Fatal("stored auto-advance policy not reported")
	}
	p.Stage = 1
	raw, _ = json.Marshal(p)
	d = Deployment{State: "shifting"}
	setNextWeights(&d, raw)
	if !reflect.DeepEqual(d.Next, map[string]int{"old": 50, "new": 50}) {
		t.Fatalf("shifting next weights: %#v", d.Next)
	}
	d = Deployment{State: "failed"}
	setNextWeights(&d, raw)
	if d.Next != nil {
		t.Fatalf("terminal deployment exposed next weights: %#v", d.Next)
	}
}

func TestDrainTimeoutAndPruneGuard(t *testing.T) {
	now := time.Now()
	i := instanceRow{State: "draining", DrainStarted: ptr(now.Add(-20 * time.Second)), Observed: &now, Heartbeat: &now, ZeroSamples: 3, Spool: ptr(int64(0))}
	if drainDecision(i, now, time.Minute) != "stop" {
		t.Fatal("zero drain did not complete")
	}
	i.Inflight = 1
	if drainDecision(i, now, time.Minute) != "wait" {
		t.Fatal("inflight ignored")
	}
	i.Inflight = 0
	i.Spool = ptr(int64(1))
	if drainDecision(i, now, time.Minute) != "wait" {
		t.Fatal("unacked ignored")
	}
	if drainDecision(i, now.Add(time.Hour), time.Minute) != "timeout" {
		t.Fatal("timeout ignored")
	}
}

func TestRestartReconciliation(t *testing.T) {
	id := uuid.NewString()
	docker := &fakeDocker{containers: []Container{{ID: "container", Names: []string{"/core-a"}, State: "exited", Labels: map[string]string{RoleLabel: "core", InstanceLabel: id, VersionLabel: "0.1.0", DeploymentLabel: "deployment", "com.docker.compose.project": "test"}}}}
	db := &fakeDB{rows: func(string, []any) [][]any {
		return [][]any{{id, uuid.NewString(), "0.1.0", ptr("container"), "core-a", "running", 100, ptr("deployment"), false}}
	}}
	r := Runtime{Pool: db, Docker: docker, Project: "test"}
	if e := r.reconcile(context.Background()); e != nil {
		t.Fatal(e)
	}
	if docker.starts != 1 {
		t.Fatalf("starts=%d", docker.starts)
	}
	if !strings.Contains(strings.Join(db.writes, "\n"), "config_revision_applied=null") {
		t.Fatal("restart retained stale admission")
	}
	for _, state := range []string{"stopped", "pruned", "draining", "failed"} {
		if shouldRestart(state, "exited", false) {
			t.Fatal("restart terminal state", state)
		}
	}
}

func TestLegacyComposeCoreIsRetired(t *testing.T) {
	id := uuid.NewString()
	docker := &fakeDocker{containers: []Container{{ID: "legacy", Name: "/token-ne-core", State: "running", Labels: map[string]string{RoleLabel: "core", InstanceLabel: id, VersionLabel: "0.0.3", "com.docker.compose.project": "test"}}}}
	db := &fakeDB{rows: func(string, []any) [][]any {
		return [][]any{{id, uuid.NewString(), "0.0.1", ptr("legacy"), "token-ne-core", "running", 0, nil, false}}
	}}
	status := validStatus()
	status.Instance, status.Version = id, "0.0.3"
	r := Runtime{Pool: db, Docker: docker, Peer: &fakePeer{status: status}, Project: "test"}
	if e := r.reconcile(context.Background()); e != nil {
		t.Fatal(e)
	}
	if docker.stops != 1 || !strings.Contains(strings.Join(db.writes, "\n"), "legacy Compose core retired") {
		t.Fatalf("legacy core was not retired: stops=%d writes=%v", docker.stops, db.writes)
	}
}

func TestTrackedCoreWithoutDeploymentLabelIsReconciledAsManaged(t *testing.T) {
	id := uuid.NewString()
	docker := &fakeDocker{containers: []Container{{ID: "managed", Name: "/managed", State: "running", Labels: map[string]string{RoleLabel: "core", InstanceLabel: id, VersionLabel: "0.0.5", "com.docker.compose.project": "test"}}}}
	db := &fakeDB{rows: func(string, []any) [][]any {
		return [][]any{{id, uuid.NewString(), "0.0.5", ptr("managed"), "managed", "running", 0, ptr("deployment"), false}}
	}}
	status := validStatus()
	status.Instance, status.Version = id, "0.0.5"
	r := Runtime{Pool: db, Docker: docker, Peer: &fakePeer{status: status}, Project: "test"}
	if err := r.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if docker.stops != 0 || strings.Contains(strings.Join(db.writes, "\n"), "legacy Compose core retired") {
		t.Fatalf("tracked core without label was treated as legacy: stops=%d writes=%v", docker.stops, db.writes)
	}
}

func TestTrackedWarmingCoreWithoutDeploymentLabelIsNotRetired(t *testing.T) {
	id := uuid.NewString()
	docker := &fakeDocker{containers: []Container{{ID: "managed", Name: "/managed", State: "running", Labels: map[string]string{RoleLabel: "core", InstanceLabel: id, VersionLabel: "0.0.5", "com.docker.compose.project": "test"}}}}
	db := &fakeDB{rows: func(string, []any) [][]any {
		return [][]any{{id, uuid.NewString(), "0.0.5", ptr("managed"), "managed", "running", 0, ptr("deployment"), false}}
	}}
	status := validStatus()
	status.Instance, status.Version = id, "0.0.5"
	r := Runtime{Pool: db, Docker: docker, Peer: &fakePeer{status: status}, Project: "test"}
	if err := r.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if docker.stops != 0 {
		t.Fatalf("warming managed core was stopped: stops=%d", docker.stops)
	}
}

func TestMissingLegacyComposeCoreIsNotRecreated(t *testing.T) {
	id := uuid.NewString()
	docker := &fakeDocker{}
	db := &fakeDB{rows: func(string, []any) [][]any {
		return [][]any{{id, uuid.NewString(), "0.0.1", ptr("legacy"), "token-ne-core", "running", 0, nil, false}}
	}}
	r := Runtime{Pool: db, Docker: docker, Project: "test"}
	if err := r.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(docker.created) != 0 || docker.starts != 0 || !strings.Contains(strings.Join(db.writes, "\n"), "legacy Compose core retired") {
		t.Fatalf("missing legacy core was recreated: creates=%d starts=%d writes=%v", len(docker.created), docker.starts, db.writes)
	}
}

func TestLegacyComposeCoreWithTrafficIsNotStopped(t *testing.T) {
	id := uuid.NewString()
	docker := &fakeDocker{containers: []Container{{ID: "legacy", Name: "/token-ne-core", State: "running", Labels: map[string]string{RoleLabel: "core", InstanceLabel: id, VersionLabel: "0.0.3", "com.docker.compose.project": "test"}}}}
	db := &fakeDB{rows: func(string, []any) [][]any {
		return [][]any{{id, uuid.NewString(), "0.0.1", ptr("legacy"), "token-ne-core", "running", 100, nil, false}}
	}}
	r := Runtime{Pool: db, Docker: docker, Project: "test"}
	if err := r.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if docker.stops != 0 || strings.Contains(strings.Join(db.writes, "\n"), "legacy Compose core retired") {
		t.Fatalf("traffic-serving legacy core was changed: stops=%d writes=%v", docker.stops, db.writes)
	}
}

func TestLegacyComposeCoreWithPendingSpoolIsNotStopped(t *testing.T) {
	id := uuid.NewString()
	docker := &fakeDocker{containers: []Container{{ID: "legacy", Name: "/token-ne-core", State: "running", Labels: map[string]string{RoleLabel: "core", InstanceLabel: id, VersionLabel: "0.0.3", "com.docker.compose.project": "test"}}}}
	db := &fakeDB{rows: func(string, []any) [][]any {
		return [][]any{{id, uuid.NewString(), "0.0.1", ptr("legacy"), "token-ne-core", "running", 0, nil, false}}
	}}
	status := validStatus()
	status.Instance, status.Version = id, "0.0.3"
	status.Spool.Unacked = ptr(int64(2))
	r := Runtime{Pool: db, Docker: docker, Peer: &fakePeer{status: status}, Project: "test"}
	if err := r.reconcile(context.Background()); err != nil {
		t.Fatalf("unsafe legacy core retirement blocked reconciliation: %v", err)
	}
	if docker.stops != 0 || strings.Contains(strings.Join(db.writes, "\n"), "legacy Compose core retired") {
		t.Fatalf("legacy core with pending spool was changed: stops=%d writes=%v", docker.stops, db.writes)
	}
}

func TestLegacyRetirementPersistsDockerListName(t *testing.T) {
	id := uuid.NewString()
	docker := &fakeDocker{containers: []Container{{ID: "legacy", Names: []string{"/token-ne-core"}, State: "running", Labels: map[string]string{RoleLabel: "core", InstanceLabel: id, VersionLabel: "0.0.3", "com.docker.compose.project": "test"}}}}
	var retired []any
	db := &fakeDB{rows: func(string, []any) [][]any {
		return [][]any{{id, uuid.NewString(), "0.0.1", ptr("legacy"), "token-ne-core", "running", 0, nil, false}}
	}}
	db.exec = func(q string, args []any) error {
		if strings.Contains(q, "lifecycle_error='legacy Compose core retired'") {
			retired = append([]any{}, args...)
		}
		return nil
	}
	status := validStatus()
	status.Instance, status.Version = id, "0.0.3"
	r := Runtime{Pool: db, Docker: docker, Peer: &fakePeer{status: status}, Project: "test"}
	if err := r.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(retired) < 3 || retired[2] != "token-ne-core" {
		t.Fatalf("retired core persisted an empty identity: args=%v", retired)
	}
}

func TestAdoptedManagedCorePersistsDeployment(t *testing.T) {
	id := uuid.NewString()
	deployment := uuid.NewString()
	docker := &fakeDocker{containers: []Container{{ID: "managed", Names: []string{"/managed"}, State: "running", Labels: map[string]string{RoleLabel: "core", InstanceLabel: id, VersionLabel: "0.0.5", DeploymentLabel: deployment, "com.docker.compose.project": "test"}}}}
	var inserted []any
	db := &fakeDB{row: func(q string, _ []any) fakeRow {
		switch {
		case strings.Contains(q, "from core_versions where version=$1"):
			return fakeRow{values: []any{"version-id"}}
		case strings.Contains(q, "from core_deployments where id=$1"):
			return fakeRow{values: []any{true}}
		}
		return fakeRow{err: pgx.ErrNoRows}
	}}
	db.exec = func(q string, args []any) error {
		if strings.Contains(q, "insert into core_instances") {
			inserted = append([]any{}, args...)
		}
		return nil
	}
	r := Runtime{Pool: db, Docker: docker, Peer: &fakePeer{status: validStatus()}, Project: "test", StateHostDir: "/srv/cores"}
	if err := r.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(inserted) != 6 || inserted[5] != deployment {
		t.Fatalf("adopted core lost its deployment identity: args=%v", inserted)
	}
}

func TestAdoptedManagedCoreWithoutDeploymentRecordIsLeftRunning(t *testing.T) {
	id := uuid.NewString()
	docker := &fakeDocker{containers: []Container{{ID: "managed", Names: []string{"/managed"}, State: "running", Labels: map[string]string{RoleLabel: "core", InstanceLabel: id, VersionLabel: "0.0.5", DeploymentLabel: uuid.NewString(), "com.docker.compose.project": "test"}}}}
	db := &fakeDB{row: func(q string, _ []any) fakeRow {
		if strings.Contains(q, "from core_versions where version=$1") {
			return fakeRow{values: []any{"version-id"}}
		}
		if strings.Contains(q, "from core_deployments where id=$1") {
			return fakeRow{values: []any{false}}
		}
		return fakeRow{err: pgx.ErrNoRows}
	}}
	r := Runtime{Pool: db, Docker: docker, Peer: &fakePeer{status: validStatus()}, Project: "test", StateHostDir: "/srv/cores"}
	if err := r.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if docker.stops != 0 || strings.Contains(strings.Join(db.writes, "\n"), "insert into core_instances") {
		t.Fatalf("core with unknown deployment was adopted or stopped: stops=%d writes=%v", docker.stops, db.writes)
	}
}

func TestOrphanLegacyComposeCoreIsNotStopped(t *testing.T) {
	id := uuid.NewString()
	docker := &fakeDocker{containers: []Container{{ID: "orphan", Name: "/token-ne-core", State: "running", Labels: map[string]string{RoleLabel: "core", InstanceLabel: id, VersionLabel: "0.0.3", "com.docker.compose.project": "test"}}}}
	db := &fakeDB{row: func(q string, _ []any) fakeRow {
		if strings.Contains(q, "exists(select 1 from core_instances where lifecycle_error") {
			return fakeRow{values: []any{false}}
		}
		return fakeRow{err: pgx.ErrNoRows}
	}}
	traffic := &fakeTraffic{}
	r := Runtime{Pool: db, Docker: docker, Caddy: traffic, Project: "test"}
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if docker.stops != 0 || len(traffic.last) != 0 {
		t.Fatalf("untracked legacy core was stopped or admitted: stops=%d backends=%v", docker.stops, traffic.last)
	}
}

func TestEnsureCurrentDoesNotDuplicateActiveVersion(t *testing.T) {
	repo := "ghcr.io/ivanli-cn/token-ne-core"
	db := &fakeDB{row: func(q string, _ []any) fakeRow {
		switch {
		case strings.Contains(q, "insert into core_versions("):
			return fakeRow{values: []any{"version-id"}}
		case strings.Contains(q, "from core_versions where version=$1"):
			return fakeRow{values: []any{"version-id", "0.2.0", repo + ":0.2.0", nil, false, 1, 1, 1}}
		case strings.Contains(q, "select exists(") && strings.Contains(q, "core_instances"):
			return fakeRow{values: []any{true}}
		case strings.Contains(q, "select exists(") && strings.Contains(q, "core_deployments"):
			return fakeRow{values: []any{false}}
		}
		return fakeRow{err: pgx.ErrNoRows}
	}}
	docker := &fakeDocker{}
	r := Runtime{Pool: db, Docker: docker, CoreImage: repo, CurrentVersion: "0.2.0"}
	if e := r.EnsureCurrent(context.Background()); e != nil {
		t.Fatal(e)
	}
	if docker.pulls != 0 || len(docker.created) != 0 {
		t.Fatalf("active current version triggered a rollout: pulls=%d created=%d", docker.pulls, len(docker.created))
	}
}

func TestEnsureCurrentDoesNotRetryFailedDeployment(t *testing.T) {
	repo := "ghcr.io/ivanli-cn/token-ne-core"
	db := &fakeDB{row: func(q string, _ []any) fakeRow {
		switch {
		case strings.Contains(q, "from core_versions where version=$1"):
			return fakeRow{values: []any{"version-id", "0.2.0", repo + ":0.2.0", nil, false, 1, 1, 1}}
		case strings.Contains(q, "select exists(") && strings.Contains(q, "core_instances"):
			return fakeRow{values: []any{false}}
		case strings.Contains(q, "state in ('failed','rolled_back')"):
			return fakeRow{values: []any{true}}
		case strings.Contains(q, "select exists(") && strings.Contains(q, "core_deployments"):
			return fakeRow{values: []any{false}}
		}
		return fakeRow{err: pgx.ErrNoRows}
	}}
	docker := &fakeDocker{}
	r := Runtime{Pool: db, Docker: docker, CoreImage: repo, CurrentVersion: "0.2.0"}
	if err := r.EnsureCurrent(context.Background()); err != nil {
		t.Fatal(err)
	}
	if docker.pulls != 0 || len(docker.created) != 0 {
		t.Fatalf("failed current deployment was retried automatically: pulls=%d created=%d", docker.pulls, len(docker.created))
	}
}

func TestCurrentCoreAttemptContextAllowsRegistryPull(t *testing.T) {
	ctx, cancel := currentCoreAttemptContext(context.Background())
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) <= 30*time.Second {
		t.Fatalf("current core attempt budget is too short for a registry pull: deadline=%v ok=%t", deadline, ok)
	}
}

func TestEnsureCurrentRegistersAndDeploysControlPlaneVersion(t *testing.T) {
	repo := "ghcr.io/ivanli-cn/token-ne-core"
	digest := testDigest("5")
	registered := false
	var progress []byte
	created := time.Now().UTC()
	db := &fakeDB{}
	db.row = func(q string, args []any) fakeRow {
		switch {
		case strings.Contains(q, "insert into core_versions("):
			registered = true
			return fakeRow{values: []any{"version-id"}}
		case strings.Contains(q, "from core_versions where version=$1 for share"):
			return fakeRow{values: []any{"version-id", 1, 1, 1}}
		case strings.Contains(q, "from core_versions where version=$1"):
			if !registered {
				return fakeRow{err: pgx.ErrNoRows}
			}
			return fakeRow{values: []any{"version-id", "0.0.5", repo + ":0.0.5", nil, false, 1, 1, 1}}
		case strings.Contains(q, "select exists("):
			return fakeRow{values: []any{false}}
		case strings.Contains(q, "from core_deployments where state not in"):
			return fakeRow{err: pgx.ErrNoRows}
		case strings.Contains(q, "from core_deployments where id=$1"):
			return fakeRow{values: []any{"deployment-id", nil, "0.0.5", "warming", map[string]int{}, progress, nil, created, created}}
		}
		return fakeRow{err: pgx.ErrNoRows}
	}
	db.exec = func(q string, args []any) error {
		if strings.Contains(q, "update core_deployments set progress=$2") {
			progress, _ = args[1].([]byte)
		}
		return nil
	}
	db.rows = func(q string, _ []any) [][]any {
		if strings.Contains(q, "select i.id::text,i.weight,v.id::text") {
			return [][]any{}
		}
		return nil
	}
	docker := &fakeDocker{image: Image{RepoDigests: []string{repo + "@" + digest}}}
	r := Runtime{Pool: db, Docker: docker, CoreImage: repo, CurrentVersion: "0.0.5", CurrentCommit: "release-commit",
		Project: "token-ne", Networks: []string{"edge", "core-control"}, StateHostDir: "/srv/cores", CoreURL: "http://api:8080", Token: "token"}
	if err := r.EnsureCurrent(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(docker.created) != 1 || docker.created[0].Version != "0.0.5" || docker.created[0].Image != repo+"@"+digest || docker.created[0].Deployment == "" {
		t.Fatalf("current release was not deployed as a versioned instance: %#v", docker.created)
	}
	if !registered || docker.pulls != 1 || docker.starts != 1 {
		t.Fatalf("registration/deployment work: registered=%t pulls=%d starts=%d", registered, docker.pulls, docker.starts)
	}
}

func TestSnapshotUpstreamBaseURLCanonicalization(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"https://api.example/v1", "https://api.example"},
		{"https://api.example/step_plan/v1/", "https://api.example/step_plan"},
		{"https://api.example/gateway%2Fv1", "https://api.example/gateway%2Fv1"},
		{"https://api.example/gateway%2Fv1/v1", "https://api.example/gateway%2Fv1"},
		{"https://api.example/v1/compatible", "https://api.example/v1/compatible"},
	} {
		if got := normalizeSnapshotUpstreamBaseURL(tc.input); got != tc.want {
			t.Errorf("normalizeSnapshotUpstreamBaseURL(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestSnapshotGenerationStableAcrossRestart(t *testing.T) {
	var saved generation
	db := &fakeDB{}
	db.row = func(q string, args []any) fakeRow {
		switch {
		case strings.Contains(q, "core_config_generation"):
			if saved.Revision == 0 {
				return fakeRow{err: pgx.ErrNoRows}
			}
			return fakeRow{values: []any{saved}}
		case strings.Contains(q, "config_schema_min"):
			return fakeRow{values: []any{1, 1, 1}}
		case strings.Contains(q, "jsonb_agg"):
			return fakeRow{values: []any{json.RawMessage(`[]`), json.RawMessage(`[]`), []byte(`[]`), json.RawMessage(`[]`)}}
		}
		return fakeRow{err: pgx.ErrNoRows}
	}
	db.exec = func(q string, args []any) error {
		if strings.Contains(q, "insert into system_state") {
			saved = args[0].(generation)
		}
		return nil
	}
	s := Snapshots{DB: db, Token: "token", BaseURL: "http://api:8080"}
	id := uuid.NewString()
	first, e := s.Build(context.Background(), id, false)
	if e != nil {
		t.Fatal(e)
	}
	restarted := Snapshots{DB: db, Token: s.Token, BaseURL: s.BaseURL}
	second, e := restarted.Build(context.Background(), id, false)
	if e != nil {
		t.Fatal(e)
	}
	if first.Revision != 1 || first.Hash != second.Hash || string(first.Body) != string(second.Body) {
		t.Fatal("revision/hash changed on restart")
	}
	restarted.Token = "rotated"
	third, e := restarted.Build(context.Background(), id, false)
	if e != nil || third.Revision != 2 {
		t.Fatalf("change did not advance revision: %v", e)
	}
	override, e := restarted.Build(context.Background(), id, true)
	if e != nil {
		t.Fatal(e)
	}
	var body map[string]any
	_ = json.Unmarshal(override.Body, &body)
	if body["allow_unsigned"] != true {
		t.Fatal("override lacks acknowledgement")
	}
}

type fixedSnapshot struct{ snapshot Snapshot }

func (f fixedSnapshot) Build(context.Context, string, bool) (Snapshot, error) { return f.snapshot, nil }
func TestAdmitHealthyZeroAndRejectHash(t *testing.T) {
	s := validStatus()
	snapshot := Snapshot{Revision: 1, Hash: s.Config.Hash}
	now := time.Now()
	i := instanceRow{ID: s.Instance, Version: s.Version, ContainerID: ptr("container"), State: "running", Weight: 0, Heartbeat: &now, Address: "core:8080"}
	peer := &fakePeer{status: s}
	db := &fakeDB{}
	r := Runtime{Pool: db, Peer: peer, Config: fixedSnapshot{snapshot}}
	pool, e := r.Admit(context.Background(), []instanceRow{i})
	if e != nil || len(pool) != 1 || pool[0].Weight != 0 {
		t.Fatalf("zero warm pool: %v %v", pool, e)
	}
	peer.status.Config.Hash = Hash([]byte("wrong"))
	pool, e = r.Admit(context.Background(), []instanceRow{i})
	if e != nil || len(pool) != 0 || peer.pushes != 1 {
		t.Fatalf("hash gate: %v %v", pool, e)
	}
	peer.status = s
	peer.status.Config.Override = ptr(true)
	peer.pushErr = ErrOverride
	pool, e = r.Admit(context.Background(), []instanceRow{i})
	if len(pool) != 0 || !errors.Is(e, ErrOverride) {
		t.Fatalf("override gate: %v %v", pool, e)
	}
}

func TestDrainDoesNotCompleteWithPendingSpool(t *testing.T) {
	id := uuid.NewString()
	p := progress{From: map[string]int{"old": 100}, To: map[string]int{"new": 100}, Deadline: 1800}
	raw, _ := json.Marshal(p)
	now := time.Now()
	i := instanceRow{ID: "old", State: "draining", DrainStarted: ptr(now.Add(-time.Minute)), Observed: &now, Spool: ptr(int64(1)), ZeroSamples: 3}
	db := &fakeDB{row: func(string, []any) fakeRow { return fakeRow{values: []any{"draining_old", raw}} }, rows: func(string, []any) [][]any { return [][]any{instanceValues(i)} }}
	docker := &fakeDocker{}
	r := Runtime{Pool: db, Docker: docker}
	if e := r.finishDrain(context.Background(), id, 1800); e != nil {
		t.Fatal(e)
	}
	if docker.stops != 0 || len(db.writes) != 0 {
		t.Fatal("premature completion", db.writes)
	}
	i.DrainStarted = ptr(now.Add(-time.Hour))
	if e := r.finishDrain(context.Background(), id, 1800); !errors.Is(e, ErrConflict) {
		t.Fatalf("missing timeout: %v", e)
	}
	if !strings.Contains(strings.Join(db.writes, "\n"), "state='failed'") {
		t.Fatal("timeout not persisted")
	}
}

type fakeTraffic struct {
	calls int
	last  []caddy.Backend
	err   error
}

func (f *fakeTraffic) Apply(_ context.Context, pool []caddy.Backend, _ bool) error {
	f.calls++
	f.last = pool
	return f.err
}
func TestTrafficApplyAndHeartbeatExportCoreMetrics(t *testing.T) {
	registry := metrics.New()
	db := &fakeDB{row: func(q string, _ []any) fakeRow {
		switch {
		case strings.Contains(q, "for update of i"):
			return fakeRow{values: []any{"0.1.0"}}
		case strings.Contains(q, "heartbeat_sent_at"):
			return fakeRow{values: []any{nil}}
		case strings.Contains(q, "config_revision_desired"):
			return fakeRow{values: []any{nil, nil, nil, nil, nil}}
		case strings.Contains(q, "exists"):
			return fakeRow{values: []any{false}}
		}
		return fakeRow{err: pgx.ErrNoRows}
	}}
	traffic := &fakeTraffic{err: errors.New("caddy reload failed")}
	r := Runtime{Pool: db, Caddy: traffic, Metrics: registry}
	if e := r.ApplyTraffic(context.Background(), false); e == nil {
		t.Fatal("failed Caddy apply was not propagated")
	}
	traffic.err = nil
	s := validStatus()
	messageID, _ := uuid.NewV7()
	h := Heartbeat{Protocol: 1, ID: messageID.String(), SentAt: time.Now().UTC(), Type: "heartbeat", Status: s}
	if e := r.Heartbeat(context.Background(), s.Instance, h); e != nil {
		t.Fatal(e)
	}
	page := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/metrics", nil)
	req.Header.Set("Authorization", "Bearer secret")
	registry.Handler("secret", staticCores{instance: s.Instance})(page, req)
	body := page.Body.String()
	for _, want := range []string{
		`token_ne_caddy_apply_total{result="error"} 1`,
		`token_ne_caddy_apply_total{result="success"} 1`,
		`token_ne_core_config_source{version="0.1.0",instance="` + s.Instance + `",source="control_plane"} 1`,
		`token_ne_core_config_override_active{version="0.1.0",instance="` + s.Instance + `"} 0`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s in metrics output: %s", want, body)
		}
	}
}

func TestCaddyMetricIgnoresCleanupFailure(t *testing.T) {
	db := &fakeDB{
		row: func(q string, _ []any) fakeRow {
			if strings.Contains(q, "exists") {
				return fakeRow{values: []any{true}}
			}
			return fakeRow{err: pgx.ErrNoRows}
		},
		exec: func(q string, _ []any) error {
			if strings.Contains(q, "lifecycle_error=null") {
				return errors.New("cleanup failed")
			}
			return nil
		},
	}
	registry := metrics.New()
	r := Runtime{Pool: db, Caddy: &fakeTraffic{}, Metrics: registry}
	if e := r.ApplyTraffic(context.Background(), false); e == nil {
		t.Fatal("cleanup failure was not propagated")
	}
	page := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/metrics", nil)
	req.Header.Set("Authorization", "Bearer secret")
	registry.Handler("secret", staticCores{instance: uuid.NewString()})(page, req)
	body := page.Body.String()
	if !strings.Contains(body, `token_ne_caddy_apply_total{result="success"} 1`) {
		t.Fatalf("successful Caddy apply was not recorded: %s", body)
	}
	if strings.Contains(body, `token_ne_caddy_apply_total{result="error"}`) {
		t.Fatalf("database cleanup failure was classified as Caddy failure: %s", body)
	}
}

type staticCores struct{ instance string }

func (s staticCores) UsageMetricCores(context.Context) ([]metrics.Core, error) {
	return []metrics.Core{{Version: "0.1.0", Instance: s.instance, State: "running"}}, nil
}

func (staticCores) UsageMetricPool() map[string]int32 { return nil }

func TestReadPolicyKeepsLastValidOnInvalidFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "status-rewrite.yaml")
	s := &Snapshots{PolicyPath: path}
	fromFile := s.readPolicy(policy{})
	if fromFile.Source != "default" || !fromFile.valid() {
		t.Fatalf("missing file must fall back to the built-in policy: %+v", fromFile)
	}
	valid, e := os.ReadFile("../../../config/status-rewrite.yaml")
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, valid, 0o644); e != nil {
		t.Fatal(e)
	}
	accepted := s.readPolicy(fromFile)
	if accepted.Source != "file" || accepted.Hash != Hash(valid) {
		t.Fatalf("valid file not published: %+v", accepted)
	}
	if e = os.WriteFile(path, []byte("schema_version: 1\nenabled: true\nrules: [{\n"), 0o644); e != nil {
		t.Fatal(e)
	}
	kept := s.readPolicy(accepted)
	if kept.Hash != accepted.Hash || kept.Source != "file" {
		t.Fatalf("invalid file replaced the last valid policy: %+v", kept)
	}
	if e = os.Remove(path); e != nil {
		t.Fatal(e)
	}
	deleted := s.readPolicy(accepted)
	if deleted.Hash != accepted.Hash || deleted.Source != "file" {
		t.Fatalf("deleted file replaced the last valid policy: %+v", deleted)
	}
	emptyPath := (&Snapshots{}).readPolicy(accepted)
	if emptyPath.Hash != accepted.Hash || emptyPath.Source != "file" {
		t.Fatalf("empty policy path replaced the last valid policy: %+v", emptyPath)
	}
}

func TestSnapshotHonoursStreamUsageInjectionSwitch(t *testing.T) {
	features := func(injection, stored *bool) map[string]any {
		db := &fakeDB{}
		db.row = func(q string, _ []any) fakeRow {
			switch {
			case strings.Contains(q, "core_config_generation"):
				return fakeRow{err: pgx.ErrNoRows}
			case strings.Contains(q, "config_schema_min"):
				return fakeRow{values: []any{1, 1, 1}}
			case strings.Contains(q, "jsonb_agg"):
				return fakeRow{values: []any{json.RawMessage(`[]`), json.RawMessage(`[]`), []byte(`[]`), json.RawMessage(`[]`)}}
			case strings.Contains(q, "stream_usage_injection") && stored != nil:
				return fakeRow{values: []any{*stored}}
			}
			return fakeRow{err: pgx.ErrNoRows}
		}
		s := Snapshots{DB: db, Token: "token", BaseURL: "http://api:8080", StreamUsageInjection: injection}
		snapshot, e := s.Build(context.Background(), uuid.NewString(), false)
		if e != nil {
			t.Fatal(e)
		}
		var body struct {
			Features map[string]any `json:"features"`
		}
		if e = json.Unmarshal(snapshot.Body, &body); e != nil {
			t.Fatal(e)
		}
		return body.Features
	}
	if got := features(nil, nil)["stream_usage_injection"]; got != true {
		t.Fatalf("default snapshot disabled stream usage injection: %v", got)
	}
	disabled := false
	if got := features(&disabled, nil)["stream_usage_injection"]; got != false {
		t.Fatalf("snapshot ignored the disabled switch: %v", got)
	}
	if got := features(&disabled, nil)["status_rewrite"]; got != true {
		t.Fatalf("policy-driven feature changed with the injection switch: %v", got)
	}
	enabled := true
	if got := features(&enabled, &disabled)["stream_usage_injection"]; got != false {
		t.Fatalf("stored switch did not override the deployment default: %v", got)
	}
}

func TestRollbackRestoresOldAndZerosNew(t *testing.T) {
	now := time.Now()
	oldStatus := validStatus()
	newStatus := validStatus()
	oldStatus.Instance = "old"
	newStatus.Instance = "new"
	old := instanceRow{ID: "old", Version: oldStatus.Version, State: "running", Address: "old:8080", Weight: 10, ContainerID: ptr("old"), Heartbeat: &now}
	fresh := instanceRow{ID: "new", Version: newStatus.Version, State: "running", Address: "new:8080", Weight: 90, ContainerID: ptr("new"), Heartbeat: &now}
	p := progress{From: map[string]int{"old": 100}, To: map[string]int{"new": 100}}
	raw, _ := json.Marshal(p)
	db := &fakeDB{row: func(q string, _ []any) fakeRow {
		if strings.Contains(q, "exists") {
			return fakeRow{values: []any{false}}
		}
		return fakeRow{values: []any{"shifting", raw}}
	}, rows: func(string, []any) [][]any { return [][]any{instanceValues(old), instanceValues(fresh)} }}
	db.exec = func(q string, a []any) error {
		if strings.Contains(q, "set weight=$2") {
			if a[0] == "old" {
				old.Weight = a[1].(int)
			} else {
				fresh.Weight = a[1].(int)
			}
		}
		return nil
	}
	peer := &fakePeer{status: oldStatus}
	traffic := &fakeTraffic{}
	docker := &fakeDocker{}
	r := Runtime{Pool: db, Docker: docker, Peer: peer, Caddy: traffic, Config: fixedSnapshot{Snapshot{Revision: 1, Hash: oldStatus.Config.Hash}}}
	if e := r.Rollback(context.Background(), uuid.NewString(), "operator"); e != nil {
		t.Fatal(e)
	}
	if old.Weight != 100 || fresh.Weight != 0 || traffic.calls == 0 {
		t.Fatal("rollback traffic not restored")
	}
	// Rollback reuses the images that are already present; it never refreshes a
	// registry tag, because the baseline must be reproducible.
	if docker.pulls != 0 || docker.inspects != 0 || len(docker.created) != 0 {
		t.Fatalf("rollback touched images: pulls=%d inspects=%d created=%d", docker.pulls, docker.inspects, len(docker.created))
	}
}

func TestDeployEnsuresTargetImage(t *testing.T) {
	repo := "ghcr.io/ivanli-cn/token-ne-core"
	digest := testDigest("3")
	for _, tc := range []struct {
		name string
		auto *bool
		want bool
	}{
		{"default auto advance", nil, true},
		{"explicitly disabled", ptr(false), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			created := time.Now().UTC()
			var stored []byte
			db := &fakeDB{}
			db.row = func(q string, _ []any) fakeRow {
				switch {
				case strings.Contains(q, "from core_versions where version=$1 for share"):
					return fakeRow{values: []any{"version-id", 1, 1, 1}}
				case strings.Contains(q, "from core_versions where version=$1"):
					return fakeRow{values: []any{"version-id", "0.2.0", repo + ":0.2.0", nil, false, 1, 1, 1}}
				case strings.Contains(q, "from core_deployments where state not in"):
					return fakeRow{err: pgx.ErrNoRows}
				case strings.Contains(q, "from core_deployments where id=$1"):
					return fakeRow{values: []any{"deployment-id", nil, "0.2.0", "warming", map[string]int{}, stored, nil, created, created}}
				}
				return fakeRow{err: pgx.ErrNoRows}
			}
			db.exec = func(q string, a []any) error {
				if q == `update core_deployments set progress=$2 where id=$1` {
					stored, _ = a[1].([]byte)
				}
				return nil
			}
			docker := &fakeDocker{image: Image{RepoDigests: []string{repo + "@" + digest}}}
			r := Runtime{Pool: db, Docker: docker, CoreImage: repo, Project: "project", Networks: []string{"edge", "core-control"}, StateHostDir: "/srv/cores", CoreURL: "http://api:8080", Token: "token"}
			deployment, e := r.Deploy(context.Background(), "operator", DeploymentRequest{Version: "0.2.0", Replicas: 1, AutoAdvance: tc.auto})
			if e != nil {
				t.Fatal(e)
			}
			if docker.pulls != 1 || len(docker.created) != 1 || docker.starts != 1 {
				t.Fatalf("image work: pulls=%d created=%d starts=%d", docker.pulls, len(docker.created), docker.starts)
			}
			if docker.created[0].Image != repo+"@"+digest {
				t.Fatalf("container image: %q", docker.created[0].Image)
			}
			if !strings.Contains(strings.Join(db.writes, "\n"), "update core_versions set image_ref=$3,image_digest=$2 where id=$1") {
				t.Fatalf("resolved digest not persisted: %v", db.writes)
			}
			if deployment.AutoAdvance != tc.want {
				t.Fatalf("auto advance: %t", deployment.AutoAdvance)
			}
		})
	}
}

func TestAutoAdvanceReadyToShifting(t *testing.T) {
	base := validStatus()
	oldStatus := validStatus()
	oldStatus.Version = "0.1.0"
	newStatus := validStatus()
	newStatus.Version = "1.0.0"
	now := time.Now()
	if !(progress{}).autoAdvance() {
		t.Fatal("missing auto-advance policy did not default to true")
	}
	for _, tc := range []struct {
		name    string
		auto    *bool
		started time.Time
		advance bool
	}{
		{"auto advance", ptr(true), now.Add(-2 * time.Minute), true},
		{"disabled", ptr(false), now.Add(-2 * time.Minute), false},
		{"minimum stage not elapsed", ptr(true), now.Add(-10 * time.Second), false},
		{"legacy record", nil, now.Add(-2 * time.Minute), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := instanceRow{ID: "old", Version: "0.1.0", State: "running", Address: "old:8080", Weight: 100, ContainerID: ptr("old"), Heartbeat: &now, Desired: ptr(int64(1)), Applied: ptr(int64(1))}
			fresh := instanceRow{ID: "new", Version: "1.0.0", State: "running", Address: "new:8080", Weight: 0, ContainerID: ptr("new"), Heartbeat: &now, Desired: ptr(int64(1)), Applied: ptr(int64(1))}
			p := progress{Stage: 0, From: map[string]int{"old": 100}, To: map[string]int{"new": 100}, Started: tc.started, Minimum: 60, Deadline: 1800, AutoAdvance: tc.auto}
			raw, _ := json.Marshal(p)
			var shifted []any
			db := &fakeDB{row: func(q string, _ []any) fakeRow {
				switch {
				case strings.Contains(q, "from core_deployments where state not in"):
					return fakeRow{values: []any{"deployment-id", "ready", raw}}
				case strings.Contains(q, "exists"):
					return fakeRow{values: []any{false}}
				}
				return fakeRow{err: pgx.ErrNoRows}
			}, rows: func(string, []any) [][]any { return [][]any{instanceValues(old), instanceValues(fresh)} }}
			db.exec = func(q string, a []any) error {
				if strings.Contains(q, "set weight=$2") {
					switch a[0] {
					case "old":
						old.Weight = a[1].(int)
					case "new":
						fresh.Weight = a[1].(int)
					}
				}
				if strings.Contains(q, "update core_deployments set state=$2,target_weights=$3,progress=$4") {
					shifted = append([]any{}, a...)
				}
				return nil
			}
			traffic := &fakeTraffic{}
			peer := &fakePeer{
				meta:     map[string]string{"old:8080": oldStatus.Version, "new:8080": newStatus.Version},
				statuses: map[string]Status{"old": oldStatus, "new": newStatus},
			}
			r := Runtime{Pool: db, Docker: &fakeDocker{}, Peer: peer, Caddy: traffic, Config: fixedSnapshot{Snapshot{Revision: 1, Hash: base.Config.Hash}}}
			if e := r.advance(context.Background()); e != nil {
				t.Fatal(e)
			}
			if !tc.advance {
				if len(shifted) != 0 || old.Weight != 100 || fresh.Weight != 0 || traffic.calls != 0 {
					t.Fatalf("ready deployment advanced: %#v old=%d new=%d", shifted, old.Weight, fresh.Weight)
				}
				return
			}
			if len(shifted) == 0 || shifted[1] != "shifting" {
				t.Fatalf("deployment state write: %#v", shifted)
			}
			weights := shifted[2].(map[string]int)
			if weights["old"] != 90 || weights["new"] != 10 || old.Weight != 90 || fresh.Weight != 10 {
				t.Fatalf("stage weights: %#v old=%d new=%d", weights, old.Weight, fresh.Weight)
			}
			if traffic.calls == 0 {
				t.Fatal("traffic not applied")
			}
		})
	}
}

func TestShiftScopesWeightsToDeploymentInstances(t *testing.T) {
	oldStatus := validStatus()
	oldStatus.Version = "0.1.0"
	newStatus := validStatus()
	newStatus.Version = "1.0.0"
	now := time.Now()
	old := instanceRow{ID: "old", Version: "0.1.0", State: "running", Address: "old:8080", Weight: 100, ContainerID: ptr("old"), Heartbeat: &now, Desired: ptr(int64(1)), Applied: ptr(int64(1))}
	fresh := instanceRow{ID: "new", Version: "1.0.0", State: "running", Address: "new:8080", Weight: 0, ContainerID: ptr("new"), Heartbeat: &now, Desired: ptr(int64(1)), Applied: ptr(int64(1))}
	// Another instance runs the new version outside this deployment. It must not
	// absorb any of the submitted version weight.
	other := instanceRow{ID: "other", Version: "1.0.0", State: "running", Address: "other:8080", Weight: 0, ContainerID: ptr("other"), Heartbeat: &now, Desired: ptr(int64(1)), Applied: ptr(int64(1))}
	p := progress{Stage: 0, From: map[string]int{"old": 100}, To: map[string]int{"new": 100}, Started: now.Add(-2 * time.Minute), Minimum: 60, Deadline: 1800}
	raw, _ := json.Marshal(p)
	db := &fakeDB{row: func(q string, _ []any) fakeRow {
		switch {
		case strings.Contains(q, "select state,progress from core_deployments"):
			return fakeRow{values: []any{"shifting", raw}}
		case strings.Contains(q, "exists"):
			return fakeRow{values: []any{false}}
		}
		return fakeRow{err: pgx.ErrNoRows}
	}, rows: func(string, []any) [][]any {
		return [][]any{instanceValues(old), instanceValues(fresh), instanceValues(other)}
	}}
	db.exec = func(q string, a []any) error {
		if strings.Contains(q, "set weight=$2") {
			switch a[0] {
			case "old":
				old.Weight = a[1].(int)
			case "new":
				fresh.Weight = a[1].(int)
			case "other":
				other.Weight = a[1].(int)
			}
		}
		return nil
	}
	traffic := &fakeTraffic{}
	peer := &fakePeer{
		meta:     map[string]string{"old:8080": oldStatus.Version, "new:8080": newStatus.Version, "other:8080": newStatus.Version},
		statuses: map[string]Status{"old": oldStatus, "new": newStatus, "other": newStatus},
	}
	r := Runtime{Pool: db, Docker: &fakeDocker{}, Peer: peer, Caddy: traffic, Config: fixedSnapshot{Snapshot{Revision: 1, Hash: oldStatus.Config.Hash}}}
	if e := r.Shift(context.Background(), uuid.NewString(), map[string]int{"0.1.0": 90, "1.0.0": 10}); e != nil {
		t.Fatal(e)
	}
	if old.Weight != 90 || fresh.Weight != 10 || other.Weight != 0 {
		t.Fatalf("scoped weights: old=%d new=%d other=%d", old.Weight, fresh.Weight, other.Weight)
	}
}
