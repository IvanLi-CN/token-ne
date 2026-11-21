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
	Error       *string        `json:"error"`
	Created     time.Time      `json:"created_at"`
	Updated     time.Time      `json:"updated_at"`
}

func (r *Runtime) Deploy(ctx context.Context, actor string, req DeploymentRequest) (Deployment, error) {
	r.lifecycle.Lock()
	defer r.lifecycle.Unlock()
	if req.Replicas < 1 || req.Replicas > 10 || req.InitialWeight != 0 {
		return Deployment{}, ErrInvalid
	}
	tx, e := r.database().Begin(ctx)
	if e != nil {
		return Deployment{}, e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `select pg_advisory_xact_lock(4927816043)`); e != nil {
		return Deployment{}, e
	}
	var id, versionID, image string
	var protocol, min, max int
	e = tx.QueryRow(ctx, `select id::text,image_ref,protocol_version,config_schema_min,config_schema_max from core_versions where version=$1 for share`, req.Version).Scan(&versionID, &image, &protocol, &min, &max)
	if errors.Is(e, pgx.ErrNoRows) {
		return Deployment{}, ErrUnknown
	}
	if e != nil {
		return Deployment{}, e
	}
	if protocol != Protocol || min > Schema || max < Schema {
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
	p := progress{Stage: -1, From: fromWeights, To: weights, Started: now, Minimum: 60, Deadline: 1800}
	raw, _ := json.Marshal(p)
	if _, e = tx.Exec(ctx, `update core_deployments set progress=$2 where id=$1`, id, raw); e != nil {
		return Deployment{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return Deployment{}, e
	}
	created := []string{}
	for _, spec := range specs {
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
		return Deployment{}, e
	}

	if e = r.Audit(ctx, actor, "core.deploy", "core_deployment", id, map[string]any{"to_version": req.Version, "replicas": req.Replicas, "initial_weight": 0}); e != nil {
		return Deployment{}, e
	}
	var updated Deployment
	e = r.database().QueryRow(ctx, `select id::text,from_version_id::text,(select version from core_versions where id=to_version_id),state,target_weights,error,created_at,updated_at from core_deployments where id=$1`, id).Scan(&updated.ID, &updated.FromVersion, &updated.ToVersion, &updated.State, &updated.Target, &updated.Error, &updated.Created, &updated.Updated)
	return updated, e
}

func (r *Runtime) failDeploy(ctx context.Context, id string, containers []string, cause error) error {
	for _, container := range containers {
		_ = r.Docker.Stop(ctx, container, 5*time.Second)
	}
	_, _ = r.database().Exec(ctx, `update core_instances set state='failed',weight=0,lifecycle_error=$2 where deployment_id=$1`, id, cause.Error())
	_, _ = r.database().Exec(ctx, `update core_deployments set state='failed',error=$2,updated_at=now() where id=$1`, id, cause.Error())
	return cause
}

func (r *Runtime) advance(ctx context.Context) error {
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
	if state != "shifting" && state != "draining_old" {
		return nil
	}
	if p.Stage < len(stages)-1 && !p.canAdvance(time.Now()) {
		return nil
	}
	if p.Stage >= len(stages)-1 {
		return r.finishDrain(ctx, id, p.Deadline)
	}
	next := p.Stage + 1
	fraction := stages[next]
	weights := map[string]int{}
	for key, weight := range p.From {
		weights[key] = weight * (100 - fraction) / 100
	}
	for key, weight := range p.To {
		weights[key] = weight * fraction / 100
	}
	if e = r.applyWeights(ctx, id, weights); e != nil {
		return errors.Join(e, r.Rollback(ctx, id, "system"))
	}
	p.Stage = next
	p.Started = time.Now().UTC()
	raw, _ = json.Marshal(p)
	state = "shifting"
	if fraction == 100 {
		state = "draining_old"
	}
	_, e = r.database().Exec(ctx, `update core_deployments set state=$2,target_weights=$3,progress=$4,updated_at=now() where id=$1`, id, state, weights, raw)
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
	// The public contract accepts version weights; resolve them to instance IDs.
	resolved := map[string]int{}
	for key, weight := range weights {
		if weight < 0 || weight > 100 {
			return ErrInvalid
		}
		ids := []string{}
		for _, i := range instances {
			if i.ID == key || i.Version == key {
				ids = append(ids, i.ID)
			}
		}
		if len(ids) == 0 {
			return ErrUnknown
		}
		for n, key := range ids {
			resolved[key] = weight / len(ids)
			if n < weight%len(ids) {
				resolved[key]++
			}
		}
	}
	expected := p.weights(p.Stage + 1)
	if len(resolved) != len(expected) {
		return ErrInvalid
	}
	for key, weight := range expected {
		v, ok := resolved[key]
		if !ok || v != weight {
			return ErrInvalid
		}
	}
	if e = r.applyWeights(ctx, id, expected); e != nil {
		return errors.Join(e, r.Rollback(ctx, id, "system"))
	}
	p.Stage++
	p.Started = time.Now().UTC()
	raw, _ = json.Marshal(p)
	state = "shifting"
	if p.Stage == len(stages)-1 {
		state = "draining_old"
	}
	_, e = r.database().Exec(ctx, `update core_deployments set state=$2,target_weights=$3,progress=$4,updated_at=now() where id=$1`, id, state, expected, raw)
	return e
}

func (r *Runtime) Rollback(ctx context.Context, id, actor string) error {
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
	_, e := r.database().Exec(ctx, `update core_instances set state='stopped',updated_at=now() where id=$1`, i.ID)
	return e
}

func (r *Runtime) Drain(ctx context.Context, id, actor string, force bool, reason *string) error {
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

func (r *Runtime) Prune(ctx context.Context, actor string, ids []string) error {
	if len(ids) == 0 {
		return ErrInvalid
	}
	for _, id := range ids {
		if !UUID(id, false) {
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
		if !canPrune(row) {
			return ErrConflict
		}
		tx, e := r.database().Begin(ctx)
		if e != nil {
			return e
		}
		tag, claimErr := tx.Exec(ctx, `update core_instances set state='pruning' where id=$1 and state='stopped' and weight=0 and spool_unacked=0`, id)
		if claimErr != nil || tag.RowsAffected() != 1 {
			tx.Rollback(ctx)
			return errors.Join(ErrConflict, claimErr)
		}
		if e = tx.Commit(ctx); e != nil {
			return e
		}
		if row.ContainerID != nil {
			if e = r.Docker.Remove(ctx, *row.ContainerID, false); e != nil {
				_, persistErr := r.database().Exec(ctx, `update core_instances set state='failed',lifecycle_error=$2 where id=$1`, id, e.Error())
				return errors.Join(e, persistErr)
			}
		}
		// The API container intentionally has no host state-directory mount.
		// Prune removes the stopped container and retains the host directory.

		if _, e = r.database().Exec(ctx, `update core_instances set state='pruned',updated_at=now() where id=$1`, id); e != nil {
			return e
		}
		if e = r.Audit(ctx, actor, "core.instance.prune", "core_instance", id, map[string]any{"spool_unacked": *row.Spool, "last_ack_seq": row.Ack}); e != nil {
			return e
		}
	}
	return nil
}

func deploymentList(ctx context.Context, r *Runtime) ([]Deployment, error) {
	rows, e := r.database().Query(ctx, `select d.id::text,d.from_version_id::text,v.version,d.state,d.target_weights,d.error,d.created_at,d.updated_at from core_deployments d join core_versions v on v.id=d.to_version_id order by d.created_at desc limit 100`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Deployment{}
	for rows.Next() {
		var d Deployment
		if e = rows.Scan(&d.ID, &d.FromVersion, &d.ToVersion, &d.State, &d.Target, &d.Error, &d.Created, &d.Updated); e != nil {
			return nil, e
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
func getDeployment(ctx context.Context, r *Runtime, id string) (Deployment, error) {
	var d Deployment
	e := r.database().QueryRow(ctx, `select d.id::text,d.from_version_id::text,v.version,d.state,d.target_weights,d.error,d.created_at,d.updated_at from core_deployments d join core_versions v on v.id=d.to_version_id where d.id=$1`, id).Scan(&d.ID, &d.FromVersion, &d.ToVersion, &d.State, &d.Target, &d.Error, &d.Created, &d.Updated)
	if errors.Is(e, pgx.ErrNoRows) {
		return d, ErrUnknown
	}
	return d, e
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
				if i.ContainerID == nil {
					return ErrConflict
				}
				if _, e := r.database().Exec(ctx, `update core_instances set state='pending',weight=0,config_revision_applied=null,last_controlled_at=null where id=$1`, id); e != nil {
					return e
				}
				if e := r.Docker.Start(ctx, *i.ContainerID); e != nil {
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
