package cores

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func candidateValues(c pruneCandidate) []any {
	return []any{c.ID, c.Version, c.ContainerID, c.ContainerName, c.State, c.Weight, c.Deployment,
		c.StateDir, c.Spool, c.Ack, c.ZeroSamples, c.DrainStarted, c.StoppedAt, c.ZeroAt, c.BlockedAt,
		c.Failures, c.NextAttempt, c.PrunedAt, c.LifecycleError, c.RetiringState, c.RetiringCompletedAt}
}

// stoppedCandidate is a fully retired instance: stopped, weight zero, drained,
// spool zero proof recorded, retirement deployment completed.
func stoppedCandidate(now time.Time) pruneCandidate {
	deployment := uuid.NewString()
	stopped := now.Add(-time.Hour)
	drained := stopped.Add(-time.Minute)
	zero := stopped.Add(-30 * time.Second)
	completed := now.Add(-time.Hour)
	state := "completed"
	return pruneCandidate{
		ID:                  uuid.NewString(),
		Version:             "0.2.0",
		ContainerID:         ptr("container-" + uuid.NewString()[:8]),
		ContainerName:       "core-0.2.0",
		State:               "stopped",
		Weight:              0,
		Deployment:          &deployment,
		StateDir:            "/srv/cores",
		Spool:               ptr(int64(0)),
		Ack:                 ptr(int64(42)),
		DrainStarted:        &drained,
		StoppedAt:           &stopped,
		ZeroAt:              &zero,
		RetiringState:       &state,
		RetiringCompletedAt: &completed,
	}
}

// pruneDB answers both the list query the automatic pass uses and the single-row
// lookup the manual prune uses.
func pruneDB(candidates ...pruneCandidate) *fakeDB {
	data := make([][]any, 0, len(candidates))
	for _, c := range candidates {
		data = append(data, candidateValues(c))
	}
	db := &fakeDB{rows: func(string, []any) [][]any { return data }}
	db.row = func(string, []any) fakeRow {
		if len(data) == 0 {
			return fakeRow{err: pgx.ErrNoRows}
		}
		return fakeRow{values: data[0]}
	}
	return db
}

func pruneRuntime(db *fakeDB, docker *fakeDocker) *Runtime {
	return &Runtime{Pool: db, Docker: docker, Project: "test", StateHostDir: "/srv/cores"}
}

func pruneWrites(db *fakeDB) string { return strings.Join(db.writes, "\n") }

