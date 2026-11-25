package cores

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/IvanLi-CN/token-ne/server/internal/store"
	"github.com/google/uuid"
)

// pruneIntegrationRuntime builds a Runtime over the real schema so the candidate
// query, the gate and the deletion bookkeeping are proven against PostgreSQL.
// Rows created here are schema-local and removed by the cleanup below.
func pruneIntegrationRuntime(t *testing.T) (*Runtime, *fakeDocker) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	docker := &fakeDocker{}
	return &Runtime{DB: s, Docker: docker, Project: "test", StateHostDir: "/srv/cores"}, docker
}

// seedRetiredInstance inserts a version, the deployment that retires an old
// instance and the instance itself, then returns the instance id.
func seedRetiredInstance(t *testing.T, r *Runtime, mutate func(context.Context, string)) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	suffix := uuid.NewString()[:8]
	version, owner, retiring, instance := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	oldVersion := uuid.NewString()
	if _, err := r.database().Exec(ctx, `insert into core_versions(id,version,image_ref,protocol_version,config_schema_min,config_schema_max)
 values($1,$2,'ghcr.io/x/core:0.1.0',1,1,1),($3,$4,'ghcr.io/x/core:0.2.0',1,1,1)`,
		oldVersion, "prune-old-"+suffix, version, "prune-new-"+suffix); err != nil {
		t.Fatal(err)
	}
	// The instance was created by the old-version deployment and retired by the
	// new-version one; a real deployment never retires the instances it created.
	if _, err := r.database().Exec(ctx, `insert into core_deployments(id,from_version_id,to_version_id,state,target_weights,progress)
 values($1,$2,$3,'completed','{}',$4::jsonb),($5,$2,$2,'completed','{}','{}')`,
		retiring, oldVersion, version,
		`{"stage":3,"from":{"`+instance+`":100},"to":{},"stage_started_at":"2026-01-01T00:00:00Z","minimum_stage_seconds":60,"drain_deadline_seconds":1800}`,
		owner); err != nil {
		t.Fatal(err)
	}
	// The retirement completed 25 minutes ago, so at least one full grace window
	// has passed since the later of that completion and the stop.
	if _, err := r.database().Exec(ctx, `update core_deployments set updated_at=now()-interval '25 minutes' where id=$1`, retiring); err != nil {
		t.Fatal(err)
	}
	if _, err := r.database().Exec(ctx, `insert into core_instances(id,version_id,container_id,container_name,address,state,weight,deployment_id,state_dir,draining_started_at,stopped_at,spool_zero_at)
 values($1,$2,$3,$4,$5,'stopped',0,$6,$7,now()-interval '1 hour',now()-interval '30 minutes',now()-interval '20 minutes')`,
		instance, oldVersion, "container-"+suffix, "core-"+suffix, "core-"+suffix+":8080", owner, "/srv/cores"); err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(ctx, instance)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		for _, step := range []string{
			"delete from audit_events where target_id='" + instance + "'",
			"delete from core_instances where id='" + instance + "'",
			"delete from core_deployments where id in ('" + owner + "','" + retiring + "')",
			"delete from core_versions where id in ('" + oldVersion + "','" + version + "')",
		} {
			if _, execErr := r.database().Exec(cleanupCtx, step); execErr != nil {
				t.Logf("cleanup %q: %v", step, execErr)
			}
		}
	})
	return instance
}

