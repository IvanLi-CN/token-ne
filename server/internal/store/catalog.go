package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/IvanLi-CN/token-ne/server/internal/modelglob"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Phase-two catalog errors are distinct so the HTTP layer can answer with a
// dedicated code and a field path instead of a generic conflict.
var (
	ErrCatalogSyncRunning        = errors.New("catalog sync already running")
	ErrCatalogSyncExpired        = errors.New("catalog sync lease expired")
	ErrCapabilitiesRevisionStale = errors.New("capabilities revision is stale")
	ErrCatalogBindingConflict    = errors.New("catalog item or capability is bound to another model")
	ErrTargetModelExists         = errors.New("target model already exists")
	ErrModelNameTaken            = errors.New("model name is already used by a route rule")
	ErrUpstreamNameTaken         = errors.New("upstream name is already in use")
)

// Catalog sync states mirror the upstream_model_sync_runs check constraint.
const (
	CatalogSyncRunningState   = "running"
	CatalogSyncSucceededState = "succeeded"
	CatalogSyncFailedState    = "failed"
)

// Catalog import modes mirror the openapi UpstreamModelImportInput enum.
const (
	CatalogImportCreateModel  = "create_model"
	CatalogImportBindExisting = "bind_existing"
)

// catalogSyncLeaseMin is the shortest lease a claimed run keeps. The lease is
// stretched to cover the upstream's configured connect and first-byte
// timeouts, because the control plane applies results only while the lease is
// valid and a fetch cannot outlive its own timeouts.
const catalogSyncLeaseMin = 15 * time.Minute

// CatalogModel is one model discovered by a successful upstream directory
// fetch. ID is the upstream model identifier; the remaining fields are stored
// verbatim when the upstream provides them.
type CatalogModel struct {
	ID              string
	Object          *string
	OwnedBy         *string
	ProviderCreated *int64
}

// CatalogSyncRun is one directory sync attempt. LeaseUntil stays internal so
// the public shape matches the openapi UpstreamModelSyncRun schema.
type CatalogSyncRun struct {
	ID              string     `json:"id"`
	UpstreamID      string     `json:"upstream_id"`
	State           string     `json:"state"`
	StartedAt       time.Time  `json:"started_at"`
	FinishedAt      *time.Time `json:"finished_at"`
	DiscoveredCount int        `json:"discovered_count"`
	Error           *string    `json:"error"`
}

// CatalogSyncBatchItem is one batch sync decision: started carries the new run
// ID, skipped carries catalog_sync_running and never creates a run.
type CatalogSyncBatchItem struct {
	UpstreamID string  `json:"upstream_id"`
	State      string  `json:"state"`
	RunID      *string `json:"run_id"`
	Reason     *string `json:"reason"`
}

// CatalogItem is one row of the latest successful snapshot plus its binding.
// BoundModelID is the single global model bound to this catalog item, if any.
type CatalogItem struct {
	UpstreamID          string    `json:"upstream_id"`
	UpstreamModelID     string    `json:"upstream_model_id"`
	Object              *string   `json:"object"`
	OwnedBy             *string   `json:"owned_by"`
	ProviderCreated     *int64    `json:"provider_created"`
	FirstSeenAt         time.Time `json:"first_seen_at"`
	LastSeenAt          time.Time `json:"last_seen_at"`
	CurrentInLatestSync bool      `json:"current_in_latest_sync"`
	Stale               bool      `json:"stale"`
	BoundModelID        *string   `json:"bound_model_id"`
}

// CatalogSnapshot keeps the latest attempt and the last successful directory
// separate: a failed attempt must not hide the data administrators can bind.
type CatalogSnapshot struct {
	Data      []CatalogItem   `json:"data"`
	LatestRun *CatalogSyncRun `json:"latest_run"`
}

// CatalogImport is one explicit import or bind request. ExpectedRevision is
// optional: when supplied it must equal the upstream's current capabilities
// revision, otherwise the import is rejected without changes.
type CatalogImport struct {
	UpstreamModelID  string
	Mode             string
	ModelID          string
	ExpectedRevision *int64
}

// CatalogImportResult reports whether a capability row was created and the
// capabilities revision after the transaction committed.
type CatalogImportResult struct {
	ModelID              string `json:"model_id"`
	CapabilityCreated    bool   `json:"capability_created"`
	CapabilitiesRevision int64  `json:"capabilities_revision"`
}

