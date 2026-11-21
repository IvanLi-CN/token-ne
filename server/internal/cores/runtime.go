package cores

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/IvanLi-CN/token-ne/server/internal/caddy"
	"github.com/IvanLi-CN/token-ne/server/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Runtime struct {
	DB                    *store.Store
	Config                SnapshotBuilder
	Pool                  Database
	Peer                  Peer
	Docker                Docker
	Caddy                 Traffic
	Project               string
	Networks              []string
	StateHostDir          string
	CoreURL               string
	Token                 string
	SigningKey            string
	SignatureMode         string
	CacheKey              string
	AllowPrivateUpstreams string
	mu                    sync.Mutex
	lifecycle             sync.Mutex
}

type Target struct {
	Version   string  `json:"version"`
	Image     string  `json:"image_ref"`
	Digest    *string `json:"image_digest"`
	Protocol  int     `json:"protocol_version"`
	SchemaMin int     `json:"config_schema_min"`
	SchemaMax int     `json:"config_schema_max"`
	Commit    *string `json:"build_commit"`
}
type DeploymentRequest struct {
	Version       string `json:"to_version"`
	Replicas      int    `json:"replicas"`
	InitialWeight int    `json:"initial_weight"`
}
type ShiftRequest struct {
	Weights map[string]int `json:"weights"`
}
type DrainRequest struct {
	Force  bool    `json:"force"`
	Reason *string `json:"reason"`
}
type PruneRequest struct {
	Instances []string `json:"instance_ids"`
}

func (r *Runtime) Audit(ctx context.Context, actor, action, kind, id string, payload any) error {
	raw, e := json.Marshal(payload)
	if e != nil {
		return e
	}
	_, e = r.database().Exec(ctx, `insert into audit_events(id,actor,action,target_type,target_id,payload) values($1,$2,$3,$4,$5,$6)`, uuid.NewString(), actor, action, kind, id, raw)
	return e
}

func (r *Runtime) RegisterVersion(ctx context.Context, v Target) (map[string]any, error) {
	if strings.TrimSpace(v.Version) == "" || len(v.Version) > 128 || v.Image == "" || v.Protocol != Protocol ||
		v.SchemaMin < 1 || v.SchemaMin > Schema || v.SchemaMax < Schema ||
		strings.Contains(strings.ToLower(v.Image), ":latest") || v.Digest != nil && !strings.HasPrefix(*v.Digest, "sha256:") {
		return nil, ErrInvalid
	}
	id := uuid.NewString()
	var result map[string]any
	var versionID string
	var imageDigest any = v.Digest
	if v.Digest != nil {
		v.Image = strings.TrimSuffix(v.Image, "@"+*v.Digest) + "@" + *v.Digest
	}
	var e error
	e = r.database().QueryRow(ctx, `insert into core_versions(id,version,image_ref,image_digest,protocol_version,config_schema_min,config_schema_max,build_commit)
 values($1,$2,$3,$4,$5,$6,$7,$8) on conflict(version) do update set version=excluded.version
 where core_versions.image_ref=excluded.image_ref and core_versions.image_digest is not distinct from excluded.image_digest
 and core_versions.protocol_version=excluded.protocol_version and core_versions.config_schema_min=excluded.config_schema_min and core_versions.config_schema_max=excluded.config_schema_max
 returning id::text`, id, v.Version, v.Image, imageDigest, v.Protocol, v.SchemaMin, v.SchemaMax, v.Commit).Scan(&versionID)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, ErrConflict
	}
	if e != nil {
		return nil, e
	}
	result = map[string]any{"id": versionID, "version": v.Version, "image_ref": v.Image, "image_digest": imageDigest, "protocol_version": v.Protocol, "config_schema_min": v.SchemaMin, "config_schema_max": v.SchemaMax, "build_commit": v.Commit, "created_at": time.Now().UTC()}
	return result, nil
}

