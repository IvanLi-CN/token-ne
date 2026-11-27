package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type UsageRange struct{ From, To time.Time }

type UsageTotals struct {
	Requests    int64       `json:"requests"`
	TotalTokens json.Number `json:"total_tokens"`
	CostUSD     *float64    `json:"cost_usd"`
}

type UsageOverview struct {
	UsageTotals
	SuccessRate float64 `json:"success_rate"`
	P95Latency  int     `json:"p95_latency_ms"`
	P95TTFT     *int    `json:"p95_ttft_ms"`
}

type UsagePoint struct {
	Bucket time.Time `json:"bucket"`
	UsageTotals
}

type UsageGroup struct {
	Key string `json:"key"`
	UsageTotals
}

type RequestFilter struct {
	UsageRange
	Model, ResolvedModel, Upstream *string
	Status                         *int
	Cursor                         *Cursor
	Limit                          int
}

type RequestSummary struct {
	EventID         string    `json:"event_id"`
	RequestID       string    `json:"request_id"`
	OccurredAt      time.Time `json:"occurred_at"`
	Endpoint        string    `json:"endpoint"`
	RequestedModel  *string   `json:"requested_model"`
	ResolvedModel   *string   `json:"resolved_model"`
	PriceRevisionID *string   `json:"price_revision_id"`
	UpstreamModel   *string   `json:"upstream_model"`
	UpstreamID      *string   `json:"upstream_id"`
	// EgressMode and EgressProxyID are the terminal attempt's egress; both are
	// absent for a phase-one event or an event with no upstream attempt.
	EgressMode    *string `json:"egress_mode"`
	EgressProxyID *string `json:"egress_proxy_id"`
	Stream        bool    `json:"stream"`
	HTTPStatus    *int    `json:"http_status"`
	Outcome       string  `json:"outcome"`
	AttemptCount  int     `json:"attempt_count"`
	TTFT          *int    `json:"ttft_ms"`
	Duration      *int    `json:"duration_ms,omitempty"`
}

type RequestDetail struct {
	RequestSummary
	ErrorClass          *string  `json:"error_class"`
	ErrorCode           *string  `json:"error_code"`
	ErrorMessage        *string  `json:"error_message"`
	InputTokens         *int64   `json:"input_tokens"`
	CachedInputTokens   *int64   `json:"cached_input_tokens"`
	CacheWriteTokens    *int64   `json:"cache_write_tokens"`
	AudioInputTokens    *int64   `json:"audio_input_tokens"`
	AudioOutputTokens   *int64   `json:"audio_output_tokens"`
	OutputTokens        *int64   `json:"output_tokens"`
	ReasoningTokens     *int64   `json:"reasoning_tokens"`
	TotalTokens         *int64   `json:"total_tokens"`
	CostUSD             *float64 `json:"cost_usd"`
	UpstreamTTFB        *int     `json:"upstream_ttfb_ms"`
	UpstreamDuration    *int     `json:"upstream_duration_ms"`
	RequestBytes        *int64   `json:"request_bytes"`
	ResponseBytes       *int64   `json:"response_bytes"`
	StatusRewrittenFrom *int     `json:"status_rewritten_from"`
	PriceSource         *string  `json:"price_source"`
	PriceSourceID       *string  `json:"price_source_id"`
}

const usageTotals = `count(*), coalesce(sum(total_tokens),0)::text, sum(cost_usd)::float8`
const usageWindow = `occurred_at >= $1 and occurred_at < $2`

func (s *Store) UsageOverview(ctx context.Context, r UsageRange) (UsageOverview, error) {
	var v UsageOverview
	err := s.Pool.QueryRow(ctx, `select `+usageTotals+`,
 coalesce(count(*) filter (where outcome='success')::float8/nullif(count(*),0),0),
 coalesce(percentile_disc(0.95) within group (order by duration_ms),0),
 percentile_disc(0.95) within group (order by ttft_ms)
 from usage_events where `+usageWindow, r.From, r.To).Scan(
		&v.Requests, &v.TotalTokens, &v.CostUSD, &v.SuccessRate, &v.P95Latency, &v.P95TTFT)
	return v, err
}

func (s *Store) UsageTimeseries(ctx context.Context, r UsageRange, interval string) ([]UsagePoint, error) {
	var stride string
	switch interval {
	case "5m":
		stride = "5 minutes"
	case "1h":
		stride = "1 hour"
	case "1d":
		stride = "1 day"
	default:
		return nil, errors.New("unsupported interval")
	}
	rows, err := s.Pool.Query(ctx, `select date_bin($3::text::interval,occurred_at,'1970-01-01 00:00:00+00'::timestamptz) as bucket,
 `+usageTotals+` from usage_events where `+usageWindow+` group by bucket order by bucket`, r.From, r.To, stride)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (UsagePoint, error) {
		var p UsagePoint
		err := row.Scan(&p.Bucket, &p.Requests, &p.TotalTokens, &p.CostUSD)
		p.Bucket = p.Bucket.UTC()
		return p, err
	})
}