const catalogRunColumns = "id::text,upstream_id::text,state,started_at,finished_at,discovered_count,error"

func scanCatalogRun(row pgx.Row) (CatalogSyncRun, error) {
	var r CatalogSyncRun
	e := row.Scan(&r.ID, &r.UpstreamID, &r.State, &r.StartedAt, &r.FinishedAt, &r.DiscoveredCount, &r.Error)
	return r, normalize(e)
}

func clipCatalogError(message string) string {
	runes := []rune(message)
	if len(runes) > 500 {
		return string(runes[:500])
	}
	return message
}

// StartCatalogSync claims the single running slot for one upstream. The
// upstream row lock serializes concurrent claims, so at most one caller sees a
// free slot. An expired running lease is failed first and then replaced, while
// a live lease is reported as ErrCatalogSyncRunning.
func (s *Store) StartCatalogSync(ctx context.Context, upstreamID string) (CatalogSyncRun, error) {
	var run CatalogSyncRun
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return run, e
	}
	defer tx.Rollback(ctx)

	var connect, first int
	if e = tx.QueryRow(ctx, "select connect_timeout_ms,first_byte_timeout_ms from upstreams where id=$1 for update", upstreamID).Scan(&connect, &first); e != nil {
		return run, normalize(e)
	}

	var currentRun string
	var leaseValid bool
	e = tx.QueryRow(ctx, `
select id::text, lease_until > now()
from upstream_model_sync_runs
where upstream_id=$1 and state='running'
for update`, upstreamID).Scan(&currentRun, &leaseValid)
	switch {
	case e == nil && leaseValid:
		return run, ErrCatalogSyncRunning
	case e == nil:
		if _, e = tx.Exec(ctx, `
update upstream_model_sync_runs
set state='failed', finished_at=now(), error='sync lease expired before the worker reported results'
where id=$1`, currentRun); e != nil {
			return run, normalize(e)
		}
	case errors.Is(e, pgx.ErrNoRows):
	default:
		return run, normalize(e)
	}

	lease := catalogSyncLeaseMin
	if d := time.Duration(connect+first)*time.Millisecond + 2*time.Minute; d > lease {
		lease = d
	}
	run, e = scanCatalogRun(tx.QueryRow(ctx, `
insert into upstream_model_sync_runs(id,upstream_id,state,started_at,lease_until)
values($1,$2,'running',now(),now()+$3::bigint*interval '1 millisecond')
returning `+catalogRunColumns, uuid.NewString(), upstreamID, lease.Milliseconds()))
	if e != nil {
		return run, e
	}
	return run, normalize(tx.Commit(ctx))
}

// StartCatalogSyncBatch claims every enabled upstream independently. A busy
// upstream is reported as skipped and never blocks the rest of the batch.
func (s *Store) StartCatalogSyncBatch(ctx context.Context) ([]CatalogSyncBatchItem, error) {
	rows, e := s.Pool.Query(ctx, "select id::text from upstreams where enabled order by created_at,id")
	if e != nil {
		return nil, e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return nil, e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}

	out := make([]CatalogSyncBatchItem, 0, len(ids))
	for _, id := range ids {
		run, err := s.StartCatalogSync(ctx, id)
		switch {
		case err == nil:
			out = append(out, CatalogSyncBatchItem{UpstreamID: id, State: "started", RunID: &run.ID})
		case errors.Is(err, ErrCatalogSyncRunning):
			reason := "catalog_sync_running"
			out = append(out, CatalogSyncBatchItem{UpstreamID: id, State: "skipped", Reason: &reason})
		case errors.Is(err, ErrNotFound):
			reason := "upstream_deleted"
			out = append(out, CatalogSyncBatchItem{UpstreamID: id, State: "skipped", Reason: &reason})
		default:
			return nil, err
		}
	}
	return out, nil
}

