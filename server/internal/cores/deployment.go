package cores

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var stages = []int{0, 10, 50, 100}

type Deployment struct {
	ID          string         `json:"id"`
	FromVersion *string        `json:"from_version"`
	ToVersion   string         `json:"to_version"`
	State       string         `json:"state"`
	Target      map[string]int `json:"target_weights"`
	Next        map[string]int `json:"next_weights,omitempty"`
	AutoAdvance bool           `json:"auto_advance"`
	Error       *string        `json:"error"`
	Created     time.Time      `json:"created_at"`
	Updated     time.Time      `json:"updated_at"`
}

func (r *Runtime) Deploy(ctx context.Context, actor string, req DeploymentRequest) (Deployment, error) {
	if req.Replicas < 1 || req.Replicas > 10 || req.InitialWeight != 0 {
		return Deployment{}, ErrInvalid
	}
	autoAdvance := true
	if req.AutoAdvance != nil {
		autoAdvance = *req.AutoAdvance
	}
	// A registry pull can take minutes, so the target image is resolved and made
	// available before the deployment transaction opens.
	target, e := r.versionImage(ctx, req.Version)
	if e != nil {
		return Deployment{}, e
	}
	if target.Protocol != Protocol {
		return Deployment{}, ErrCompatibility
	}
	if _, e = SchemaFor(target.SchemaMin, target.SchemaMax); e != nil {
		return Deployment{}, ErrCompatibility
	}
	image, e := r.prepareVersionImage(ctx, target)
	if e != nil {
		return Deployment{}, e
	}
	r.deployMu.Lock()
	defer r.deployMu.Unlock()
	r.lifecycle.Lock()
	defer r.lifecycle.Unlock()
	tx, e := r.database().Begin(ctx)
	if e != nil {
		return Deployment{}, e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `select pg_advisory_xact_lock(4927816043)`); e != nil {
		return Deployment{}, e
	}
	var id, versionID string
	var protocol, min, max int
	e = tx.QueryRow(ctx, `select id::text,protocol_version,config_schema_min,config_schema_max from core_versions where version=$1 for share`, req.Version).Scan(&versionID, &protocol, &min, &max)
	if errors.Is(e, pgx.ErrNoRows) {
		return Deployment{}, ErrUnknown
	}
	if e != nil {
		return Deployment{}, e
	}
	if protocol != Protocol {
		return Deployment{}, ErrCompatibility
	}
	if _, e = SchemaFor(min, max); e != nil {
		return Deployment{}, ErrCompatibility
	}
	var state string
	e = tx.QueryRow(ctx, `select state from core_deployments where state not in ('completed','failed','rolled_back') limit 1 for update`).Scan(&state)
	if e == nil {
		return Deployment{}, ErrConflict
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return Deployment{}, e
	}
	if _, e = tx.Exec(ctx, `select pg_advisory_xact_lock(4927816044)`); e != nil {
		return Deployment{}, e
	}
	id = uuid.NewString()
	var from *string
	fromWeights := map[string]int{}
	oldRows, e := tx.Query(ctx, `select i.id::text,i.weight,v.id::text from core_instances i join core_versions v on v.id=i.version_id where i.weight>0 and i.state='running' order by i.id for update of i`)
	if e != nil {
		return Deployment{}, e
	}
	for oldRows.Next() {
		var instance, version string
		var weight int
		if e = oldRows.Scan(&instance, &weight, &version); e != nil {
			oldRows.Close()
			return Deployment{}, e
		}
		fromWeights[instance] = weight
		if from == nil {
			from = &version
		}
	}
	oldRows.Close()
	if e = oldRows.Err(); e != nil {
		return Deployment{}, e
	}
	now := time.Now().UTC()
	weights := map[string]int{}
	if _, e = tx.Exec(ctx, `insert into core_deployments(id,from_version_id,to_version_id,state,target_weights,progress) values($1,$2,$3,'pending','{}','{}')`, id, from, versionID); e != nil {
		return Deployment{}, e
	}
	specs := []ContainerSpec{}
	for n := 0; n < req.Replicas; n++ {
		instance := uuid.NewString()
		name := fmt.Sprintf("token-ne-core-%s-%s-%d", slug(req.Version), id[:8], n+1)
		specs = append(specs, ContainerSpec{Instance: instance, Version: req.Version, Deployment: id, Name: name, Image: image, Project: r.Project, Networks: r.Networks, StateHostDir: r.StateHostDir, ControlURL: r.CoreURL, Token: r.Token, SigningKey: r.SigningKey, SignatureMode: r.SignatureMode, CacheKey: r.CacheKey, AllowPrivateUpstreams: r.AllowPrivateUpstreams})
		weights[instance] = 100 / req.Replicas
		if n < 100%req.Replicas {
			weights[instance]++
		}
		if _, e = tx.Exec(ctx, `insert into core_instances(id,version_id,container_name,address,weight,state,inflight,state_dir,deployment_id) values($1,$2,$3,$4,0,'pending',0,$5,$6)`, instance, versionID, name, name+":8080", filepath.Join(r.StateHostDir, instance), id); e != nil {
			return Deployment{}, e
		}
	}
	p := progress{Stage: -1, From: fromWeights, To: weights, Started: now, Minimum: 60, Deadline: 1800, AutoAdvance: &autoAdvance}
	raw, _ := json.Marshal(p)
	if _, e = tx.Exec(ctx, `update core_deployments set progress=$2 where id=$1`, id, raw); e != nil {
		return Deployment{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return Deployment{}, e
	}
	created := []string{}
	for _, spec := range specs {
		if createErr := r.Docker.EnsureStateDir(ctx, spec.StateHostDir, spec.Instance); createErr != nil {
			return Deployment{}, r.failDeploy(ctx, id, created, createErr)
		}
		container, createErr := r.Docker.Create(ctx, spec)
		if createErr != nil {
			return Deployment{}, r.failDeploy(ctx, id, created, createErr)
		}
		created = append(created, container.ID)
		if _, e = r.database().Exec(ctx, `update core_instances set container_id=$2 where id=$1`, spec.Instance, container.ID); e != nil {
			return Deployment{}, r.failDeploy(ctx, id, created, e)
		}
		if e = r.Docker.Start(ctx, container.ID); e != nil {
			return Deployment{}, r.failDeploy(ctx, id, created, e)
		}
	}
	if _, e = r.database().Exec(ctx, `update core_deployments set state='warming',updated_at=now() where id=$1`, id); e != nil {
		return Deployment{}, r.failDeploy(ctx, id, created, e)
	}

	if e = r.Audit(ctx, actor, "core.deploy", "core_deployment", id, map[string]any{"to_version": req.Version, "replicas": req.Replicas, "initial_weight": 0, "auto_advance": autoAdvance}); e != nil {
		return Deployment{}, r.failDeploy(ctx, id, created, e)
	}
	var updated Deployment
	var stored []byte
	e = r.database().QueryRow(ctx, `select id::text,from_version_id::text,(select version from core_versions where id=to_version_id),state,target_weights,progress,error,created_at,updated_at from core_deployments where id=$1`, id).Scan(&updated.ID, &updated.FromVersion, &updated.ToVersion, &updated.State, &updated.Target, &stored, &updated.Error, &updated.Created, &updated.Updated)
	if e != nil {
		return Deployment{}, r.failDeploy(ctx, id, created, e)
	}
	setNextWeights(&updated, stored)
	return updated, nil
}

func (r *Runtime) failDeploy(ctx context.Context, id string, containers []string, cause error) error {
	// Persist the terminal marker before teardown: a slow or hung container stop
	// must not let reconcile resurrect a deployment the caller was told failed.
	// The request context may already be canceled, so use an independent budget.
	stateCtx, cancelState := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	_, deploymentErr := r.database().Exec(stateCtx, `update core_deployments set state='failed',error=$2,updated_at=now() where id=$1`, id, cause.Error())
	_, instanceErr := r.database().Exec(stateCtx, `update core_instances set state='failed',weight=0,lifecycle_error=$2 where deployment_id=$1`, id, cause.Error())
	cancelState()

	cleanup, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancelCleanup()
	for _, container := range containers {
		if e := r.Docker.Stop(cleanup, container, 5*time.Second); e != nil {
			_ = r.Docker.Remove(cleanup, container, true)
		}
	}
	return errors.Join(cause, deploymentErr, instanceErr)
}

func (r *Runtime) advance(ctx context.Context) error {
	r.deployMu.Lock()
	defer r.deployMu.Unlock()
	var id, state string
	var raw []byte
	e := r.database().QueryRow(ctx, `select id::text,state,progress from core_deployments where state not in ('completed','failed','rolled_back') order by created_at limit 1`).Scan(&id, &state, &raw)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	var p progress
	if e = json.Unmarshal(raw, &p); e != nil {
		return e
	}
	instances, e := r.instances(ctx)
	if e != nil {
		return e
	}
	if state == "pending" {
		if time.Since(p.Started) > 30*time.Minute {
			_, e = r.database().Exec(ctx, `update core_deployments set state='failed',error='pending timeout; containers were not observable',updated_at=now() where id=$1`, id)
			return e
		}
		if len(p.To) == 0 {
			return ErrConflict
		}
		for target := range p.To {
			found := false
			for _, i := range instances {
				if i.ID == target && i.ContainerID != nil {
					found = true
				}
			}
			if !found {
				return nil
			}
		}
		_, e = r.database().Exec(ctx, `update core_deployments set state='warming',updated_at=now() where id=$1`, id)
		return e
	}
	if state == "warming" {
		if time.Since(p.Started) > 30*time.Minute {
			_, e = r.database().Exec(ctx, `update core_deployments set state='failed',error='warming timeout',updated_at=now() where id=$1`, id)
			return e
		}
		targetReady := false
		for _, instance := range instances {
			if instance.Deployment != nil && *instance.Deployment == id {
				if instance.Heartbeat == nil || time.Since(*instance.Heartbeat) > 30*time.Second || instance.State != "running" || instance.Applied == nil || instance.Desired == nil || *instance.Applied != *instance.Desired {
					return nil
				}
				ctx2, cancel := context.WithTimeout(ctx, 5*time.Second)
				status, statusErr := r.Peer.Status(ctx2, instance.Address, instance.ID)
				ready := r.Peer.Ready(ctx2, instance.Address)
				cancel()
				if statusErr != nil || !ready {
					return nil
				}
				snapshot, snapshotErr := r.Config.Build(ctx, instance.ID, false)
				if snapshotErr != nil || !snapshot.Matches(status) {
					return nil
				}
				targetReady = true
			}
		}
		if targetReady {
			p.Stage = 0
			p.Started = time.Now().UTC()
			next, _ := json.Marshal(p)
			_, e = r.database().Exec(ctx, `update core_deployments set state='ready',progress=$2,updated_at=now() where id=$1`, id, next)
		}
		return e
	}
	if state == "rolling_back" {
		if e = r.restoreBaseline(ctx, p, instances); e != nil {
			return e
		}
		if e = r.applyWeights(ctx, id, p.weights(0)); e != nil {
			return e
		}
		return r.finishDrain(ctx, id, p.Deadline)
	}
	if state == "ready" {
		// Warm instances become eligible at stage 0. With auto-advance the
		// control plane enters the first weight stage itself once the same
		// minimum dwell a manual shift would wait has elapsed.
		if !p.autoAdvance() || !p.canAdvance(time.Now()) {
			return nil
		}
		return r.applyStage(ctx, id, &p)
	}
	if state != "shifting" && state != "draining_old" {
		return nil
	}
	if p.Stage < len(stages)-1 && !p.canAdvance(time.Now()) {
		return nil
	}
	if p.Stage >= len(stages)-1 {
		return r.finishDrain(ctx, id, p.Deadline)
	}
	return r.applyStage(ctx, id, &p)
}

// applyStage applies the next weight stage of a ready or shifting deployment and
// persists the state transition. Applying weights can fail admission; the
// deployment then rolls back to the recorded baseline while the warmed instances
// and their state directories are retained for inspection.
func (r *Runtime) applyStage(ctx context.Context, id string, p *progress) error {
	next := p.Stage + 1
	if next <= 0 || next >= len(stages) {
		return ErrConflict
	}
	weights := p.weights(next)
	if e := r.applyWeights(ctx, id, weights); e != nil {
		return errors.Join(e, r.rollback(ctx, id, "system"))
	}
	p.Stage = next
	p.Started = time.Now().UTC()
	raw, _ := json.Marshal(p)
	state := "shifting"
	if stages[next] == 100 {
		state = "draining_old"
	}
	_, e := r.database().Exec(ctx, `update core_deployments set state=$2,target_weights=$3,progress=$4,updated_at=now() where id=$1`, id, state, weights, raw)
	return e
}

func (r *Runtime) applyWeights(ctx context.Context, deployment string, weights map[string]int) error {
	tx, e := r.database().Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	for id, weight := range weights {
		if weight < 0 || weight > 10000 {
			return ErrInvalid
		}
		tag, updateErr := tx.Exec(ctx, `update core_instances set weight=$2,updated_at=now() where id=$1`, id, weight)
		if updateErr != nil || tag.RowsAffected() != 1 {
			return errors.Join(ErrUnknown, updateErr)
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	// A positive desired weight requires live, compatible and current configuration.
	instances, e := r.instances(ctx)
	if e != nil {
		return e
	}
	admitted, e := r.Admit(ctx, instances)
	if e != nil {
		return e
	}
	eligible := map[string]bool{}
	for _, b := range admitted {
		eligible[b.Address] = true
	}
	for _, i := range instances {
		if weights[i.ID] > 0 && !eligible[i.Address] {
			return ErrConflict
		}
	}
	return r.ApplyTraffic(ctx, true)
}

func (r *Runtime) shift(ctx context.Context, id string, weights map[string]int) error {
	r.deployMu.Lock()
	defer r.deployMu.Unlock()
	if !UUID(id, false) || len(weights) == 0 {
		return ErrInvalid
	}
	var state string
	var raw []byte
	e := r.database().QueryRow(ctx, `select state,progress from core_deployments where id=$1`, id).Scan(&state, &raw)
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrUnknown
	}
	if e != nil {
		return e
	}
	if state != "ready" && state != "shifting" {
		return ErrConflict
	}
	var p progress
	if e = json.Unmarshal(raw, &p); e != nil {
		return e
	}
	if !p.canAdvance(time.Now()) {
		return ErrConflict
	}
	instances, e := r.instances(ctx)
	if e != nil {
		return e
	}
	expected := p.weights(p.Stage + 1)
	// The public contract accepts version weights; resolve them only against
	// instances in this deployment. Another instance may run the same version
	// (for example the baseline during a first rollout) and must not absorb it.
	resolved := map[string]int{}
	for key, weight := range weights {
		if weight < 0 || weight > 100 {
			return ErrInvalid
		}
		ids := []string{}
		for _, i := range instances {
			if _, ok := expected[i.ID]; !ok {
				continue
			}
			if i.ID == key || i.Version == key {
				ids = append(ids, i.ID)
			}
		}
		if len(ids) == 0 {
			return ErrUnknown
		}
		for n, id := range ids {
			resolved[id] = weight / len(ids)
			if n < weight%len(ids) {
				resolved[id]++
			}
		}
	}
	if len(resolved) != len(expected) {
		return ErrInvalid
	}
	for key, weight := range expected {
		v, ok := resolved[key]
		if !ok || v != weight {
			return ErrInvalid
		}
	}
	return r.applyStage(ctx, id, &p)
}

func (r *Runtime) Rollback(ctx context.Context, id, actor string) error {
	r.deployMu.Lock()
	defer r.deployMu.Unlock()
	return r.rollback(ctx, id, actor)
}

func (r *Runtime) rollback(ctx context.Context, id, actor string) error {
	var state string
	var raw []byte
	e := r.database().QueryRow(ctx, `select state,progress from core_deployments where id=$1`, id).Scan(&state, &raw)
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrUnknown
	}
	if e != nil {
		return e
	}
	if state == "completed" || state == "rolled_back" {
		return ErrConflict
	}
	var p progress
	if e = json.Unmarshal(raw, &p); e != nil {
		return e
	}
	weights := p.weights(0)
	if _, e = r.database().Exec(ctx, `update core_deployments set state='rolling_back',updated_at=now() where id=$1`, id); e != nil {
		return e
	}

	instances, e := r.instances(ctx)
	if e != nil {
		return e
	}
	if e = r.restoreBaseline(ctx, p, instances); e != nil {
		return e
	}
	if e = r.applyWeights(ctx, id, weights); e != nil {
		return e
	}
	if _, e = r.database().Exec(ctx, `update core_deployments set state='rolling_back',updated_at=now() where id=$1`, id); e != nil {
		return e
	}
	return r.Audit(ctx, actor, "core.deployment.rollback", "core_deployment", id, map[string]any{"restored_weights": weights})
}

// finishDrain only inspects the deployment's retiring instances. Caddy counters
// never establish drain completion; observations come from authenticated cores.
func (r *Runtime) finishDrain(ctx context.Context, id string, deadline int) error {
	var state string
	var raw []byte
	if e := r.database().QueryRow(ctx, `select state,progress from core_deployments where id=$1`, id).Scan(&state, &raw); e != nil {
		return e
	}
	var p progress
	if e := json.Unmarshal(raw, &p); e != nil {
		return e
	}
	retiring := p.From
	if state == "rolling_back" {
		retiring = p.To
	}
	instances, e := r.instances(ctx)
	if e != nil {
		return e
	}
	all := true
	for _, i := range instances {
		if _, ok := retiring[i.ID]; !ok {
			continue
		}
		if i.State == "stopped" || i.State == "pruned" {
			continue
		}
		if i.DrainStarted == nil {
			if e = r.ApplyTraffic(ctx, true); e != nil {
				return e
			}
			if _, e = r.database().Exec(ctx, `update core_instances set state='draining',weight=0,zero_samples=0,draining_started_at=now() where id=$1`, i.ID); e != nil {
				return e
			}
			all = false
			continue
		}
		switch drainDecision(i, time.Now(), time.Duration(deadline)*time.Second) {
		case "timeout":
			_, e = r.database().Exec(ctx, `update core_deployments set state='failed',error='drain timeout; state retained for compatible restart and spool replay',updated_at=now() where id=$1`, id)
			return errors.Join(ErrConflict, e)
		case "wait":
			all = false
			continue
		}
		if e = r.stopInstance(ctx, i); e != nil {
			return e
		}
	}
	if !all {
		return nil
	}
	final := "completed"
	if state == "rolling_back" {
		final = "rolled_back"
	}
	_, e = r.database().Exec(ctx, `update core_deployments set state=$2,updated_at=now() where id=$1`, id, final)
	return e
}

func (r *Runtime) stopInstance(ctx context.Context, i instanceRow) error {
	if _, e := r.database().Exec(ctx, `update core_instances set state='stopping',weight=0 where id=$1`, i.ID); e != nil {
		return e
	}
	if i.ContainerID != nil {
		if e := r.Docker.Stop(ctx, *i.ContainerID, 30*time.Second); e != nil {
			_, persistErr := r.database().Exec(ctx, `update core_instances set state='failed',lifecycle_error=$2 where id=$1`, i.ID, e.Error())
			return errors.Join(e, persistErr)
		}
	}
	// stopped_at anchors the automatic-destruction grace window. A later
	// observation or a re-stop never rewrites the first stop.
	_, e := r.database().Exec(ctx, `update core_instances set state='stopped',stopped_at=coalesce(stopped_at,now()),updated_at=now() where id=$1`, i.ID)
	return e
}

func (r *Runtime) Drain(ctx context.Context, id, actor string, force bool, reason *string) error {
	r.deployMu.Lock()
	defer r.deployMu.Unlock()
	if !UUID(id, false) || force && (reason == nil || *reason == "") {
		return ErrInvalid
	}
	var row instanceRow
	row, e := scanInstance(r.database().QueryRow(ctx, instanceSelect+` where i.id=$1`, id))
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrUnknown
	}
	if e != nil {
		return e
	}
	if row.State == "stopped" {
		return nil
	}
	if row.State != "draining" {
		if _, e = r.database().Exec(ctx, `update core_instances set weight=0,state='draining',zero_samples=0,draining_started_at=now() where id=$1`, id); e != nil {
			return e
		}
		if e = r.ApplyTraffic(ctx, true); e != nil {
			return e
		}
		if !force {
			return nil
		}
	}
	if !force && drainDecision(row, time.Now(), 30*time.Minute) != "stop" {
		return ErrConflict
	}

	if force {
		if e = r.Audit(ctx, actor, "core.instance.force_drain", "core_instance", id, map[string]any{"inflight": row.Inflight, "spool_unacked": row.Spool, "reason": reason}); e != nil {
			return e
		}
	}
	if e = r.stopInstance(ctx, row); e != nil {
		return e
	}

	return r.Audit(ctx, actor, "core.instance.drain", "core_instance", id, map[string]any{"force": force, "reason": reason, "spool_unacked": row.Spool})
}