func containsArg(args []any, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

// auditPayload decodes the JSON payload of the last recorded audit insert.
func auditPayload(t *testing.T, args []any) map[string]any {
	t.Helper()
	for n := len(args) - 1; n >= 0; n-- {
		raw, ok := args[n].([]byte)
		if !ok {
			continue
		}
		var payload map[string]any
		if json.Unmarshal(raw, &payload) == nil && payload["automatic"] != nil {
			return payload
		}
	}
	t.Fatalf("no audit payload recorded in %v", args)
	return nil
}

func TestPreDrainZeroObservationDoesNotAuthorizeDeletion(t *testing.T) {
	now := time.Now().UTC()
	drained := now.Add(-time.Minute)
	observedAt := drained.Add(-2 * time.Minute)
	status := validStatus()
	status.Inflight, status.Spool.Unacked, status.Draining = ptr(int64(0)), ptr(int64(0)), ptr(false)

	if postDrainZero(&drained, status, observedAt) {
		t.Fatal("zero observation taken before drain completion counted as proof")
	}
	if proof := spoolProof(&drained, status, observedAt); proof != nil {
		t.Fatalf("pre-drain zero produced a durable proof: %v", proof)
	}

	c := stoppedCandidate(now)
	c.ZeroAt = nil // only a pre-drain zero was ever seen
	docker := &fakeDocker{}
	db := pruneDB(c)
	if e := pruneRuntime(db, docker).autoPrune(context.Background()); e != nil {
		t.Fatal(e)
	}
	if docker.removes != 0 || len(docker.removedDirs) != 0 {
		t.Fatalf("deletion with only a pre-drain zero: removes=%d dirs=%v", docker.removes, docker.removedDirs)
	}
	if strings.Contains(pruneWrites(db), "state='pruned'") {
		t.Fatal("instance was marked destroyed without a post-drain proof")
	}
}

func TestPostDrainZeroAndElapsedGraceAuthorizesDeletion(t *testing.T) {
	c := stoppedCandidate(time.Now().UTC())
	docker := &fakeDocker{}
	db := pruneDB(c)
	if e := pruneRuntime(db, docker).autoPrune(context.Background()); e != nil {
		t.Fatal(e)
	}
	if docker.removes != 1 {
		t.Fatalf("container removes=%d", docker.removes)
	}
	if len(docker.removedDirs) != 1 || docker.removedDirs[0] != c.ID {
		t.Fatalf("state directory removals=%v", docker.removedDirs)
	}
	writes := pruneWrites(db)
	if !strings.Contains(writes, "state='pruned'") || !strings.Contains(writes, "pruned_at=now()") {
		t.Fatalf("instance not recorded as destroyed: %v", writes)
	}
	audit := auditPayload(t, db.args)
	if audit["automatic"] != true || audit["result"] != "success" || audit["state_dir_removed"] != true {
		t.Fatalf("automatic prune audit incomplete: %v", audit)
	}
}

func TestGraceAnchorsAtLaterOfStopAndCompletion(t *testing.T) {
	now := time.Now().UTC()
	// Stop is old, completion is recent: the window starts at completion.
	c := stoppedCandidate(now)
	c.StoppedAt = ptr(now.Add(-2 * time.Hour))
	c.RetiringCompletedAt = ptr(now.Add(-time.Minute))
	cfg := pruneConfig{enabled: true, grace: 5 * time.Minute, maxAge: 24 * time.Hour}
	if reason := pruneEligibility(c, nil, now, cfg); reason != pruneReasonGrace {
		t.Fatalf("grace did not anchor at completion: %s", reason)
	}
	// Stop is recent, completion is old: the window starts at the stop.
	c = stoppedCandidate(now)
	c.StoppedAt = ptr(now.Add(-time.Minute))
	c.RetiringCompletedAt = ptr(now.Add(-2 * time.Hour))
	if reason := pruneEligibility(c, nil, now, cfg); reason != pruneReasonGrace {
		t.Fatalf("grace did not anchor at the stop: %s", reason)
	}
	// Both old: the window has elapsed.
	c = stoppedCandidate(now)
	c.StoppedAt = ptr(now.Add(-2 * time.Hour))
	c.RetiringCompletedAt = ptr(now.Add(-2 * time.Hour))
	if reason := pruneEligibility(c, nil, now, cfg); reason != pruneReasonNone {
		t.Fatalf("elapsed window still gated: %s", reason)
	}
	// A deployment that has not completed never opens the gate.
	c = stoppedCandidate(now)
	incomplete := "draining_old"
	c.RetiringState = &incomplete
	if reason := pruneEligibility(c, nil, now, cfg); reason != pruneReasonDeployment {
		t.Fatalf("incomplete deployment opened the gate: %s", reason)
	}
}

func TestNonzeroSpoolKeepsContainerAndDirectory(t *testing.T) {
	now := time.Now().UTC()
	// A later nonzero observation clears the earlier zero proof.
	drained := now.Add(-time.Minute)
	status := validStatus()
	status.Inflight, status.Spool.Unacked = ptr(int64(0)), ptr(int64(3))
	if proof := spoolProof(&drained, status, now); proof != nil {
		t.Fatal("nonzero spool produced a zero proof")
	}
	c := stoppedCandidate(now)
	c.ZeroAt = nil
	c.Spool = ptr(int64(3))
	docker := &fakeDocker{}
	db := pruneDB(c)
	if e := pruneRuntime(db, docker).autoPrune(context.Background()); e != nil {
		t.Fatal(e)
	}
	if docker.removes != 0 || len(docker.removedDirs) != 0 {
		t.Fatalf("nonzero spool deleted artifacts: removes=%d dirs=%v", docker.removes, docker.removedDirs)
	}
	if strings.Contains(pruneWrites(db), "state='pruned'") {
		t.Fatal("nonzero spool marked the instance destroyed")
	}
	// A retained-but-stale proof is also not enough once a newer nonzero landed.
	c = stoppedCandidate(now)
	c.Spool = ptr(int64(2))
	c.ZeroAt = nil
	if reason := pruneEligibility(c, nil, now, pruneConfig{grace: 0, maxAge: 24 * time.Hour}); reason != pruneReasonSpool {
		t.Fatalf("nonzero spool opened the gate: %s", reason)
	}
}

func TestCleanupBlockSetsAtMaxAgeAndClearsWhenZeroArrives(t *testing.T) {
	now := time.Now().UTC()
	c := stoppedCandidate(now)
	c.ZeroAt = nil
	c.StoppedAt = ptr(now.Add(-25 * time.Hour))
	c.DrainStarted = ptr(now.Add(-30 * time.Hour))
	docker := &fakeDocker{}
	db := pruneDB(c)
	if e := pruneRuntime(db, docker).autoPrune(context.Background()); e != nil {
		t.Fatal(e)
	}
	writes := pruneWrites(db)
	if !strings.Contains(writes, "cleanup_blocked_at=$2") {
		t.Fatalf("max age did not set cleanup_blocked: %v", writes)
	}
	if docker.removes != 0 || len(docker.removedDirs) != 0 {
		t.Fatal("blocked instance was destroyed")
	}

	// The block is not terminal: a newer post-drain zero lifts it.
	drained := now.Add(-time.Minute)
	status := validStatus()
	status.Inflight, status.Spool.Unacked, status.Draining = ptr(int64(0)), ptr(int64(0)), ptr(false)
	observed := now
	if proof := spoolProof(&drained, status, observed); proof == nil || !proof.Equal(observed) {
		t.Fatalf("zero after a block did not produce a proof: %v", proof)
	}
	blocked := now.Add(-time.Hour)
	c.BlockedAt = &blocked
	c.ZeroAt = &observed
	db = pruneDB(c)
	docker = &fakeDocker{}
	if e := pruneRuntime(db, docker).autoPrune(context.Background()); e != nil {
		t.Fatal(e)
	}
	if docker.removes != 1 || len(docker.removedDirs) != 1 {
		t.Fatalf("cleared block was not re-entered into deletion: removes=%d dirs=%v", docker.removes, docker.removedDirs)
	}
}

func TestMissingOwnershipLabelBlocksDeletion(t *testing.T) {
	now := time.Now().UTC()
	c := stoppedCandidate(now)
	container := Container{ID: *c.ContainerID, State: "exited", Labels: map[string]string{
		RoleLabel: "core", VersionLabel: c.Version, InstanceLabel: c.ID, // deployment label missing
		"com.docker.compose.project": "test",
	}}
	docker := &fakeDocker{containers: []Container{container}}
	db := pruneDB(c)
	r := pruneRuntime(db, docker)
	r.Metrics = nil
	if e := r.autoPrune(context.Background()); e != nil {
		t.Fatal(e)
	}
	if docker.removes != 0 || len(docker.removedDirs) != 0 {
		t.Fatalf("incomplete labels allowed deletion: removes=%d dirs=%v", docker.removes, docker.removedDirs)
	}
	if !strings.Contains(pruneWrites(db), "update core_instances set lifecycle_error=$2") {
		t.Fatalf("labels-incomplete condition was not recorded: %v", pruneWrites(db))
	}
	if !containsArg(db.args, pruneLabelsError) {
		t.Fatalf("labels-incomplete marker not persisted: %v", db.args)
	}
	// A disagreeing deployment label is just as incomplete.
	c = stoppedCandidate(now)
	mismatched := map[string]string{
		RoleLabel: "core", VersionLabel: c.Version, InstanceLabel: c.ID,
		DeploymentLabel: uuid.NewString(), "com.docker.compose.project": "test",
	}
	if ownershipLabelsMatch(mismatched, c) {
		t.Fatal("label disagreement accepted")
	}
}

func TestFailedRemovalPersistsRetryWithBackoff(t *testing.T) {
	now := time.Now().UTC()
	c := stoppedCandidate(now)
	c.ContainerID = nil // the state-directory helper is what fails
	docker := &fakeDocker{removeDirErr: errors.New("helper exited with status 1")}
	db := pruneDB(c)
	if e := pruneRuntime(db, docker).autoPrune(context.Background()); e == nil {
		t.Fatal("removal failure was swallowed")
	}
	writes := pruneWrites(db)
	if !strings.Contains(writes, "prune_failures=prune_failures+1") || !strings.Contains(writes, "prune_next_attempt_at=$2") {
		t.Fatalf("retry not persisted: %v", writes)
	}
	if !strings.Contains(writes, "state='stopped'") {
		t.Fatalf("failed instance left the retryable state: %v", writes)
	}
	if strings.Contains(writes, "state='pruned'") {
		t.Fatal("failed instance was marked destroyed")
	}
	if pruneBackoff(1) != time.Minute || pruneBackoff(2) != 2*time.Minute || pruneBackoff(60) != time.Hour {
		t.Fatalf("backoff schedule: %v %v %v", pruneBackoff(1), pruneBackoff(2), pruneBackoff(60))
	}
	// A pending retry is not retried early.
	next := now.Add(time.Minute)
	c2 := stoppedCandidate(now)
	c2.Failures, c2.NextAttempt = 1, &next
	if reason := pruneEligibility(c2, nil, now, pruneConfig{grace: 0, maxAge: 24 * time.Hour}); reason != pruneReasonBackoff {
		t.Fatalf("backoff ignored: %s", reason)
	}
}

func TestForcePruneRemovesContainerOnly(t *testing.T) {
	now := time.Now().UTC()
	c := stoppedCandidate(now)
	c.ZeroAt = nil // no zero proof: force is the only way to drop the container
	docker := &fakeDocker{}
	db := pruneDB(c)
	r := pruneRuntime(db, docker)
	if e := r.Prune(context.Background(), "operator", []string{c.ID}, true); e != nil {
		t.Fatal(e)
	}
	if docker.removes != 1 {
		t.Fatalf("force prune removes=%d", docker.removes)
	}
	if len(docker.removedDirs) != 0 {
		t.Fatalf("force prune deleted the state directory: %v", docker.removedDirs)
	}
	writes := pruneWrites(db)
	if !strings.Contains(writes, "container_id=null") {
		t.Fatalf("force prune did not clear the container reference: %v", writes)
	}
	audit := auditPayload(t, db.args)
	if audit["force"] != true || audit["automatic"] != false || audit["state_dir_removed"] != false {
		t.Fatalf("force prune audit incomplete: %v", audit)
	}
	if strings.Contains(writes, "state='pruned'") {
		t.Fatal("force prune marked the instance destroyed")
	}
}

func TestManualPruneRequiresPostDrainZeroProof(t *testing.T) {
	now := time.Now().UTC()
	c := stoppedCandidate(now)
	c.ZeroAt = nil
	docker := &fakeDocker{}
	db := pruneDB(c)
	r := pruneRuntime(db, docker)
	e := r.Prune(context.Background(), "operator", []string{c.ID}, false)
	if !errors.Is(e, ErrSpoolNotDrained) || !errors.Is(e, ErrConflict) {
		t.Fatalf("manual prune without proof: %v", e)
	}
	if docker.removes != 0 || len(docker.removedDirs) != 0 {
		t.Fatal("manual prune without proof had side effects")
	}
	// A proof observed before the drain mark is not a proof.
	c = stoppedCandidate(now)
	c.ZeroAt = ptr(c.DrainStarted.Add(-time.Minute))
	if manualZeroProof(c) {
		t.Fatal("pre-drain proof accepted")
	}
	c = stoppedCandidate(now)
	if !manualZeroProof(c) {
		t.Fatal("post-drain proof rejected")
	}
	// A running instance is refused even with force.
	c = stoppedCandidate(now)
	c.State = "running"
	db = pruneDB(c)
	docker = &fakeDocker{}
	if e := pruneRuntime(db, docker).Prune(context.Background(), "operator", []string{c.ID}, true); !errors.Is(e, ErrConflict) {
		t.Fatalf("running instance pruned: %v", e)
	}
	if docker.removes != 0 {
		t.Fatal("running instance container removed")
	}
}

func TestNonzeroSpoolRestartsCompatibleCoreForReplay(t *testing.T) {
	now := time.Now().UTC()
	c := stoppedCandidate(now)
	c.ZeroAt = nil
	c.Spool = ptr(int64(4))
	docker := &fakeDocker{}
	db := pruneDB(c)
	if e := pruneRuntime(db, docker).autoPrune(context.Background()); e != nil {
		t.Fatal(e)
	}
	if docker.starts != 1 {
		t.Fatalf("retired instance was not restarted for replay: starts=%d", docker.starts)
	}
	writes := pruneWrites(db)
	if !strings.Contains(writes, "state='draining'") || !strings.Contains(writes, "spool_zero_at=null") {
		t.Fatalf("replay did not re-enter drain with a cleared proof: %v", writes)
	}
	if strings.Contains(writes, "state='pruned'") {
		t.Fatal("replay marked the instance destroyed")
	}
}

func TestReplayWaitsForRetirementCompletion(t *testing.T) {
	now := time.Now().UTC()
	c := stoppedCandidate(now)
	c.ZeroAt = nil
	c.Spool = ptr(int64(4))
	incomplete := "draining_old"
	c.RetiringState = &incomplete
	docker := &fakeDocker{}
	if e := pruneRuntime(pruneDB(c), docker).autoPrune(context.Background()); e != nil {
		t.Fatal(e)
	}
	if docker.starts != 0 {
		t.Fatalf("replay started before retirement completed: starts=%d", docker.starts)
	}
}

func TestBlockedReplayStopsWithoutDestroying(t *testing.T) {
	now := time.Now().UTC()
	c := stoppedCandidate(now)
	c.ZeroAt = nil
	c.State = "draining"
	c.StoppedAt = ptr(now.Add(-25 * time.Hour))
	docker := &fakeDocker{}
	db := pruneDB(c)
	if e := pruneRuntime(db, docker).autoPrune(context.Background()); e != nil {
		t.Fatal(e)
	}
	if docker.stops != 1 {
		t.Fatalf("blocked replay was not stopped: stops=%d", docker.stops)
	}
	if docker.removes != 0 || len(docker.removedDirs) != 0 {
		t.Fatal("blocked replay destroyed artifacts")
	}
	if !strings.Contains(pruneWrites(db), "cleanup_blocked_at=$2") {
		t.Fatalf("blocked replay was not recorded: %v", pruneWrites(db))
	}
}

func TestCleanupBlockSuppressesAutomaticRestart(t *testing.T) {
	if shouldRestart("running", "exited", true) {
		t.Fatal("cleanup-blocked instance restarted automatically")
	}
	if !shouldRestart("running", "exited", false) {
		t.Fatal("healthy instance refused restart")
	}
}

func TestRollbackBranchesOnStateDirectory(t *testing.T) {
	id, deployment := uuid.NewString(), uuid.NewString()
	old := instanceRow{ID: id, Version: "0.1.0", VersionID: uuid.NewString(), ContainerID: ptr("old-container"),
		Name: "core-old", State: "stopped", Deployment: &deployment}

	// Retained directory: reuse it and replay, never recreate or wipe it.
	db := &fakeDB{row: func(q string, _ []any) fakeRow {
		switch {
		case strings.Contains(q, "select image_ref,image_digest"):
			return fakeRow{values: []any{"ghcr.io/x/core:0.1.0", nil}}
		}
		return fakeRow{err: pgx.ErrNoRows}
	}}
	docker := &fakeDocker{dirExists: true}
	r := &Runtime{Pool: db, Docker: docker, StateHostDir: "/srv/cores", Project: "test"}
	if e := r.restoreBaseline(context.Background(), progress{Stage: 1, From: map[string]int{id: 100}}, []instanceRow{old}); !errors.Is(e, ErrConflict) {
		t.Fatalf("retained directory did not stop rollback for warming: %v", e)
	}
	if docker.starts != 1 || docker.ensures != 0 {
		t.Fatalf("retained directory was not reused: starts=%d ensures=%d", docker.starts, docker.ensures)
	}
}

func TestRollbackRecreatesContainerWhenDirectoryRetainedButContainerGone(t *testing.T) {
	id, deployment := uuid.NewString(), uuid.NewString()
	old := instanceRow{ID: id, Version: "0.1.0", VersionID: uuid.NewString(), ContainerID: nil,
		Name: "core-old", State: "stopped", Deployment: &deployment}
	db := &fakeDB{row: func(q string, _ []any) fakeRow {
		if strings.Contains(q, "select image_ref,image_digest") {
			return fakeRow{values: []any{"ghcr.io/x/core:0.1.0", ptr("sha256:" + strings.Repeat("a", 64))}}
		}
		return fakeRow{err: pgx.ErrNoRows}
	}}
	docker := &fakeDocker{dirExists: true}
	r := &Runtime{Pool: db, Docker: docker, StateHostDir: "/srv/cores", Project: "test"}
	if e := r.restoreBaseline(context.Background(), progress{Stage: 1, From: map[string]int{id: 100}}, []instanceRow{old}); !errors.Is(e, ErrConflict) {
		t.Fatalf("container rebuild did not stop rollback for warming: %v", e)
	}
	if len(docker.created) != 1 || docker.ensures != 0 {
		t.Fatalf("container was not rebuilt from the recorded image: created=%d ensures=%d", len(docker.created), docker.ensures)
	}
	want := "ghcr.io/x/core@sha256:" + strings.Repeat("a", 64)
	if docker.created[0].Image != want {
		t.Fatalf("digest identity lost: %q", docker.created[0].Image)
	}
}

func TestRollbackCreatesFreshDirectoryOnlyAfterOriginalDestroyed(t *testing.T) {
	id, deployment := uuid.NewString(), uuid.NewString()
	old := instanceRow{ID: id, Version: "0.1.0", VersionID: uuid.NewString(), ContainerID: nil,
		Name: "core-old", State: "stopped", Deployment: &deployment}
	db := &fakeDB{row: func(q string, _ []any) fakeRow {
		if strings.Contains(q, "select image_ref,image_digest") {
			return fakeRow{values: []any{"ghcr.io/x/core:0.1.0", nil}}
		}
		return fakeRow{err: pgx.ErrNoRows}
	}}
	docker := &fakeDocker{dirExists: false}
	r := &Runtime{Pool: db, Docker: docker, StateHostDir: "/srv/cores", Project: "test"}
	if e := r.restoreBaseline(context.Background(), progress{Stage: 1, From: map[string]int{id: 100}}, []instanceRow{old}); !errors.Is(e, ErrConflict) {
		t.Fatalf("fresh directory did not stop rollback for warming: %v", e)
	}
	if docker.ensures != 1 || len(docker.created) != 1 {
		t.Fatalf("fresh directory not created: ensures=%d created=%d", docker.ensures, len(docker.created))
	}
}

func TestAutoPruneFeatureSwitchAndDefaults(t *testing.T) {
	t.Setenv("CORE_AUTO_PRUNE", "")
	t.Setenv("CORE_AUTO_PRUNE_GRACE_SECONDS", "")
	t.Setenv("CORE_AUTO_PRUNE_MAX_AGE_SECONDS", "")
	cfg := (&Runtime{}).pruneConfig()
	if !cfg.enabled || cfg.grace != 300*time.Second || cfg.maxAge != 24*time.Hour {
		t.Fatalf("defaults: %+v", cfg)
	}

	t.Setenv("CORE_AUTO_PRUNE", "false")
	if (&Runtime{}).pruneConfig().enabled {
		t.Fatal("CORE_AUTO_PRUNE=false ignored")
	}
	// The switch is not part of the gate: disabling it only skips the pass.
	c := stoppedCandidate(time.Now().UTC())
	if reason := pruneEligibility(c, nil, time.Now().UTC(), pruneConfig{grace: 0, maxAge: 24 * time.Hour}); reason != pruneReasonNone {
		t.Fatalf("gate depended on the feature switch: %s", reason)
	}
	t.Setenv("CORE_AUTO_PRUNE", "true")
	t.Setenv("CORE_AUTO_PRUNE_GRACE_SECONDS", "60")
	t.Setenv("CORE_AUTO_PRUNE_MAX_AGE_SECONDS", "120")
	cfg = (&Runtime{}).pruneConfig()
	if !cfg.enabled || cfg.grace != time.Minute || cfg.maxAge != 2*time.Minute {
		t.Fatalf("env override: %+v", cfg)
	}

	// A disabled pass has no side effects at all.
	t.Setenv("CORE_AUTO_PRUNE", "false")
	docker := &fakeDocker{}
	db := pruneDB(c)
	r := pruneRuntime(db, docker)
	if e := r.autoPrune(context.Background()); e != nil {
		t.Fatal(e)
	}
	if len(db.writes) != 0 || docker.removes != 0 {
		t.Fatal("disabled automatic prune wrote or deleted")
	}
}

func TestIncompleteLabelMarkerClearsOnceLabelsMatch(t *testing.T) {
	now := time.Now().UTC()
	c := stoppedCandidate(now)
	c.LifecycleError = ptr(pruneLabelsError)
	container := Container{ID: *c.ContainerID, State: "exited", Labels: map[string]string{
		RoleLabel: "core", VersionLabel: c.Version, InstanceLabel: c.ID, DeploymentLabel: *c.Deployment,
		"com.docker.compose.project": "test",
	}}
	docker := &fakeDocker{containers: []Container{container}}
	db := pruneDB(c)
	if e := pruneRuntime(db, docker).autoPrune(context.Background()); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(pruneWrites(db), "lifecycle_error=null") {
		t.Fatalf("stale labels marker survived: %v", pruneWrites(db))
	}
	if docker.removes != 1 || len(docker.removedDirs) != 1 {
		t.Fatalf("restored labels did not allow deletion: removes=%d dirs=%v", docker.removes, docker.removedDirs)
	}
}

func TestRunningContainerIsNotDestroyed(t *testing.T) {
	now := time.Now().UTC()
	c := stoppedCandidate(now)
	container := Container{ID: *c.ContainerID, State: "running", Labels: map[string]string{
		RoleLabel: "core", VersionLabel: c.Version, InstanceLabel: c.ID, DeploymentLabel: *c.Deployment,
		"com.docker.compose.project": "test",
	}}
	docker := &fakeDocker{containers: []Container{container}}
	if e := pruneRuntime(pruneDB(c), docker).autoPrune(context.Background()); e != nil {
		t.Fatal(e)
	}
	if docker.removes != 0 || len(docker.removedDirs) != 0 {
		t.Fatal("a container the engine still reports running was destroyed")
	}
}

func TestReplayInProgressRestartsDeadCore(t *testing.T) {
	now := time.Now().UTC()
	c := stoppedCandidate(now)
	c.State = "draining"
	c.LifecycleError = ptr(replayMarker)
	c.ZeroAt = nil
	c.StoppedAt = ptr(now.Add(-10 * time.Minute))
	container := Container{ID: *c.ContainerID, State: "exited", Labels: map[string]string{
		RoleLabel: "core", VersionLabel: c.Version, InstanceLabel: c.ID, DeploymentLabel: *c.Deployment,
		"com.docker.compose.project": "test",
	}}
	docker := &fakeDocker{containers: []Container{container}}
	db := pruneDB(c)
	if e := pruneRuntime(db, docker).autoPrune(context.Background()); e != nil {
		t.Fatal(e)
	}
	if docker.starts != 1 {
		t.Fatalf("dead replay core was not restarted: starts=%d", docker.starts)
	}
	if docker.removes != 0 || len(docker.removedDirs) != 0 {
		t.Fatal("replay in progress was destroyed")
	}
	// A live replay is left alone.
	docker = &fakeDocker{containers: []Container{{ID: container.ID, State: "running", Labels: container.Labels}}}
	if e := pruneRuntime(pruneDB(c), docker).autoPrune(context.Background()); e != nil {
		t.Fatal(e)
	}
	if docker.starts != 0 {
		t.Fatal("live replay core was restarted")
	}
}