// CompleteCatalogSync applies the snapshot and the terminal state in one
// transaction. Both the upstream row lock and the run row lock are taken
// before the lease is rechecked, so a late worker cannot overwrite a newer
// successful directory: it may only record an error on its own run.
func (s *Store) CompleteCatalogSync(ctx context.Context, upstreamID, runID string, models []CatalogModel) (CatalogSyncRun, error) {
	var run CatalogSyncRun
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return run, e
	}
	defer tx.Rollback(ctx)

	var locked int
	if e = tx.QueryRow(ctx, "select 1 from upstreams where id=$1 for update", upstreamID).Scan(&locked); e != nil {
		return run, normalize(e)
	}
	var state string
	var leaseValid bool
	if e = tx.QueryRow(ctx, `
select state, lease_until > now()
from upstream_model_sync_runs
where upstream_id=$1 and id=$2
for update`, upstreamID, runID).Scan(&state, &leaseValid); e != nil {
		return run, normalize(e)
	}

	if state != CatalogSyncRunningState || !leaseValid {
		message := "late sync result discarded: run is no longer running with a valid lease"
		if state == CatalogSyncRunningState {
			if _, e = tx.Exec(ctx, `
update upstream_model_sync_runs
set state='failed', finished_at=now(), error=$3
where upstream_id=$1 and id=$2`, upstreamID, runID, message); e != nil {
				return run, normalize(e)
			}
		} else if _, e = tx.Exec(ctx, `
update upstream_model_sync_runs
set error=case when error is null or error='' then $3 else left(error || ' | ' || $3, 2000) end
where upstream_id=$1 and id=$2`, upstreamID, runID, message); e != nil {
			return run, normalize(e)
		}
		run, e = scanCatalogRun(tx.QueryRow(ctx, "select "+catalogRunColumns+" from upstream_model_sync_runs where upstream_id=$1 and id=$2", upstreamID, runID))
		if e != nil {
			return run, e
		}
		if e = tx.Commit(ctx); e != nil {
			return run, e
		}
		return run, ErrCatalogSyncExpired
	}

	if _, e = tx.Exec(ctx, "update upstream_model_catalog set current_in_latest_sync=false where upstream_id=$1 and current_in_latest_sync", upstreamID); e != nil {
		return run, normalize(e)
	}
	for _, m := range models {
		if _, e = tx.Exec(ctx, `
insert into upstream_model_catalog(upstream_id,upstream_model_id,object,owned_by,provider_created,first_seen_at,last_seen_at,current_in_latest_sync,stale_since,last_sync_id)
values($1,$2,$3,$4,$5,now(),now(),true,null,$6)
on conflict (upstream_id,upstream_model_id) do update
set object=excluded.object,owned_by=excluded.owned_by,provider_created=excluded.provider_created,
    last_seen_at=excluded.last_seen_at,current_in_latest_sync=true,stale_since=null,last_sync_id=excluded.last_sync_id`,
			upstreamID, m.ID, m.Object, m.OwnedBy, m.ProviderCreated, runID); e != nil {
			return run, normalize(e)
		}
	}
	if _, e = tx.Exec(ctx, `
update upstream_model_catalog
set stale_since=now()
where upstream_id=$1 and not current_in_latest_sync and stale_since is null`, upstreamID); e != nil {
		return run, normalize(e)
	}
	run, e = scanCatalogRun(tx.QueryRow(ctx, `
update upstream_model_sync_runs
set state='succeeded', finished_at=now(), discovered_count=$3, error=null
where upstream_id=$1 and id=$2
returning `+catalogRunColumns, upstreamID, runID, len(models)))
	if e != nil {
		return run, e
	}
	return run, normalize(tx.Commit(ctx))
}

// FailCatalogSync records a terminal failure and never touches the catalog, so
// the last successful snapshot and every routable capability survive a broken
// upstream.
func (s *Store) FailCatalogSync(ctx context.Context, upstreamID, runID, message string) (CatalogSyncRun, error) {
	var run CatalogSyncRun
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return run, e
	}
	defer tx.Rollback(ctx)

	var locked int
	if e = tx.QueryRow(ctx, "select 1 from upstreams where id=$1 for update", upstreamID).Scan(&locked); e != nil {
		return run, normalize(e)
	}
	var state string
	if e = tx.QueryRow(ctx, `
select state from upstream_model_sync_runs where upstream_id=$1 and id=$2 for update`, upstreamID, runID).Scan(&state); e != nil {
		return run, normalize(e)
	}
	if state == CatalogSyncRunningState {
		if _, e = tx.Exec(ctx, `
update upstream_model_sync_runs
set state='failed', finished_at=now(), error=$3
where upstream_id=$1 and id=$2`, upstreamID, runID, clipCatalogError(message)); e != nil {
			return run, normalize(e)
		}
	}
	run, e = scanCatalogRun(tx.QueryRow(ctx, "select "+catalogRunColumns+" from upstream_model_sync_runs where upstream_id=$1 and id=$2", upstreamID, runID))
	if e != nil {
		return run, e
	}
	return run, normalize(tx.Commit(ctx))
}