type instanceRow struct {
	ID           string          `json:"id"`
	Version      string          `json:"version"`
	VersionID    string          `json:"version_id"`
	ContainerID  *string         `json:"container_id"`
	Name         string          `json:"container_name"`
	Address      string          `json:"address"`
	Weight       int             `json:"weight"`
	State        string          `json:"state"`
	Inflight     int64           `json:"inflight"`
	Desired      *int64          `json:"config_revision_desired"`
	Applied      *int64          `json:"config_revision_applied"`
	DesiredHash  *string         `json:"config_hash_desired"`
	Controlled   *time.Time      `json:"last_controlled_at"`
	Heartbeat    *time.Time      `json:"last_heartbeat_at"`
	Status       json.RawMessage `json:"control_status"`
	Observed     *time.Time      `json:"observed_at"`
	Spool        *int64          `json:"spool_unacked"`
	Ack          *int64          `json:"last_ack_seq"`
	Deployment   *string         `json:"deployment_id"`
	ZeroSamples  int             `json:"zero_samples"`
	DrainStarted *time.Time      `json:"draining_started_at"`
}

func scanInstance(row pgx.Row) (v instanceRow, e error) {
	e = row.Scan(&v.ID, &v.Version, &v.VersionID, &v.ContainerID, &v.Name, &v.Address, &v.Weight, &v.State, &v.Inflight, &v.Desired, &v.Applied, &v.DesiredHash, &v.Controlled, &v.Heartbeat, &v.Status, &v.Observed, &v.Spool, &v.Ack, &v.Deployment, &v.ZeroSamples, &v.DrainStarted)
	return
}

const instanceSelect = `select i.id::text,v.version,v.id::text,i.container_id,i.container_name,i.address,i.weight,i.state,i.inflight,
 i.config_revision_desired,i.config_revision_applied,i.config_hash_desired,i.last_controlled_at,i.last_heartbeat_at,i.control_status,
 i.observed_at,i.spool_unacked,nullif(i.control_status->'spool'->>'last_ack_seq','')::bigint,i.deployment_id::text,i.zero_samples,i.draining_started_at
 from core_instances i join core_versions v on v.id=i.version_id`

