package cores

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/IvanLi-CN/token-ne/server/internal/caddy"
	"reflect"
	"strings"
	"testing"
	"time"

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
	starts, stops, removes int
	containers             []Container
}

func (d *fakeDocker) EnsureStateDir(context.Context, string, string) error { return nil }
func (d *fakeDocker) Create(context.Context, ContainerSpec) (Container, error) {
	return Container{ID: "created"}, nil
}
func (d *fakeDocker) Start(context.Context, string) error                { d.starts++; return nil }
func (d *fakeDocker) Stop(context.Context, string, time.Duration) error  { d.stops++; return nil }
func (d *fakeDocker) Remove(context.Context, string, bool) error         { d.removes++; return nil }
func (d *fakeDocker) List(context.Context) ([]Container, error)          { return d.containers, nil }
func (d *fakeDocker) Inspect(context.Context, string) (Container, error) { return Container{}, nil }

type fakePeer struct {
	status  Status
	pushes  int
	pushErr error
}

func (p *fakePeer) Meta(context.Context, string) (Meta, error) {
	m := BrokerMeta()
	m.Version = p.status.Version
	return m, nil
}
func (p *fakePeer) Live(context.Context, string) bool  { return true }
func (p *fakePeer) Ready(context.Context, string) bool { return true }
func (p *fakePeer) Status(_ context.Context, _ string, id string) (Status, error) {
	s := p.status
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
	i.ID = uuid.NewString()
	i.State = "stopped"
	db := &fakeDB{row: func(string, []any) fakeRow { return fakeRow{values: instanceValues(i)} }}
	docker := &fakeDocker{}
	r := Runtime{Pool: db, Docker: docker}
	if e := r.Prune(context.Background(), "operator", []string{i.ID}); !errors.Is(e, ErrConflict) {
		t.Fatalf("prune allowed unacked: %v", e)
	}
	if docker.removes != 0 || len(db.writes) != 0 {
		t.Fatal("prune had side effects")
	}
	i.Spool = ptr(int64(0))
	if !canPrune(i) {
		t.Fatal("cleared stopped instance refused")
	}
	i.Spool = nil
	if canPrune(i) {
		t.Fatal("unknown spool allowed")
	}
}

func TestRestartReconciliation(t *testing.T) {
	id := uuid.NewString()
	docker := &fakeDocker{containers: []Container{{ID: "container", Names: []string{"/core-a"}, State: "exited", Labels: map[string]string{RoleLabel: "core", InstanceLabel: id, VersionLabel: "0.1.0", "com.docker.compose.project": "test"}}}}
	db := &fakeDB{rows: func(string, []any) [][]any {
		return [][]any{{id, uuid.NewString(), "0.1.0", ptr("container"), "core-a", "running"}}
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
		if shouldRestart(state, "exited") {
			t.Fatal("restart terminal state", state)
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
}

func (f *fakeTraffic) Apply(_ context.Context, pool []caddy.Backend, _ bool) error {
	f.calls++
	f.last = pool
	return nil
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
	r := Runtime{Pool: db, Peer: peer, Caddy: traffic, Config: fixedSnapshot{Snapshot{Revision: 1, Hash: oldStatus.Config.Hash}}}
	if e := r.Rollback(context.Background(), uuid.NewString(), "operator"); e != nil {
		t.Fatal(e)
	}
	if old.Weight != 100 || fresh.Weight != 0 || traffic.calls == 0 {
		t.Fatal("rollback traffic not restored")
	}
}