// UpstreamCatalog returns the latest attempt and the last successful snapshot,
// including stale rows. Rows missing from a successful sync keep their binding
// and only gain stale_since, so this read never hides a routable relation.
func (s *Store) UpstreamCatalog(ctx context.Context, upstreamID string) (CatalogSnapshot, error) {
	var exists bool
	if e := s.Pool.QueryRow(ctx, "select exists(select 1 from upstreams where id=$1)", upstreamID).Scan(&exists); e != nil {
		return CatalogSnapshot{}, normalize(e)
	} else if !exists {
		return CatalogSnapshot{}, ErrNotFound
	}
	out := CatalogSnapshot{Data: []CatalogItem{}}
	latest, e := scanCatalogRun(s.Pool.QueryRow(ctx, `
select `+catalogRunColumns+`
from upstream_model_sync_runs
where upstream_id=$1
order by started_at desc, id desc
limit 1`, upstreamID))
	if e == nil {
		out.LatestRun = &latest
	} else if !errors.Is(e, ErrNotFound) {
		return CatalogSnapshot{}, e
	}

	rows, e := s.Pool.Query(ctx, `
select c.upstream_id::text,c.upstream_model_id,c.object,c.owned_by,c.provider_created,
       c.first_seen_at,c.last_seen_at,c.current_in_latest_sync,c.stale_since is not null,um.model_id
from upstream_model_catalog c
left join upstream_models um on um.upstream_id=c.upstream_id and um.catalog_model_id=c.upstream_model_id
where c.upstream_id=$1
order by c.upstream_model_id`, upstreamID)
	if e != nil {
		return CatalogSnapshot{}, e
	}
	defer rows.Close()
	for rows.Next() {
		var item CatalogItem
		if e = rows.Scan(&item.UpstreamID, &item.UpstreamModelID, &item.Object, &item.OwnedBy, &item.ProviderCreated,
			&item.FirstSeenAt, &item.LastSeenAt, &item.CurrentInLatestSync, &item.Stale, &item.BoundModelID); e != nil {
			return CatalogSnapshot{}, e
		}
		out.Data = append(out.Data, item)
	}
	return out, rows.Err()
}

// CatalogRun reads one sync run scoped to its upstream.
func (s *Store) CatalogRun(ctx context.Context, upstreamID, runID string) (CatalogSyncRun, error) {
	return scanCatalogRun(s.Pool.QueryRow(ctx,
		"select "+catalogRunColumns+" from upstream_model_sync_runs where upstream_id=$1 and id=$2", upstreamID, runID))
}

