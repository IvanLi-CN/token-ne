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
	Cost            *string `json:"cost_usd"`
	PriceRevisionID *string `json:"price_revision_id"`
}

// priceProfile mirrors the ingestion price query. Overrides is keyed by
// upstream UUID; the global row carries no upstream override.
type priceProfile struct {
	Input, Cached, CacheWrite, AudioIn, AudioOut, Reasoning, Output *string
	RevisionID                                                      *string
	Tiers                                                           []cost.Tier
	Overrides                                                       map[string]priceProfile
}

func (p priceProfile) Prices() cost.Prices {
	return cost.Prices{
		Input: p.Input, Cached: p.Cached, CacheWrite: p.CacheWrite,
		AudioIn: p.AudioIn, AudioOut: p.AudioOut, Reasoning: p.Reasoning, Output: p.Output,
	}
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
 endpoint, requested_model, resolved_model, upstream_model, stream, http_status, status_rewritten_from, outcome,
 error_class, error_code, error_message, attempt_count, input_tokens, cached_input_tokens,
 cache_write_tokens, audio_input_tokens, audio_output_tokens, output_tokens,
 reasoning_tokens, total_tokens, cost_usd, price_revision_id, ttft_ms, upstream_ttfb_ms, duration_ms, upstream_duration_ms,
 request_bytes, response_bytes
)
select e.* from jsonb_to_recordset($1::jsonb) as e (
 event_id uuid, request_id text, occurred_at timestamptz, core_version text, core_instance text,
 downstream_key_id uuid, upstream_id uuid, endpoint text, requested_model text, resolved_model text, upstream_model text,
 stream boolean, http_status int, status_rewritten_from int, outcome text, error_class text,
 error_code text, error_message text, attempt_count int, input_tokens bigint, cached_input_tokens bigint,
 cache_write_tokens bigint, audio_input_tokens bigint, audio_output_tokens bigint,
 output_tokens bigint, reasoning_tokens bigint, total_tokens bigint, cost_usd numeric(14,6), price_revision_id uuid,
 ttft_ms int, upstream_ttfb_ms int, duration_ms int, upstream_duration_ms int, request_bytes bigint,
 response_bytes bigint
)
order by e.event_id
on conflict (event_id) do nothing
returning event_id::text`

const usagePriceQuery = `select coalesce((select jsonb_object_agg(model_id,jsonb_build_object(
 'Input',input_usd_per_mtok::text,'Cached',cached_input_usd_per_mtok::text,
 'CacheWrite',cache_write_usd_per_mtok::text,'AudioIn',audio_input_usd_per_mtok::text,
 'AudioOut',audio_output_usd_per_mtok::text,'Reasoning',reasoning_usd_per_mtok::text,'Output',output_usd_per_mtok::text,
 'Tiers',coalesce((select jsonb_agg(jsonb_build_object(
   'threshold_tokens',(t->>'threshold_tokens')::bigint,
   'input_usd_per_mtok',t->>'input_usd_per_mtok',
   'cached_input_usd_per_mtok',t->>'cached_input_usd_per_mtok',
   'cache_write_usd_per_mtok',t->>'cache_write_usd_per_mtok',
   'audio_input_usd_per_mtok',t->>'audio_input_usd_per_mtok',
   'audio_output_usd_per_mtok',t->>'audio_output_usd_per_mtok',
   'reasoning_usd_per_mtok',t->>'reasoning_usd_per_mtok',
   'output_usd_per_mtok',t->>'output_usd_per_mtok'))
  from jsonb_array_elements(tiers) t), '[]'::jsonb),
 'RevisionID',billing_revision_id::text,'Overrides',coalesce(overrides,'{}'::jsonb)))
 from (select m.model_id,r.input_usd_per_mtok,r.cached_input_usd_per_mtok,r.cache_write_usd_per_mtok,
   r.audio_input_usd_per_mtok,r.audio_output_usd_per_mtok,r.reasoning_usd_per_mtok,r.output_usd_per_mtok,r.tiers,m.billing_revision_id,
  (select jsonb_object_agg(mp.upstream_id::text,jsonb_build_object(
    'Input',mp.input_usd_per_mtok::text,'Cached',mp.cached_input_usd_per_mtok::text,
    'CacheWrite',mp.cache_write_usd_per_mtok::text,'AudioIn',mp.audio_input_usd_per_mtok::text,
    'AudioOut',mp.audio_output_usd_per_mtok::text,'Reasoning',mp.reasoning_usd_per_mtok::text,'Output',mp.output_usd_per_mtok::text,
    'Tiers',coalesce((select jsonb_agg(jsonb_build_object(
      'threshold_tokens',(t->>'threshold_tokens')::bigint,
      'input_usd_per_mtok',t->>'input_usd_per_mtok',
      'cached_input_usd_per_mtok',t->>'cached_input_usd_per_mtok',
      'cache_write_usd_per_mtok',t->>'cache_write_usd_per_mtok',
      'audio_input_usd_per_mtok',t->>'audio_input_usd_per_mtok',
      'audio_output_usd_per_mtok',t->>'audio_output_usd_per_mtok',
      'reasoning_usd_per_mtok',t->>'reasoning_usd_per_mtok',
      'output_usd_per_mtok',t->>'output_usd_per_mtok'))
     from jsonb_array_elements(mp.tiers) t), '[]'::jsonb),
    'RevisionID',mp.price_revision_id::text))
   from model_prices mp where mp.model_id=m.model_id) as overrides
  from models m join price_revisions r on r.id=m.billing_revision_id where m.model_id=any($1::text[])) s), '{}')`

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
	seenModels := map[string]bool{}
	addModel := func(v *string) {
		if v != nil && !seenModels[*v] {
			seenModels[*v] = true
			models = append(models, *v)
		}
	}
	for _, e := range batch.Events {
		eventIDs = append(eventIDs, e.EventID)
		versions = append(versions, e.CoreVersion)
		if e.DownstreamKeyID != nil {
			keyIDs = append(keyIDs, *e.DownstreamKeyID)
		}
		if e.UpstreamID != nil {
			upstreamIDs = append(upstreamIDs, *e.UpstreamID)
		}
		addModel(e.RequestedModel)
		addModel(e.ResolvedModel)
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
	var prices map[string]priceProfile
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
		modelID := e.RequestedModel
		if e.ResolvedModel != nil {
			if _, ok := prices[*e.ResolvedModel]; ok {
				modelID = e.ResolvedModel
			} else {
				slog.WarnContext(ctx, "resolved model is not in current configuration; attributing cost to the requested model", "event_id", e.EventID, "resolved_model", *e.ResolvedModel)
			}
		}
		var revisionID *string
		if modelID != nil {
			if price, ok := prices[*modelID]; ok {
				effective := price
				if e.UpstreamID != nil {
					if override, ok := price.Overrides[*e.UpstreamID]; ok {
						effective = override
					}
				}
				obs.ModelLabel = *modelID
				var fits bool
				obs.Cost, fits = cost.Calculate(effective.Prices(), effective.Tiers, cost.Usage{
					Input: e.InputTokens, Cached: e.CachedInputTokens, CacheWrite: e.CacheWriteTokens,
					AudioIn: e.AudioInputTokens, AudioOut: e.AudioOutputTokens,
					Reasoning: e.ReasoningTokens, Output: e.OutputTokens,
				})
				if !fits {
					field := "input_tokens"
					if e.CachedInputTokens != nil && e.InputTokens != nil && *e.CachedInputTokens > *e.InputTokens {
						field = "cached_input_tokens"
					}
					return usage.Result{}, usage.EventProblem(i, field, "calculated cost exceeds storage range or token detail is inconsistent")
				}
				revisionID = effective.RevisionID
			} else {
				slog.WarnContext(ctx, "usage event has no matching price profile", "event_id", e.EventID, "model", *modelID)
			}
		}
		inconsistent[e.EventID] = e.NormalizeTotal()
		obs.Event = e
		observations[e.EventID] = obs
		rows = append(rows, pricedUsage{Event: e, Cost: obs.Cost, PriceRevisionID: revisionID})
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