func (r *Runtime) instances(ctx context.Context) ([]instanceRow, error) {
	rows, e := r.database().Query(ctx, instanceSelect+" order by i.created_at,i.id")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []instanceRow{}
	for rows.Next() {
		v, e := scanInstance(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (r *Runtime) Admit(ctx context.Context, instances []instanceRow) ([]caddy.Backend, error) {
	ready := []caddy.Backend{}
	var errorsSeen []error
	for _, i := range instances {
		if i.Weight < 0 || i.State != "running" || i.ContainerID == nil {
			continue
		}
		if i.Heartbeat == nil || time.Since(*i.Heartbeat) > 30*time.Second {
			continue
		}
		controlCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		live := r.Peer.Live(controlCtx, i.Address)
		readyNow := r.Peer.Ready(controlCtx, i.Address)
		status, e := r.Peer.Status(controlCtx, i.Address, i.ID)
		cancel()
		if !live || !readyNow || e != nil {
			continue
		}
		meta, metaErr := r.Peer.Meta(ctx, i.Address)
		if metaErr != nil || meta.Compatible() != nil || meta.Version != i.Version {
			continue
		}
		snapshot, e := r.Config.Build(ctx, i.ID, false)
		if e != nil {
			errorsSeen = append(errorsSeen, e)
			continue
		}
		if e = r.applySnapshot(ctx, i, snapshot, status); e != nil {
			errorsSeen = append(errorsSeen, e)
			continue
		}
		if !snapshot.Matches(status) {
			status, e = r.Peer.Status(ctx, i.Address, i.ID)
			if e != nil || !snapshot.Matches(status) {
				continue
			}
		}
		if status.Instance != i.ID || status.Version != i.Version || status.Draining == nil || *status.Draining {
			continue
		}
		tag, updateErr := r.database().Exec(ctx, `update core_instances set config_revision_applied=$2,last_controlled_at=$3 where id=$1 and config_revision_desired=$2 and config_hash_desired=$4`, i.ID, snapshot.Revision, status.ControlledAt, snapshot.Hash)
		if updateErr != nil || tag.RowsAffected() != 1 {
			errorsSeen = append(errorsSeen, errors.Join(ErrConflict, updateErr))
			continue
		}
		ready = append(ready, caddy.Backend{Address: i.Address, Weight: i.Weight})
	}
	if len(errorsSeen) > 0 {
		return ready, errors.Join(errorsSeen...)
	}
	return ready, nil
}

func (r *Runtime) applySnapshot(ctx context.Context, i instanceRow, s Snapshot, status Status) error {
	if status.Config == nil || status.Config.Revision < s.Revision || status.Config.Hash != s.Hash || status.Config.Source != "control_plane" ||
		status.Config.Override == nil || *status.Config.Override || status.ControlledAt == nil {
		if err := r.Peer.Push(ctx, i.Address, i.ID, s); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runtime) ApplyTraffic(ctx context.Context, force bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.applyTraffic(ctx, force)
}

func (r *Runtime) applyTraffic(ctx context.Context, force bool) error {
	instances, e := r.instances(ctx)
	if e != nil {
		return e
	}
	backends, e := r.Admit(ctx, instances)
	if e != nil {
		// Configuration failures deny traffic only for affected instances; other
		// qualified cores remain eligible, and a failed Caddy apply remains visible.
	}
	var refresh bool
	if scanErr := r.database().QueryRow(ctx, `select exists(select 1 from core_instances where lifecycle_error='reload Caddy DNS')`).Scan(&refresh); scanErr != nil {
		return scanErr
	}
	applyErr := r.Caddy.Apply(ctx, backends, force || refresh)
	if applyErr == nil && refresh {
		_, applyErr = r.database().Exec(ctx, `update core_instances set lifecycle_error=null where lifecycle_error='reload Caddy DNS'`)
	}
	return errors.Join(e, applyErr)
}

func (r *Runtime) Heartbeat(ctx context.Context, id string, h Heartbeat) error {
	if !UUID(id, false) || h.Protocol != Protocol || h.Type != "heartbeat" || !UUID(h.ID, true) ||
		h.SentAt.After(time.Now().Add(5*time.Minute)) || h.SentAt.Before(time.Now().Add(-24*time.Hour)) ||
		h.Status.Instance != id || h.Status.Validate(time.Now()) != nil {
		return ErrInvalid
	}
	raw, _ := json.Marshal(h.Status)
	tx, e := r.database().Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var version string
	e = tx.QueryRow(ctx, `select v.version from core_instances i join core_versions v on v.id=i.version_id where i.id=$1 for update of i`, id).Scan(&version)
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrUnknown
	}
	if e != nil {
		return e
	}
	if h.Status.Version != version {
		return ErrInvalid
	}
	var sent *time.Time
	if e = tx.QueryRow(ctx, `select heartbeat_sent_at from core_instances where id=$1`, id).Scan(&sent); e != nil {
		return e
	}
	if sent != nil && sent.Equal(h.SentAt) {
		return tx.Commit(ctx)
	}
	if sent != nil && h.SentAt.Before(*sent) {
		return nil
	}
	var desired *int64
	var expected *string
	if e = tx.QueryRow(ctx, `select config_revision_desired,config_hash_desired from core_instances where id=$1`, id).Scan(&desired, &expected); e != nil {
		return e
	}
	var applied any
	var controlled any
	if status := h.Status.Config; status != nil && status.Source == "control_plane" && status.Override != nil && !*status.Override &&
		h.Status.ControlledAt != nil && desired != nil && expected != nil && status.Revision == *desired && status.Hash == *expected {
		applied = status.Revision
		controlled = time.Now().UTC()
	}
	inflight := *h.Status.Inflight
	draining := *h.Status.Draining
	state := "running"
	if draining {
		state = "draining"
	}
	_, e = tx.Exec(ctx, `update core_instances set state=case when state in ('draining','stopping','stopped','pruning','pruned','failed') then state else $2 end,inflight=$3,config_revision_applied=coalesce($4,config_revision_applied),last_controlled_at=coalesce($5,last_controlled_at),
 control_status=$6,observed_at=now(),heartbeat_sent_at=$7,last_heartbeat_at=now(),spool_bytes=$8,spool_unacked=$9,
 zero_samples=case when $3=0 then zero_samples+1 else 0 end,draining_started_at=case when state='draining' or $2='draining' then coalesce(draining_started_at,now()) else null end,
 lifecycle_error=case when lifecycle_error='reload Caddy DNS' then lifecycle_error else null end where id=$1`, id, state, inflight, applied, controlled, raw, h.SentAt, *h.Status.Spool.Bytes, *h.Status.Spool.Unacked)
	if e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `insert into system_state(key,value) values($1,$2) on conflict(key) do update set value=excluded.value,updated_at=now()`,
		"core_status:"+id, raw); e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	return r.ApplyTraffic(ctx, false)
}

func (r *Runtime) Pull(ctx context.Context, instance string, etag string) (Snapshot, bool, error) {
	if !UUID(instance, false) {
		return Snapshot{}, false, ErrInvalid
	}
	var address string
	var desired, applied *int64
	var expected *string
	var controlled *time.Time
	e := r.database().QueryRow(ctx, `select address,config_revision_desired,config_revision_applied,config_hash_desired,last_controlled_at from core_instances where id=$1`, instance).Scan(&address, &desired, &applied, &expected, &controlled)
	if errors.Is(e, pgx.ErrNoRows) {
		return Snapshot{}, false, ErrUnknown
	}
	if e != nil {
		return Snapshot{}, false, e
	}
	s, e := r.Config.Build(ctx, instance, false)
	if e != nil {
		return Snapshot{}, false, e
	}
	if status, statusErr := r.Peer.Status(ctx, address, instance); statusErr == nil && s.Matches(status) &&
		desired != nil && applied != nil && expected != nil && *desired == s.Revision && *applied == s.Revision &&
		*expected == s.Hash && controlled != nil && etag == fmt.Sprintf("\"%d\"", s.Revision) {
		return s, true, nil
	}
	return s, false, nil
}

func (r *Runtime) PushAll(ctx context.Context) error {
	instances, e := r.instances(ctx)
	if e != nil {
		return e
	}
	var failures []error
	for _, i := range instances {
		if i.State != "running" || i.ContainerID == nil || i.Heartbeat == nil || time.Since(*i.Heartbeat) > 30*time.Second {
			continue
		}
		meta, metaErr := r.Peer.Meta(ctx, i.Address)
		if metaErr != nil || meta.Compatible() != nil || meta.Version != i.Version {
			continue
		}
		s, e := r.Config.Build(ctx, i.ID, false)
		if e != nil {
			failures = append(failures, e)
			continue
		}
		status, e := r.Peer.Status(ctx, i.Address, i.ID)
		if e != nil {
			continue
		}
		if status.Config != nil && status.Config.Source == "control_plane" && status.Config.Override != nil && !*status.Config.Override &&
			status.Config.Revision == s.Revision && status.Config.Hash == s.Hash {
			continue
		}
		if e = r.Peer.Push(ctx, i.Address, i.ID, s); e != nil {
			failures = append(failures, e)
			continue
		}
		status, statusErr := r.Peer.Status(ctx, i.Address, i.ID)
		if statusErr != nil || !s.Matches(status) {
			continue
		}
		_, e = r.database().Exec(ctx, `update core_instances set config_revision_applied=$2,last_controlled_at=coalesce($3,last_controlled_at) where id=$1 and config_revision_desired=$2 and config_hash_desired=$4`, i.ID, s.Revision, status.ControlledAt, s.Hash)
		if e != nil {
			failures = append(failures, e)
		}
	}
	return errors.Join(failures...)
}

func (r *Runtime) Run(ctx context.Context) {
	// Startup reconciliation is retried even when Docker is briefly unavailable.
	func() {
		delay := time.Second
		for ctx.Err() == nil {
			c, cancel := context.WithTimeout(ctx, 30*time.Second)
			e := r.Reconcile(c)
			cancel()
			if e == nil {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			if delay < 30*time.Second {
				delay *= 2
			}
		}
	}()
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			c, cancel := context.WithTimeout(ctx, 30*time.Second)
			operations := []struct {
				name string
				run  func(context.Context) error
			}{{"config", r.PushAll}, {"drain", r.observeDrains}, {"deployment", r.advance}, {"reconcile", r.Reconcile}}
			for _, op := range operations {
				if e := op.run(c); e != nil {
					slog.Warn("core control reconciliation failed", "operation", op.name, "error", e)
				}
			}

			cancel()
		}
	}
}

func (r *Runtime) Reconcile(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.reconcile(ctx); err != nil {
		return err
	}
	return r.applyTraffic(ctx, false)
}

func (r *Runtime) reconcile(ctx context.Context) error {
	r.lifecycle.Lock()
	defer r.lifecycle.Unlock()
	if r.Project == "" {
		hostname, e := os.Hostname()
		if e != nil {
			return e
		}
		self, e := r.Docker.Inspect(ctx, hostname)
		if e != nil {
			return e
		}
		r.Project = self.Labels["com.docker.compose.project"]
		if r.Project == "" {
			return errors.New("Compose project ownership could not be discovered")
		}
	}
	tx, e := r.database().Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `select pg_advisory_xact_lock(4927816043)`); e != nil {
		return e
	}
	containers, e := r.Docker.List(ctx)
	if e != nil {
		return e
	}
	byInstance := map[string]Container{}
	for _, c := range containers {
		if c.Labels[RoleLabel] != "core" || c.Labels["com.docker.compose.project"] != r.Project {
			continue
		}
		id := c.Labels[InstanceLabel]
		if !UUID(id, false) || byInstance[id].ID != "" {
			return ErrConflict
		}
		byInstance[id] = c
	}
	rows, e := tx.Query(ctx, `select i.id::text,i.version_id::text,v.version,i.container_id,i.container_name,i.state from core_instances i join core_versions v on v.id=i.version_id for update of i`)
	if e != nil {
		return e
	}
	type existing struct {
		id, versionID, version, name, state string
		containerID                         *string
	}
	existingRows := []existing{}
	for rows.Next() {
		var v existing
		if e = rows.Scan(&v.id, &v.versionID, &v.version, &v.containerID, &v.name, &v.state); e != nil {
			rows.Close()
			return e
		}
		existingRows = append(existingRows, v)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return e
	}
	for _, row := range existingRows {
		id, version, name, state, containerID := row.id, row.version, row.name, row.state, row.containerID
		c, exists := byInstance[id]
		if !exists {
			if state != "failed" && state != "stopped" && state != "pruned" && state != "pruning" {
				var image string
				if e = tx.QueryRow(ctx, `select image_ref from core_versions where id=$1`, row.versionID).Scan(&image); e != nil {
					return e
				}
				if _, e = tx.Exec(ctx, `update core_instances set state='pending',weight=0,config_revision_applied=null,last_controlled_at=null,lifecycle_error='reload Caddy DNS' where id=$1`, id); e != nil {
					return e
				}
				replacement, createErr := r.Docker.Create(ctx, ContainerSpec{Instance: id, Version: version, Name: name, Image: image, Project: r.Project, Networks: r.Networks, StateHostDir: r.StateHostDir, ControlURL: r.CoreURL, Token: r.Token, SigningKey: r.SigningKey, SignatureMode: r.SignatureMode, CacheKey: r.CacheKey, AllowPrivateUpstreams: r.AllowPrivateUpstreams})
				if createErr != nil {
					return createErr
				}
				if e = r.Docker.Start(ctx, replacement.ID); e != nil {
					return e
				}
				_, e = tx.Exec(ctx, `update core_instances set container_id=$2,state='pending',lifecycle_error='reload Caddy DNS' where id=$1`, id, replacement.ID)
			}
			if e != nil {
				rows.Close()
				return e
			}
			continue
		}
		delete(byInstance, id)
		if state == "pruned" || state == "pruning" {
			continue
		}
		if state == "stopped" && c.State == "running" {
			if e = r.Docker.Stop(ctx, c.ID, 30*time.Second); e != nil {
				return e
			}
			c.State = "exited"
		}
		containerID = &c.ID
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		if c.Labels[VersionLabel] != version {
			return ErrConflict
		}
		if shouldRestart(state, c.State) {
			if _, e = tx.Exec(ctx, `update core_instances set state='pending',weight=0,config_revision_applied=null,last_controlled_at=null,lifecycle_error='reload Caddy DNS' where id=$1`, id); e != nil {
				return e
			}
			if e = r.Docker.Start(ctx, c.ID); e != nil {
				return e
			}
			c.State = "running"
		}
		desired := "stopped"
		if c.State == "running" {
			desired = "running"
		}
		if _, e = tx.Exec(ctx, `update core_instances set container_id=$2,container_name=$3,address=$3||':8080',state=case when state in ('draining','stopping','pruning','pruned','failed') then state else $4 end,updated_at=now(),lifecycle_error=case when lifecycle_error='reload Caddy DNS' then lifecycle_error else nullif($5,'') end where id=$1`, id, containerID, name, desired, c.Status); e != nil {
			rows.Close()
			return e
		}
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return e
	}
	for id, c := range byInstance {
		version := c.Labels[VersionLabel]
		var versionID string
		e = tx.QueryRow(ctx, `select id::text from core_versions where version=$1`, version).Scan(&versionID)
		if errors.Is(e, pgx.ErrNoRows) {
			name := strings.TrimPrefix(c.Name, "/")
			if len(c.Names) > 0 {
				name = strings.TrimPrefix(c.Names[0], "/")
			}
			if c.State != "running" {
				if e = r.Docker.Start(ctx, c.ID); e != nil {
					return e
				}
			}
			meta, metaErr := r.Peer.Meta(ctx, name+":8080")
			if metaErr != nil {
				return metaErr
			}
			if meta.Compatible() != nil || meta.Version != version {
				return ErrCompatibility
			}
			versionID = uuid.NewString()
			_, e = tx.Exec(ctx, `insert into core_versions(id,version,image_ref,protocol_version,config_schema_min,config_schema_max,build_commit) values($1,$2,$3,$4,$5,$6,$7) on conflict(version) do nothing`, versionID, version, c.Image, meta.Protocol, meta.SchemaMin, meta.SchemaMax, meta.Build.Commit)
			if e == nil {
				e = tx.QueryRow(ctx, `select id::text from core_versions where version=$1`, version).Scan(&versionID)
			}
		}
		if e != nil {
			return e
		}
		name := strings.TrimPrefix(c.Name, "/")
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		if _, e = tx.Exec(ctx, `insert into core_instances(id,version_id,container_id,container_name,address,weight,state,inflight,state_dir,spool_unacked)
		values($1,$2,$3,$4,$4||':8080',0,'pending',0,$5,0) on conflict(id) do nothing`, id, versionID, c.ID, name, r.StateHostDir); e != nil {
			return e
		}
		if c.State != "running" {
			if e = r.Docker.Start(ctx, c.ID); e != nil {
				return e
			}
		}
	}
	return tx.Commit(ctx)
}

func (r *Runtime) Export(ctx context.Context, id string) ([]byte, error) {
	s, e := r.Config.Build(ctx, id, true)
	if e != nil {
		return nil, e
	}
	return s.Body, nil
}

func (r *Runtime) SaveExport(ctx context.Context, actor, id, path string) error {
	b, e := r.Export(ctx, id)
	if e != nil {
		return e
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	if _, e = f.Write(b); e == nil {
		e = f.Sync()
	}
	if closeErr := f.Close(); e == nil {
		e = closeErr
	}
	if e != nil {
		_ = os.Remove(path)
		return e
	}
	return r.Audit(ctx, actor, "core.config.export", "core_instance", id, map[string]any{"sha256": Hash(b), "path": filepath.Base(path)})
}

func ConstantToken(actual, expected string) bool {
	return expected != "" && len(actual) == len(expected) && subtle.ConstantTimeCompare([]byte(actual), []byte(expected)) == 1
}
func (r *Runtime) Shift(ctx context.Context, id string, weights map[string]int) error {
	return r.shift(ctx, id, weights)
}
func (r *Runtime) SortedInstances(ctx context.Context) ([]instanceRow, error) {
	list, e := r.instances(ctx)
	if e == nil {
		sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	}
	return list, e
}