func (s *Store) UsageBreakdown(ctx context.Context, r UsageRange, group string) ([]UsageGroup, error) {
	var column string
	switch group {
	case "model":
		column = "requested_model"
	case "upstream":
		column = "upstream_id::text"
	case "downstream_key":
		column = "downstream_key_id::text"
	default:
		return nil, errors.New("unsupported group")
	}
	rows, err := s.Pool.Query(ctx, `select coalesce(`+column+`,'unknown') as key,`+usageTotals+
		` from usage_events where `+usageWindow+` group by 1 order by count(*) desc,key`, r.From, r.To)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (UsageGroup, error) {
		var g UsageGroup
		err := row.Scan(&g.Key, &g.Requests, &g.TotalTokens, &g.CostUSD)
		return g, err
	})
}

const requestColumns = `u.event_id::text,u.request_id,u.occurred_at,u.endpoint,u.requested_model,u.resolved_model,
u.price_revision_id,u.upstream_model,u.upstream_id::text,u.egress_mode,u.egress_proxy_id::text,u.stream,u.http_status,u.outcome,u.attempt_count,u.ttft_ms,u.duration_ms`

func (v *RequestSummary) fields() []any {
	return []any{&v.EventID, &v.RequestID, &v.OccurredAt, &v.Endpoint, &v.RequestedModel, &v.ResolvedModel,
		&v.PriceRevisionID, &v.UpstreamModel, &v.UpstreamID, &v.EgressMode, &v.EgressProxyID, &v.Stream, &v.HTTPStatus, &v.Outcome, &v.AttemptCount, &v.TTFT, &v.Duration}
}

func (s *Store) UsageRequests(ctx context.Context, f RequestFilter) ([]RequestSummary, error) {
	var before *time.Time
	var id *string
	if f.Cursor != nil {
		before, id = &f.Cursor.Time, &f.Cursor.ID
	}
	rows, err := s.Pool.Query(ctx, `select `+requestColumns+` from usage_events u where `+usageWindow+`
 and ($3::text is null or requested_model=$3)
	 and ($9::text is null or resolved_model=$9)
 and ($4::uuid is null or upstream_id=$4)
 and ($5::int is null or http_status=$5)
 and ($6::timestamptz is null or (occurred_at,event_id)<($6,$7::uuid))
	 order by occurred_at desc,event_id desc limit $8`, f.From, f.To, f.Model, f.Upstream, f.Status, before, id, f.Limit, f.ResolvedModel)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (RequestSummary, error) {
		var v RequestSummary
		err := row.Scan(v.fields()...)
		v.OccurredAt = v.OccurredAt.UTC()
		return v, err
	})
}

func (s *Store) UsageRequest(ctx context.Context, id string) (RequestDetail, error) {
	var v RequestDetail
	err := s.Pool.QueryRow(ctx, `select `+requestColumns+`,pr.source,pr.source_id,error_class,error_code,error_message,input_tokens,cached_input_tokens,
 cache_write_tokens,audio_input_tokens,audio_output_tokens,
 output_tokens,reasoning_tokens,total_tokens,cost_usd::float8,upstream_ttfb_ms,upstream_duration_ms,
 request_bytes,response_bytes,status_rewritten_from from usage_events u left join price_revisions pr on pr.id=u.price_revision_id where request_id=$1
 order by occurred_at desc,event_id desc limit 1`, id).Scan(append(v.fields(), &v.PriceSource, &v.PriceSourceID, &v.ErrorClass, &v.ErrorCode, &v.ErrorMessage, &v.InputTokens, &v.CachedInputTokens,
		&v.CacheWriteTokens, &v.AudioInputTokens, &v.AudioOutputTokens, &v.OutputTokens, &v.ReasoningTokens, &v.TotalTokens, &v.CostUSD, &v.UpstreamTTFB, &v.UpstreamDuration,
		&v.RequestBytes, &v.ResponseBytes, &v.StatusRewrittenFrom)...)
	v.OccurredAt = v.OccurredAt.UTC()
	return v, normalize(err)
}

// PruneUsage and PruneAudit delete one bounded batch. Scheduling belongs to
// callers; the API does not execute either hook on startup or in the background.
func (s *Store) PruneUsage(ctx context.Context, now time.Time, limit int) (int64, error) {
	return s.prune(ctx, now.Add(-30*24*time.Hour), limit, `delete from usage_events where event_id in (
 select event_id from usage_events where occurred_at < $1 order by occurred_at,event_id limit $2 for update skip locked)`)
}

func (s *Store) PruneAudit(ctx context.Context, now time.Time, limit int) (int64, error) {
	return s.prune(ctx, now.Add(-90*24*time.Hour), limit, `delete from audit_events where id in (
 select id from audit_events where created_at < $1 order by created_at,id limit $2 for update skip locked)`)
}

func (s *Store) prune(ctx context.Context, cutoff time.Time, limit int, sql string) (int64, error) {
	if limit < 1 || limit > 10000 {
		return 0, errors.New("prune limit must be between 1 and 10000")
	}
	tag, err := s.Pool.Exec(ctx, sql, cutoff, limit)
	return tag.RowsAffected(), err
}