func TestAutoPruneIntegrationDestroysConfirmedRetiredInstance(t *testing.T) {
	r, docker := pruneIntegrationRuntime(t)
	instance := seedRetiredInstance(t, r, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := r.autoPrune(ctx); err != nil {
		t.Fatal(err)
	}
	if docker.removes != 1 || len(docker.removedDirs) != 1 || docker.removedDirs[0] != instance {
		t.Fatalf("confirmed instance was not destroyed: removes=%d dirs=%v", docker.removes, docker.removedDirs)
	}
	var state string
	var prunedAt *time.Time
	if err := r.database().QueryRow(ctx, `select state,pruned_at from core_instances where id=$1`, instance).Scan(&state, &prunedAt); err != nil {
		t.Fatal(err)
	}
	if state != "pruned" || prunedAt == nil {
		t.Fatalf("instance not marked destroyed: state=%s pruned_at=%v", state, prunedAt)
	}
	var automatic bool
	var result string
	if err := r.database().QueryRow(ctx, `select (payload->>'automatic')::bool,payload->>'result' from audit_events
 where action='core.instance.prune' and target_id=$1`, instance).Scan(&automatic, &result); err != nil {
		t.Fatal(err)
	}
	if !automatic || result != "success" {
		t.Fatalf("audit: automatic=%t result=%s", automatic, result)
	}
}

func TestAutoPruneIntegrationRetainsInstanceWithoutZeroProof(t *testing.T) {
	r, docker := pruneIntegrationRuntime(t)
	instance := seedRetiredInstance(t, r, func(ctx context.Context, id string) {
		// A zero observed before the drain mark is exactly the stale-heartbeat
		// shape the contract refuses.
		if _, err := r.database().Exec(ctx, `update core_instances set spool_zero_at=draining_started_at-interval '5 minutes',draining_started_at=now()-interval '1 hour' where id=$1`, id); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := r.autoPrune(ctx); err != nil {
		t.Fatal(err)
	}
	if docker.removes != 0 || len(docker.removedDirs) != 0 {
		t.Fatalf("instance without a valid proof was destroyed: removes=%d dirs=%v", docker.removes, docker.removedDirs)
	}
	var state string
	if err := r.database().QueryRow(ctx, `select state from core_instances where id=$1`, instance).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state == "pruned" {
		t.Fatal("instance marked destroyed without a proof")
	}
}

func TestAutoPruneIntegrationBlocksAtMaxAge(t *testing.T) {
	r, docker := pruneIntegrationRuntime(t)
	instance := seedRetiredInstance(t, r, func(ctx context.Context, id string) {
		if _, err := r.database().Exec(ctx, `update core_instances set spool_zero_at=null,stopped_at=now()-interval '25 hours',draining_started_at=now()-interval '26 hours' where id=$1`, id); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := r.autoPrune(ctx); err != nil {
		t.Fatal(err)
	}
	var blocked *time.Time
	if err := r.database().QueryRow(ctx, `select cleanup_blocked_at from core_instances where id=$1`, instance).Scan(&blocked); err != nil {
		t.Fatal(err)
	}
	if blocked == nil {
		t.Fatal("max age did not set cleanup_blocked")
	}
	if docker.removes != 0 || len(docker.removedDirs) != 0 {
		t.Fatal("blocked instance was destroyed")
	}

	// The block clears when a post-drain zero arrives, and the instance then
	// re-enters the candidate set even though its stop is older than max age.
	if _, err := r.database().Exec(ctx, `update core_instances set cleanup_blocked_at=null,spool_zero_at=now()-interval '1 minute' where id=$1`, instance); err != nil {
		t.Fatal(err)
	}
	if err := r.autoPrune(ctx); err != nil {
		t.Fatal(err)
	}
	if docker.removes != 1 || len(docker.removedDirs) != 1 {
		t.Fatalf("cleared block was not re-entered: removes=%d dirs=%v", docker.removes, docker.removedDirs)
	}
}

func TestManualPruneIntegrationMapsSpoolNotDrained(t *testing.T) {
	r, docker := pruneIntegrationRuntime(t)
	instance := seedRetiredInstance(t, r, func(ctx context.Context, id string) {
		if _, err := r.database().Exec(ctx, `update core_instances set spool_zero_at=null where id=$1`, id); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	err := r.Prune(ctx, "operator", []string{instance}, false)
	if !errors.Is(err, ErrSpoolNotDrained) || !errors.Is(err, ErrConflict) {
		t.Fatalf("manual prune without proof: %v", err)
	}
	if docker.removes != 0 || len(docker.removedDirs) != 0 {
		t.Fatal("manual prune without proof had side effects")
	}
	// Force drops the container and leaves the directory.
	if err := r.Prune(ctx, "operator", []string{instance}, true); err != nil {
		t.Fatal(err)
	}
	if docker.removes != 1 || len(docker.removedDirs) != 0 {
		t.Fatalf("force prune: removes=%d dirs=%v", docker.removes, docker.removedDirs)
	}
	var containerID *string
	if err := r.database().QueryRow(ctx, `select container_id from core_instances where id=$1`, instance).Scan(&containerID); err != nil {
		t.Fatal(err)
	}
	if containerID != nil {
		t.Fatal("force prune kept the container reference")
	}
}