// ImportCatalogModel creates a disabled model or attaches a disabled
// capability to an existing model. The upstream row lock, the name advisory
// lock and the catalog checks all run in one transaction with the capabilities
// revision bump, so a stale editor form cannot overwrite a concurrent import.
func (s *Store) ImportCatalogModel(ctx context.Context, upstreamID string, in CatalogImport, a Audit) (CatalogImportResult, error) {
	var out CatalogImportResult
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(ctx)

	var revision int64
	if e = tx.QueryRow(ctx, "select capabilities_revision from upstreams where id=$1 for update", upstreamID).Scan(&revision); e != nil {
		return out, normalize(e)
	}
	if in.ExpectedRevision != nil && *in.ExpectedRevision != revision {
		return out, ErrCapabilitiesRevisionStale
	}
	if _, e = tx.Exec(ctx, "select pg_advisory_xact_lock($1)", modelglob.AdvisoryKey(in.ModelID)); e != nil {
		return out, normalize(e)
	}

	var catalogExists bool
	if e = tx.QueryRow(ctx, `
select exists(select 1 from upstream_model_catalog where upstream_id=$1 and upstream_model_id=$2)`,
		upstreamID, in.UpstreamModelID).Scan(&catalogExists); e != nil {
		return out, normalize(e)
	}
	if !catalogExists {
		return out, ErrNotFound
	}

	var boundTo *string
	e = tx.QueryRow(ctx, `
select model_id from upstream_models where upstream_id=$1 and catalog_model_id=$2`,
		upstreamID, in.UpstreamModelID).Scan(&boundTo)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return out, normalize(e)
	}
	if boundTo != nil && *boundTo != in.ModelID {
		return out, ErrCatalogBindingConflict
	}

	out.ModelID = in.ModelID
	switch in.Mode {
	case CatalogImportCreateModel:
		var modelExists bool
		if e = tx.QueryRow(ctx, "select exists(select 1 from models where model_id=$1)", in.ModelID).Scan(&modelExists); e != nil {
			return out, normalize(e)
		}
		if modelExists {
			return out, ErrTargetModelExists
		}
		var nameTaken bool
		if e = tx.QueryRow(ctx, "select exists(select 1 from model_route_rules where pattern=$1)", in.ModelID).Scan(&nameTaken); e != nil {
			return out, normalize(e)
		}
		if nameTaken {
			return out, ErrModelNameTaken
		}
		priceRevision, err := AppendGlobalRevision(ctx, tx, in.ModelID, Billing{}, "manual", nil, false)
		if err != nil {
			return out, err
		}
		if _, err = tx.Exec(ctx, `
insert into models(model_id,enabled,default_upstream_model,shutdown_date,input_usd_per_mtok,cached_input_usd_per_mtok,output_usd_per_mtok,billing_revision_id,billing_revision_scope)
values($1,false,null,null,null,null,null,$2,'global')`, in.ModelID, priceRevision); err != nil {
			return out, normalize(err)
		}
		if err := insertCatalogCapability(ctx, tx, upstreamID, in.ModelID, in.UpstreamModelID); err != nil {
			return out, err
		}
		out.CapabilityCreated = true

	case CatalogImportBindExisting:
		var modelExists bool
		if e = tx.QueryRow(ctx, "select exists(select 1 from models where model_id=$1)", in.ModelID).Scan(&modelExists); e != nil {
			return out, normalize(e)
		}
		if !modelExists {
			return out, ErrNotFound
		}
		var capabilityCatalog *string
		e = tx.QueryRow(ctx, `
select catalog_model_id from upstream_models where upstream_id=$1 and model_id=$2 for update`,
			upstreamID, in.ModelID).Scan(&capabilityCatalog)
		switch {
		case e == nil:
			if capabilityCatalog != nil && *capabilityCatalog != in.UpstreamModelID {
				return out, ErrCatalogBindingConflict
			}
			if capabilityCatalog != nil {
				out.CapabilitiesRevision = revision
				return s.finishCatalogImport(ctx, tx, upstreamID, in, a, out)
			}
			if _, e = tx.Exec(ctx, `
update upstream_models set catalog_model_id=$3, updated_at=now()
where upstream_id=$1 and model_id=$2`, upstreamID, in.ModelID, in.UpstreamModelID); e != nil {
				return out, normalize(e)
			}
		case errors.Is(e, pgx.ErrNoRows):
			if e = insertCatalogCapability(ctx, tx, upstreamID, in.ModelID, in.UpstreamModelID); e != nil {
				return out, e
			}
			out.CapabilityCreated = true
		default:
			return out, normalize(e)
		}
	default:
		return out, ErrReference
	}

	if e = tx.QueryRow(ctx, `
update upstreams set capabilities_revision=capabilities_revision+1, updated_at=now()
where id=$1 returning capabilities_revision`, upstreamID).Scan(&out.CapabilitiesRevision); e != nil {
		return out, normalize(e)
	}
	return s.finishCatalogImport(ctx, tx, upstreamID, in, a, out)
}

// insertCatalogCapability creates the phase-one default for a new binding:
// disabled, and carrying the raw catalog ID as the upstream model ID.
func insertCatalogCapability(ctx context.Context, tx pgx.Tx, upstreamID, modelID, upstreamModelID string) error {
	_, e := tx.Exec(ctx, `
insert into upstream_models(upstream_id,model_id,upstream_model_id,enabled,catalog_model_id)
values($1,$2,$3,false,$3)`, upstreamID, modelID, upstreamModelID)
	return normalize(e)
}

