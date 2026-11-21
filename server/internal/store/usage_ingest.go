package store

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"

	"github.com/IvanLi-CN/token-ne/server/internal/cost"
	"github.com/IvanLi-CN/token-ne/server/internal/usage"
	"github.com/jackc/pgx/v5"
)

type pricedUsage struct {
	usage.Event
	Cost *string `json:"cost_usd"`
}

type UsageTx interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
	Commit(context.Context) error
	Rollback(context.Context) error
}

type UsagePool interface {
	Begin(context.Context) (pgx.Tx, error)
}

type UsageInserter struct {
	Pool           UsagePool
	PriceQuery     string
	ReferenceQuery string
	InsertQuery    string
}

func NewUsageInserter(pool UsagePool) *UsageInserter {
	return &UsageInserter{Pool: pool, PriceQuery: usagePriceQuery, ReferenceQuery: usageReferenceQuery, InsertQuery: usageInsert}
}

func (u *UsageInserter) IngestUsage(ctx context.Context, batch usage.Batch) (usage.Result, error) {
	return ingestUsage(ctx, u.Pool, batch, u.PriceQuery, u.ReferenceQuery, u.InsertQuery)
}

const usageInsert = `
insert into usage_events (
 event_id, request_id, occurred_at, core_version, core_instance, downstream_key_id, upstream_id,
 endpoint, requested_model, upstream_model, stream, http_status, status_rewritten_from, outcome,
 error_class, error_code, error_message, attempt_count, input_tokens, cached_input_tokens, output_tokens,
 reasoning_tokens, total_tokens, cost_usd, ttft_ms, upstream_ttfb_ms, duration_ms, upstream_duration_ms,
 request_bytes, response_bytes
)
select e.* from jsonb_to_recordset($1::jsonb) as e (
 event_id uuid, request_id text, occurred_at timestamptz, core_version text, core_instance text,
 downstream_key_id uuid, upstream_id uuid, endpoint text, requested_model text, upstream_model text,
 stream boolean, http_status int, status_rewritten_from int, outcome text, error_class text,
 error_code text, error_message text, attempt_count int, input_tokens bigint, cached_input_tokens bigint,
 output_tokens bigint, reasoning_tokens bigint, total_tokens bigint, cost_usd numeric(14,6),
 ttft_ms int, upstream_ttfb_ms int, duration_ms int, upstream_duration_ms int, request_bytes bigint,
 response_bytes bigint
)
order by e.event_id
on conflict (event_id) do nothing
returning event_id::text`

const usagePriceQuery = `select coalesce((select jsonb_object_agg(model_id,jsonb_build_object(
 'Input',input_usd_per_mtok::text,'Cached',cached_input_usd_per_mtok::text,'Output',output_usd_per_mtok::text))
 from models where model_id=any($1::text[])), '{}')`

const usageReferenceQuery = `select
 array(select event_id::text from usage_events where event_id=any($1::text[]::uuid[])),
 array(select id::text from downstream_api_keys where id=any($2::text[]::uuid[])),
 coalesce((select jsonb_object_agg(id::text,name) from upstreams where id=any($3::text[]::uuid[])), '{}'),
 array(select version from core_versions where version=any($4::text[]))`

// IngestUsage returns observations only for rows inserted by a committed transaction.
func (s *Store) IngestUsage(ctx context.Context, batch usage.Batch) (usage.Result, error) {
	return ingestUsage(ctx, s.Pool, batch, usagePriceQuery, usageReferenceQuery, usageInsert)
}