func (r *Runtime) Prune(ctx context.Context, actor string, ids []string, force bool) error {
	if len(ids) == 0 {
		return ErrInvalid
	}
	r.deployMu.Lock()
	defer r.deployMu.Unlock()
	for _, id := range ids {
		if !UUID(id, false) {
			return ErrInvalid
		}
		c, e := scanPruneCandidate(r.database().QueryRow(ctx, pruneCandidateSelect+` where i.id=$1`, id))
		if errors.Is(e, pgx.ErrNoRows) {
			return ErrUnknown
		}
		if e != nil {
			return e
		}
		if c.State == "pruned" {
			continue
		}
		if c.State != "stopped" || c.Weight != 0 {
			return ErrConflict
		}
		proof := manualZeroProof(c)
		if force {
			if e = r.forcePrune(ctx, actor, c, proof); e != nil {
				return e
			}
			continue
		}
		if !proof {
			return ErrSpoolNotDrained
		}
		if e = r.destroyInstance(ctx, actor, c, time.Now().UTC(), false); e != nil {
			return e
		}
	}
	return nil
}

// forcePrune deletes only the container and keeps the state directory until a
// zero observation confirms the spool is empty. It is the single exception to
// the zero-proof rule, and the audit records that the directory was retained.
// When the proof is already present the instance is destroyed normally, so the
// explicit flag never wastes the retained directory.
func (r *Runtime) forcePrune(ctx context.Context, actor string, c pruneCandidate, proof bool) error {
	if proof {
		return r.destroyInstance(ctx, actor, c, time.Now().UTC(), false)
	}
	if c.ContainerID != nil {
		if e := r.Docker.Remove(ctx, *c.ContainerID, false); e != nil {
			return e
		}
	}
	if _, e := r.database().Exec(ctx, `update core_instances set container_id=null,
 lifecycle_error='force prune removed container; state directory retained until spool zero',updated_at=now() where id=$1`, c.ID); e != nil {
		return e
	}
	return r.Audit(ctx, actor, "core.instance.prune", "core_instance", c.ID, map[string]any{
		"force": true, "automatic": false, "spool_unacked": c.Spool, "last_ack_seq": c.Ack, "state_dir_removed": false})
}