func (s *Store) finishCatalogImport(ctx context.Context, tx pgx.Tx, upstreamID string, in CatalogImport, a Audit, out CatalogImportResult) (CatalogImportResult, error) {
	if a.Action == "" {
		a.Action = "catalog.import"
	}
	if len(a.Payload) == 0 {
		a.Payload, _ = json.Marshal(map[string]string{
			"upstream_id": upstreamID, "upstream_model_id": in.UpstreamModelID, "mode": in.Mode,
		})
	}
	if e := writeAudit(ctx, tx, a); e != nil {
		return out, e
	}
	return out, normalize(tx.Commit(ctx))
}

// SaveUpstreamPhaseTwo replaces the capability set under the upstream row lock.
// A stale expected revision is rejected before any change, retained
// capabilities keep their catalog binding, removed capabilities lose it, and
// the revision advances once in the same transaction as the full replacement.
func (s *Store) SaveUpstreamPhaseTwo(ctx context.Context, u Upstream, expected int64, a Audit) (Upstream, int64, error) {
	models := u.Models
	var revision int64
	e := s.mutate(ctx, a, func(tx pgx.Tx) error {
		var current int64
		if err := tx.QueryRow(ctx, "select capabilities_revision from upstreams where id=$1 for update", u.ID).Scan(&current); err != nil {
			return normalize(err)
		}
		if current != expected {
			return ErrCapabilitiesRevisionStale
		}
		var nameTaken bool
		if err := tx.QueryRow(ctx, "select exists(select 1 from upstreams where name=$2 and id<>$1)", u.ID, u.Name).Scan(&nameTaken); err != nil {
			return normalize(err)
		}
		if nameTaken {
			// Keep the name conflict distinct from unrelated unique/FK violations
			// so the API can report the field instead of a bare 409.
			return ErrUpstreamNameTaken
		}
		q := "update upstreams set name=$2,kind=$3,base_url=$4,auth_style=$5,api_key_hint=$6,enabled=$7,connect_timeout_ms=$8,first_byte_timeout_ms=$9,idle_timeout_ms=$10,api_key_ciphertext=$11,api_key_nonce=$12,capabilities_revision=capabilities_revision+1,updated_at=now() where id=$1"
		if _, err := tx.Exec(ctx, q, u.ID, u.Name, u.Kind, u.BaseURL, u.AuthStyle, u.Hint, u.Enabled, u.Connect, u.FirstByte, u.Idle, u.Ciphertext, u.Nonce); err != nil {
			return normalize(err)
		}
		var err error
		if u, err = scanUpstream(tx.QueryRow(ctx, "select "+upstreamColumns+" from upstreams where id=$1", u.ID)); err != nil {
			return err
		}
		if err = tx.QueryRow(ctx, "select capabilities_revision from upstreams where id=$1", u.ID).Scan(&revision); err != nil {
			return normalize(err)
		}
		ids := make([]string, 0, len(models))
		for _, c := range models {
			ids = append(ids, c.ModelID)
		}
		if _, err = tx.Exec(ctx, "delete from upstream_models where upstream_id=$1 and not (model_id = any($2::text[]))", u.ID, ids); err != nil {
			return normalize(err)
		}
		for _, c := range models {
			if _, err = tx.Exec(ctx, `
insert into upstream_models(upstream_id,upstream_model_id,model_id,enabled)
values($1,$2,$3,$4)
on conflict (upstream_id,upstream_model_id) do update
set model_id=excluded.model_id,enabled=excluded.enabled,updated_at=now()`,
				u.ID, c.UpstreamModelID, c.ModelID, c.Enabled); err != nil {
				return normalize(err)
			}
		}
		return nil
	})
	u.Models = models
	return u, revision, e
}

// CapabilitiesRevisions reads the current capability collection version for
// the requested upstreams. Missing IDs are simply absent from the result.
func (s *Store) CapabilitiesRevisions(ctx context.Context, ids []string) (map[string]int64, error) {
	out := make(map[string]int64, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, e := s.Pool.Query(ctx, "select id::text,capabilities_revision from upstreams where id=any($1::uuid[])", ids)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var revision int64
		if e = rows.Scan(&id, &revision); e != nil {
			return nil, e
		}
		out[id] = revision
	}
	return out, rows.Err()
}