func ingestUsage(ctx context.Context, pool UsagePool, batch usage.Batch, priceQuery, referenceQuery, insertQuery string) (usage.Result, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return usage.Result{}, err
	}
	defer tx.Rollback(ctx)

	var registered bool
	if err := tx.QueryRow(ctx, "select exists(select 1 from core_instances where id=$1)", batch.Instance).Scan(&registered); err != nil {
		return usage.Result{}, err
	}
	if !registered {
		return usage.Result{}, usage.ErrUnknownInstance
	}

	eventIDs, keyIDs, upstreamIDs, models, versions := []string{}, []string{}, []string{}, []string{}, []string{}
	for _, e := range batch.Events {
		eventIDs = append(eventIDs, e.EventID)
		versions = append(versions, e.CoreVersion)
		if e.DownstreamKeyID != nil {
			keyIDs = append(keyIDs, *e.DownstreamKeyID)
		}
		if e.UpstreamID != nil {
			upstreamIDs = append(upstreamIDs, *e.UpstreamID)
		}
		if e.RequestedModel != nil {
			models = append(models, *e.RequestedModel)
		}
	}
	// All lookups share a statement snapshot. Previously accepted events skip
	// reference validation so retries survive removal of old configurations.
	var existing, keys, knownVersions []string
	var upstreamJSON, pricingJSON []byte
	err = tx.QueryRow(ctx, referenceQuery, eventIDs, keyIDs, upstreamIDs, versions).Scan(&existing, &keys, &upstreamJSON, &knownVersions)
	if err != nil {
		return usage.Result{}, err
	}
	if err = tx.QueryRow(ctx, priceQuery, models).Scan(&pricingJSON); err != nil {
		return usage.Result{}, err
	}
	var upstreams map[string]string
	var prices map[string]cost.Prices
	if err := json.Unmarshal(upstreamJSON, &upstreams); err != nil {
		return usage.Result{}, err
	}
	if err := json.Unmarshal(pricingJSON, &prices); err != nil {
		return usage.Result{}, err
	}

	rows := make([]pricedUsage, 0, len(batch.Events))
	observations := make(map[string]usage.AcceptedEvent, len(batch.Events))
	inconsistent := make(map[string]bool)
	for i, e := range batch.Events {
		if slices.Contains(existing, e.EventID) {
			continue
		}
		if e.DownstreamKeyID != nil && !slices.Contains(keys, *e.DownstreamKeyID) {
			return usage.Result{}, usage.EventProblem(i, "downstream_key_id", "downstream key is not registered")
		}
		obs := usage.AcceptedEvent{ModelLabel: "unknown", UpstreamLabel: "unknown", VersionLabel: "unknown"}
		if e.UpstreamID != nil {
			name, ok := upstreams[*e.UpstreamID]
			if !ok {
				return usage.Result{}, usage.EventProblem(i, "upstream_id", "upstream is not registered")
			}
			obs.UpstreamLabel = name
		}
		if slices.Contains(knownVersions, e.CoreVersion) {
			obs.VersionLabel = e.CoreVersion
		}
		if e.RequestedModel != nil {
			if price, ok := prices[*e.RequestedModel]; ok {
				obs.ModelLabel = *e.RequestedModel
				var fits bool
				obs.Cost, fits = cost.Calculate(price, e.InputTokens, e.CachedInputTokens, e.OutputTokens)
				if !fits {
					field := "input_tokens"
					if e.CachedInputTokens != nil && e.InputTokens != nil && *e.CachedInputTokens > *e.InputTokens {
						field = "cached_input_tokens"
					}
					return usage.Result{}, usage.EventProblem(i, field, "calculated cost exceeds storage range or token detail is inconsistent")
				}
			}
		}
		inconsistent[e.EventID] = e.NormalizeTotal()
		obs.Event = e
		observations[e.EventID] = obs
		rows = append(rows, pricedUsage{Event: e, Cost: obs.Cost})
	}

	result := usage.Result{Duplicates: len(batch.Events)}
	if len(rows) != 0 {
		data, err := json.Marshal(rows)
		if err != nil {
			return usage.Result{}, err
		}
		returned, err := tx.Query(ctx, insertQuery, data)
		if err != nil {
			return usage.Result{}, err
		}
		ids, err := pgx.CollectRows(returned, pgx.RowTo[string])
		if err != nil {
			return usage.Result{}, err
		}
		for _, id := range ids {
			result.Inserted = append(result.Inserted, observations[id])
		}
		result.Duplicates -= len(ids)
	}
	if err := tx.Commit(ctx); err != nil {
		return usage.Result{}, err
	}
	for _, obs := range result.Inserted {
		if inconsistent[obs.EventID] {
			slog.WarnContext(ctx, "usage total differs from input plus output", "event_id", obs.EventID)
		}
	}
	return result, nil
}