func deploymentList(ctx context.Context, r *Runtime) ([]Deployment, error) {
	rows, e := r.database().Query(ctx, `select d.id::text,d.from_version_id::text,v.version,d.state,d.target_weights,d.progress,d.error,d.created_at,d.updated_at from core_deployments d join core_versions v on v.id=d.to_version_id order by d.created_at desc limit 100`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Deployment{}
	for rows.Next() {
		var d Deployment
		var raw []byte
		if e = rows.Scan(&d.ID, &d.FromVersion, &d.ToVersion, &d.State, &d.Target, &raw, &d.Error, &d.Created, &d.Updated); e != nil {
			return nil, e
		}
		setNextWeights(&d, raw)
		out = append(out, d)
	}
	return out, rows.Err()
}
func getDeployment(ctx context.Context, r *Runtime, id string) (Deployment, error) {
	var d Deployment
	var raw []byte
	e := r.database().QueryRow(ctx, `select d.id::text,d.from_version_id::text,v.version,d.state,d.target_weights,d.progress,d.error,d.created_at,d.updated_at from core_deployments d join core_versions v on v.id=d.to_version_id where d.id=$1`, id).Scan(&d.ID, &d.FromVersion, &d.ToVersion, &d.State, &d.Target, &raw, &d.Error, &d.Created, &d.Updated)
	if errors.Is(e, pgx.ErrNoRows) {
		return d, ErrUnknown
	}
	if e != nil {
		return d, e
	}
	setNextWeights(&d, raw)
	return d, nil
}

// setNextWeights hydrates deployment responses from the stored progress record.
// It exposes the stage the next shift will apply; the shift API validates
// submitted weights against this exact stage, so clients must not have to
// reconstruct the stage progression themselves. It also reports the stored
// auto-advance policy for every state.
func setNextWeights(d *Deployment, raw []byte) {
	var p progress
	if json.Unmarshal(raw, &p) != nil {
		return
	}
	d.AutoAdvance = p.autoAdvance()
	if d.State != "ready" && d.State != "shifting" {
		return
	}
	if p.Stage < 0 || p.Stage+1 >= len(stages) {
		return
	}
	d.Next = p.weights(p.Stage + 1)
}

func (r *Runtime) restoreBaseline(ctx context.Context, p progress, instances []instanceRow) error {
	for id := range p.From {
		found := false
		for _, i := range instances {
			if i.ID != id {
				continue
			}
			found = true
			if i.State == "draining" {
				if _, e := r.database().Exec(ctx, `update core_instances set state='running',draining_started_at=null where id=$1`, id); e != nil {
					return e
				}
			}
			if i.State == "stopped" || i.State == "failed" {
				// The instance returns to service, so its drain anchor, spool proof
				// and cleanup block describe a previous life and must not survive
				// the revival.
				if _, e := r.database().Exec(ctx, `update core_instances set draining_started_at=null,spool_zero_at=null,cleanup_blocked_at=null where id=$1`, id); e != nil {
					return e
				}
				// Rollback branches on state-directory existence, not container
				// existence: a retained directory is reused and replayed even when
				// a forced prune removed the container, while a fresh directory is
				// created only after the original was confirmed destroyed (which
				// implies a confirmed spool zero).
				exists, e := r.Docker.StateDirExists(ctx, r.StateHostDir, id)
				if e != nil {
					return e
				}
				if !exists {
					if e = r.Docker.EnsureStateDir(ctx, r.StateHostDir, id); e != nil {
						return e
					}
				}
				if _, e := r.database().Exec(ctx, `update core_instances set state='pending',weight=0,config_revision_applied=null,last_controlled_at=null where id=$1`, id); e != nil {
					return e
				}
				if i.ContainerID != nil {
					e = r.Docker.Start(ctx, *i.ContainerID)
				} else {
					e = r.restoreContainer(ctx, i)
				}
				if e != nil {
					return e
				}
				return ErrConflict // reconciliation and heartbeats must warm it before restoring traffic
			}
		}
		if !found {
			return ErrConflict
		}
	}
	return nil
}

// restoreContainer recreates a retained instance's container from its recorded
// image identity so a compatible core can replay the preserved state directory.
func (r *Runtime) restoreContainer(ctx context.Context, i instanceRow) error {
	var image string
	var digest *string
	if e := r.database().QueryRow(ctx, `select image_ref,image_digest from core_versions where id=$1`, i.VersionID).Scan(&image, &digest); e != nil {
		return e
	}
	ref, e := ParseImageReference(image)
	if e != nil {
		if image, e = VersionImageRef(r.CoreImage, i.Version, digest); e != nil {
			return e
		}
	} else if digest != nil && ref.Digest == "" {
		image = ref.WithDigest(*digest).String()
	}
	var deployment string
	if i.Deployment != nil {
		deployment = *i.Deployment
	}
	spec := ContainerSpec{Instance: i.ID, Version: i.Version, Deployment: deployment, Name: i.Name, Image: image,
		Project: r.Project, Networks: r.Networks, StateHostDir: r.StateHostDir, ControlURL: r.CoreURL, Token: r.Token,
		SigningKey: r.SigningKey, SignatureMode: r.SignatureMode, CacheKey: r.CacheKey, AllowPrivateUpstreams: r.AllowPrivateUpstreams}
	if i.Name == "" {
		spec.Name = "token-ne-core-" + slug(i.Version) + "-" + i.ID[:8]
	}
	container, e := r.Docker.Create(ctx, spec)
	if e != nil {
		return e
	}
	if _, e = r.database().Exec(ctx, `update core_instances set container_id=$2,container_name=$3,address=$3||':8080' where id=$1`, i.ID, container.ID, spec.Name); e != nil {
		return e
	}
	return r.Docker.Start(ctx, container.ID)
}
