package store

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"time"

	"github.com/IvanLi-CN/token-ne/server/internal/pricesource"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var (
	ErrStalePriceQuote     = errors.New("stale price quote")
	ErrQuoteReviewRequired = errors.New("quote review required")
	ErrPriceRunNotReady    = errors.New("price run not ready")
	ErrQuoteNotApplicable  = errors.New("quote not applicable")
)

// PriceSource is one controlled source exposed to the management API.
type PriceSource struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Enabled      bool       `json:"enabled"`
	LastSyncedAt *time.Time `json:"last_synced_at"`
}

// PriceQuote is one generated price suggestion. A nil UpstreamID targets the
// model's global default; otherwise it targets one upstream override.
type PriceQuote struct {
	ID                  string     `json:"id"`
	ModelID             string     `json:"model_id"`
	UpstreamID          *string    `json:"upstream_id"`
	SourceModelID       *string    `json:"source_model_id"`
	MatchMethod         string     `json:"match_method"`
	Status              string     `json:"status"`
	Locked              bool       `json:"locked"`
	BasePriceRevisionID *string    `json:"base_price_revision_id"`
	BaseUpdatedAt       *time.Time `json:"base_updated_at"`
	Current             *Billing   `json:"current"`
	Proposed            *Billing   `json:"proposed"`
}

// PriceSyncRun is the reviewable output of one source fetch.
type PriceSyncRun struct {
	ID               string       `json:"id"`
	SourceID         string       `json:"source_id"`
	State            string       `json:"state"`
	FetchedAt        *time.Time   `json:"fetched_at"`
	AppliedAt        *time.Time   `json:"applied_at"`
	PriceRevisionIDs []string     `json:"price_revision_ids"`
	Error            *string      `json:"error"`
	Items            []PriceQuote `json:"items"`
}

// PriceSyncApplyInput is the selected and explicitly reviewed subset of one
// run. ForceLocked confirms locked profiles only; it never substitutes for
// per-quote review.
type PriceSyncApplyInput struct {
	QuoteIDs         []string
	ReviewedQuoteIDs []string
	ForceLocked      bool
}

const priceRunColumns = "id::text,source_id,state,fetched_at,applied_at,quotes,price_revision_ids::text[],error"

func scanPriceRun(row pgx.Row) (PriceSyncRun, error) {
	var run PriceSyncRun
	var raw []byte
	err := row.Scan(&run.ID, &run.SourceID, &run.State, &run.FetchedAt, &run.AppliedAt,
		&raw, &run.PriceRevisionIDs, &run.Error)
	if err != nil {
		return PriceSyncRun{}, normalize(err)
	}
	if len(raw) == 0 {
		raw = []byte("[]")
	}
	if err = json.Unmarshal(raw, &run.Items); err != nil {
		return PriceSyncRun{}, err
	}
	if run.Items == nil {
		run.Items = []PriceQuote{}
	}
	if run.PriceRevisionIDs == nil {
		run.PriceRevisionIDs = []string{}
	}
	return run, nil
}

// PriceSources lists the controlled sources and the last completed fetch.
func (s *Store) PriceSources(ctx context.Context) ([]PriceSource, error) {
	adapter := pricesource.Builtin{}
	infos := adapter.Sources()
	out := make([]PriceSource, 0, len(infos))
	byID := map[string]int{}
	for _, info := range infos {
		byID[info.ID] = len(out)
		out = append(out, PriceSource{ID: info.ID, Name: info.Name, Enabled: info.Enabled})
	}
	rows, err := s.Pool.Query(ctx, `
select source_id,max(fetched_at) from price_sync_runs
where fetched_at is not null group by source_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var fetched *time.Time
		if err = rows.Scan(&id, &fetched); err != nil {
			return nil, err
		}
		if i, ok := byID[id]; ok {
			out[i].LastSyncedAt = fetched
		}
	}
	return out, rows.Err()
}

// SyncPriceSource creates a running run, generates quotes from the controlled
// dataset, and commits it as ready or failed. It never changes current prices.
func (s *Store) SyncPriceSource(ctx context.Context, sourceID string, a Audit) (PriceSyncRun, error) {
	adapter := pricesource.Builtin{}
	dataset, err := adapter.Fetch(sourceID)
	if errors.Is(err, pricesource.ErrSourceNotFound) {
		return PriceSyncRun{}, ErrNotFound
	}
	if err != nil {
		return PriceSyncRun{}, err
	}
	if a.Action == "" {
		a.Action = "price.quote_fetch"
	}
	if a.TargetType == "" {
		a.TargetType = "price_sync_run"
	}
	runID := uuid.NewString()
	a.TargetID = &runID
	now := time.Now().UTC()
	run := PriceSyncRun{
		ID: runID, SourceID: sourceID, State: "running",
		PriceRevisionIDs: []string{}, Items: []PriceQuote{},
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return run, err
	}
	if _, err = tx.Exec(ctx, "insert into price_sync_runs(id,source_id,state) values($1,$2,'running')", runID, sourceID); err != nil {
		_ = tx.Rollback(ctx)
		return run, normalize(err)
	}
	fail := func(reason error) (PriceSyncRun, error) {
		message := reason.Error()
		run.State, run.Error, run.FetchedAt = "failed", &message, &now
		if _, updateErr := tx.Exec(ctx, "update price_sync_runs set state='failed',fetched_at=$2,error=$3 where id=$1", runID, now, message); updateErr != nil {
			_ = tx.Rollback(ctx)
			return run, normalize(updateErr)
		}
		a.Payload = priceAuditPayload(map[string]any{
			"source_id": sourceID, "outcome": "failed", "error": message,
		})
		if auditErr := writeAudit(ctx, tx, a); auditErr != nil {
			_ = tx.Rollback(ctx)
			return run, auditErr
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return run, commitErr
		}
		return run, nil
	}
	if err = adapter.Validate(dataset); err != nil {
		return fail(err)
	}
	targets, err := loadPriceTargets(ctx, tx)
	if err != nil {
		_ = tx.Rollback(ctx)
		return run, err
	}
	quotes, err := adapter.Quotes(dataset, targets)
	if err != nil {
		return fail(err)
	}
	items := make([]PriceQuote, len(quotes))
	for i, q := range quotes {
		items[i] = storeQuote(q)
	}
	raw, err := json.Marshal(items)
	if err != nil {
		return fail(err)
	}
	run.State, run.FetchedAt, run.Items = "ready", &now, items
	if _, err = tx.Exec(ctx, "update price_sync_runs set state='ready',fetched_at=$2,quotes=$3 where id=$1", runID, now, raw); err != nil {
		_ = tx.Rollback(ctx)
		return run, normalize(err)
	}
	a.Payload = priceAuditPayload(map[string]any{
		"source_id": sourceID, "outcome": "ready", "quote_count": len(items),
	})
	if err = writeAudit(ctx, tx, a); err != nil {
		_ = tx.Rollback(ctx)
		return run, err
	}
	if err = tx.Commit(ctx); err != nil {
		return run, err
	}
	return run, nil
}

// PriceSyncRun returns one run with its generated quotes.
func (s *Store) PriceSyncRun(ctx context.Context, runID string) (PriceSyncRun, error) {
	if _, err := uuid.Parse(runID); err != nil {
		return PriceSyncRun{}, ErrNotFound
	}
	return scanPriceRun(s.Pool.QueryRow(ctx, "select "+priceRunColumns+" from price_sync_runs where id=$1", runID))
}

// ApplyPriceSyncRun applies an explicitly selected subset atomically. It locks
// the run row plus every target profile before rechecking every baseline.
func (s *Store) ApplyPriceSyncRun(ctx context.Context, runID string, in PriceSyncApplyInput, a Audit) (PriceSyncRun, error) {
	if a.Action == "" {
		a.Action = "price.quote_apply"
	}
	if a.TargetType == "" {
		a.TargetType = "price_sync_run"
	}
	a.TargetID = &runID
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return PriceSyncRun{}, err
	}
	run, appliedIDs, err := applyPriceRun(ctx, tx, runID, in)
	if err != nil {
		_ = tx.Rollback(ctx)
		a.Payload = priceAuditPayload(map[string]any{
			"quote_ids": in.QuoteIDs, "reviewed_quote_ids": in.ReviewedQuoteIDs,
			"force_locked": in.ForceLocked, "outcome": "rejected", "code": rejectionCode(err),
		})
		_ = s.RecordAudit(ctx, a)
		return PriceSyncRun{}, err
	}
	a.Payload = priceAuditPayload(map[string]any{
		"quote_ids": in.QuoteIDs, "reviewed_quote_ids": in.ReviewedQuoteIDs,
		"force_locked": in.ForceLocked, "outcome": "applied", "price_revision_ids": appliedIDs,
	})
	if err = writeAudit(ctx, tx, a); err != nil {
		_ = tx.Rollback(ctx)
		return PriceSyncRun{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return PriceSyncRun{}, err
	}
	return run, nil
}

func applyPriceRun(ctx context.Context, tx pgx.Tx, runID string, in PriceSyncApplyInput) (PriceSyncRun, []string, error) {
	var empty []string
	run, err := scanPriceRun(tx.QueryRow(ctx, "select "+priceRunColumns+" from price_sync_runs where id=$1 for update", runID))
	if err != nil {
		return PriceSyncRun{}, empty, err
	}
	if run.State != "ready" && run.State != "applied" {
		return PriceSyncRun{}, empty, ErrPriceRunNotReady
	}
	if len(in.QuoteIDs) == 0 {
		return PriceSyncRun{}, empty, ErrQuoteNotApplicable
	}
	items := make(map[string]PriceQuote, len(run.Items))
	for _, q := range run.Items {
		items[q.ID] = q
	}
	reviewed := make(map[string]bool, len(in.ReviewedQuoteIDs))
	for _, id := range in.ReviewedQuoteIDs {
		if !containsString(in.QuoteIDs, id) {
			return PriceSyncRun{}, empty, ErrQuoteReviewRequired
		}
		reviewed[id] = true
	}
	selected := make([]PriceQuote, 0, len(in.QuoteIDs))
	seenQuote := map[string]bool{}
	seenTarget := map[string]bool{}
	for _, id := range in.QuoteIDs {
		if seenQuote[id] {
			return PriceSyncRun{}, empty, ErrQuoteNotApplicable
		}
		seenQuote[id] = true
		q, ok := items[id]
		if !ok || q.Proposed == nil {
			return PriceSyncRun{}, empty, ErrQuoteNotApplicable
		}
		switch q.Status {
		case pricesource.StatusSafe:
		case pricesource.StatusNeedsReview, pricesource.StatusConflict:
			if !reviewed[id] {
				return PriceSyncRun{}, empty, ErrQuoteReviewRequired
			}
		default:
			return PriceSyncRun{}, empty, ErrQuoteNotApplicable
		}
		if d := validateQuoteBilling(*q.Proposed); d != "" {
			return PriceSyncRun{}, empty, ErrQuoteNotApplicable
		}
		if q.Locked && !in.ForceLocked {
			return PriceSyncRun{}, empty, ErrPriceProfileLocked
		}
		if q.UpstreamID != nil {
			if _, err := uuid.Parse(*q.UpstreamID); err != nil {
				return PriceSyncRun{}, empty, ErrQuoteNotApplicable
			}
		}
		if q.ModelID == "" {
			return PriceSyncRun{}, empty, ErrQuoteNotApplicable
		}
		key := q.ModelID + "\x00"
		if q.UpstreamID != nil {
			key += *q.UpstreamID
		}
		if seenTarget[key] {
			return PriceSyncRun{}, empty, ErrQuoteNotApplicable
		}
		seenTarget[key] = true
		selected = append(selected, q)
	}
	sort.Slice(selected, func(i, j int) bool {
		if selected[i].ModelID != selected[j].ModelID {
			return selected[i].ModelID < selected[j].ModelID
		}
		return upstreamKey(selected[i]) < upstreamKey(selected[j])
	})

	// Lock every target model, capability, and profile row before any baseline
	// comparison. A nil baseline is rechecked as "still absent" after the locks.
	lockedModels := map[string]bool{}
	for _, q := range selected {
		if lockedModels[q.ModelID] {
			continue
		}
		if err = lockModel(ctx, tx, q.ModelID); err != nil {
			return PriceSyncRun{}, empty, err
		}
		lockedModels[q.ModelID] = true
	}
	for _, q := range selected {
		if q.UpstreamID == nil {
			continue
		}
		if err = lockCapability(ctx, tx, q.ModelID, *q.UpstreamID); err != nil {
			return PriceSyncRun{}, empty, err
		}
	}
	current := map[string]priceCurrent{}
	for _, q := range selected {
		key := targetKey(q.ModelID, q.UpstreamID)
		c, err := readTargetRevision(ctx, tx, q.ModelID, q.UpstreamID)
		if err != nil {
			return PriceSyncRun{}, empty, err
		}
		current[key] = c
	}
	for _, q := range selected {
		c := current[targetKey(q.ModelID, q.UpstreamID)]
		if q.BasePriceRevisionID == nil {
			if c.exists {
				return PriceSyncRun{}, empty, ErrStalePriceQuote
			}
			continue
		}
		if !c.exists || c.revision != *q.BasePriceRevisionID {
			return PriceSyncRun{}, empty, ErrStalePriceQuote
		}
	}
	for _, q := range selected {
		if current[targetKey(q.ModelID, q.UpstreamID)].locked && !in.ForceLocked {
			return PriceSyncRun{}, empty, ErrPriceProfileLocked
		}
	}

	applied := make([]string, 0, len(selected))
	for _, q := range selected {
		c := current[targetKey(q.ModelID, q.UpstreamID)]
		revision, err := appendQuotedRevision(ctx, tx, run.SourceID, q, c)
		if err != nil {
			return PriceSyncRun{}, empty, err
		}
		applied = append(applied, revision)
	}
	run, err = scanPriceRun(tx.QueryRow(ctx, `
update price_sync_runs
set price_revision_ids=price_revision_ids || $2::text[]::uuid[],applied_at=now(),state='applied'
where id=$1 returning `+priceRunColumns, runID, applied))
	if err != nil {
		return PriceSyncRun{}, empty, err
	}
	return run, applied, nil
}

type priceCurrent struct {
	exists   bool
	revision string
	locked   bool
}

func lockModel(ctx context.Context, tx pgx.Tx, modelID string) error {
	var one int
	err := tx.QueryRow(ctx, "select 1 from models where model_id=$1 for update", modelID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrQuoteNotApplicable
	}
	return normalize(err)
}

func lockCapability(ctx context.Context, tx pgx.Tx, modelID, upstreamID string) error {
	var one int
	err := tx.QueryRow(ctx, "select 1 from upstream_models where model_id=$1 and upstream_id=$2 for update", modelID, upstreamID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrQuoteNotApplicable
	}
	return normalize(err)
}

func readTargetRevision(ctx context.Context, tx pgx.Tx, modelID string, upstreamID *string) (priceCurrent, error) {
	var c priceCurrent
	var err error
	if upstreamID == nil {
		err = tx.QueryRow(ctx, `
select m.billing_revision_id::text,r.locked
from models m join price_revisions r on r.id=m.billing_revision_id
where m.model_id=$1`, modelID).Scan(&c.revision, &c.locked)
		if errors.Is(err, pgx.ErrNoRows) {
			return c, ErrQuoteNotApplicable
		}
		if err != nil {
			return c, normalize(err)
		}
		c.exists = true
		return c, nil
	}
	err = tx.QueryRow(ctx, `
select price_revision_id::text,locked from model_prices
where model_id=$1 and upstream_id=$2`, modelID, *upstreamID).Scan(&c.revision, &c.locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, nil
	}
	if err != nil {
		return c, normalize(err)
	}
	c.exists = true
	return c, nil
}

func appendQuotedRevision(ctx context.Context, tx pgx.Tx, sourceID string, q PriceQuote, c priceCurrent) (string, error) {
	b := *q.Proposed
	if q.UpstreamID == nil {
		revision, err := AppendGlobalRevision(ctx, tx, q.ModelID, b, "price_source", &sourceID, c.locked)
		if err != nil {
			return "", err
		}
		input, cached, output := roundedBilling(b)
		tag, err := tx.Exec(ctx, `
update models set input_usd_per_mtok=$2,cached_input_usd_per_mtok=$3,output_usd_per_mtok=$4,
  billing_revision_id=$5,billing_revision_scope='global',updated_at=now()
where model_id=$1`, q.ModelID, input, cached, output, revision)
		if err != nil {
			return "", normalize(err)
		}
		if tag.RowsAffected() == 0 {
			return "", ErrStalePriceQuote
		}
		return revision, nil
	}
	revision := uuid.NewString()
	input, cached, output := roundedBilling(b)
	var returned string
	err := tx.QueryRow(ctx, `
insert into price_revisions(id,model_id,upstream_id,input_usd_per_mtok,cached_input_usd_per_mtok,output_usd_per_mtok,source,source_id,locked,scope)
values($1,$2,$3,$4,$5,$6,'price_source',$7,$8,'upstream') returning id::text`,
		revision, q.ModelID, *q.UpstreamID, input, cached, output, sourceID, c.locked).Scan(&returned)
	if err != nil {
		return "", normalize(err)
	}
	tag, err := tx.Exec(ctx, `
update model_prices set input_usd_per_mtok=$3,cached_input_usd_per_mtok=$4,output_usd_per_mtok=$5,
  source='price_source',locked=$6,price_revision_id=$7,updated_at=now()
where model_id=$1 and upstream_id=$2`,
		q.ModelID, *q.UpstreamID, input, cached, output, c.locked, returned)
	if err != nil {
		return "", normalize(err)
	}
	if tag.RowsAffected() == 0 {
		return "", ErrStalePriceQuote
	}
	return returned, nil
}

func loadPriceTargets(ctx context.Context, tx pgx.Tx) ([]pricesource.Target, error) {
	out := []pricesource.Target{}
	rows, err := tx.Query(ctx, `
select m.model_id,m.input_usd_per_mtok::float8,m.cached_input_usd_per_mtok::float8,m.output_usd_per_mtok::float8,
       m.billing_revision_id::text,m.updated_at,r.locked
from models m join price_revisions r on r.id=m.billing_revision_id
order by m.model_id`)
	if err != nil {
		return nil, normalize(err)
	}
	for rows.Next() {
		var modelID, revision string
		var input, cached, output *float64
		var updated time.Time
		var locked bool
		if err = rows.Scan(&modelID, &input, &cached, &output, &revision, &updated, &locked); err != nil {
			rows.Close()
			return nil, normalize(err)
		}
		out = append(out, pricesource.Target{
			ModelID: modelID, RevisionID: &revision, UpdatedAt: &updated, Locked: locked,
			Current: &pricesource.Billing{Input: input, Cached: cached, Output: output},
			Names:   []string{modelID},
		})
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, normalize(err)
	}
	rows.Close()

	rows, err = tx.Query(ctx, `
select um.model_id,um.upstream_id::text,um.upstream_model_name,um.catalog_model_id,
       mp.price_revision_id::text,mp.input_usd_per_mtok::float8,mp.cached_input_usd_per_mtok::float8,
       mp.output_usd_per_mtok::float8,coalesce(mp.locked,false),mp.updated_at
from upstream_models um
left join model_prices mp on mp.model_id=um.model_id and mp.upstream_id=um.upstream_id
order by um.model_id,um.upstream_id`)
	if err != nil {
		return nil, normalize(err)
	}
	for rows.Next() {
		var modelID, upstreamID string
		var upstreamName, catalogID, revision *string
		var input, cached, output *float64
		var locked bool
		var updated *time.Time
		if err = rows.Scan(&modelID, &upstreamID, &upstreamName, &catalogID, &revision,
			&input, &cached, &output, &locked, &updated); err != nil {
			rows.Close()
			return nil, normalize(err)
		}
		names := []string{}
		if upstreamName != nil && *upstreamName != "" {
			names = append(names, *upstreamName)
		} else {
			names = append(names, modelID)
		}
		if catalogID != nil && *catalogID != "" {
			names = append(names, *catalogID)
		}
		target := pricesource.Target{
			ModelID: modelID, UpstreamID: &upstreamID, RevisionID: revision,
			UpdatedAt: updated, Locked: locked, Names: uniqueStrings(names),
		}
		if revision != nil {
			target.Current = &pricesource.Billing{Input: input, Cached: cached, Output: output}
		}
		out = append(out, target)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, normalize(err)
	}
	rows.Close()

	rows, err = tx.Query(ctx, `
select mp.model_id,mp.upstream_id::text,mp.price_revision_id::text,mp.input_usd_per_mtok::float8,
       mp.cached_input_usd_per_mtok::float8,mp.output_usd_per_mtok::float8,mp.locked,mp.updated_at
from model_prices mp
where not exists (
  select 1 from upstream_models um where um.model_id=mp.model_id and um.upstream_id=mp.upstream_id
)
order by mp.model_id,mp.upstream_id`)
	if err != nil {
		return nil, normalize(err)
	}
	for rows.Next() {
		var modelID, upstreamID string
		var revision *string
		var input, cached, output *float64
		var locked bool
		var updated *time.Time
		if err = rows.Scan(&modelID, &upstreamID, &revision, &input, &cached, &output, &locked, &updated); err != nil {
			rows.Close()
			return nil, normalize(err)
		}
		out = append(out, pricesource.Target{
			ModelID: modelID, UpstreamID: &upstreamID, RevisionID: revision,
			UpdatedAt: updated, Locked: locked,
			Current: &pricesource.Billing{Input: input, Cached: cached, Output: output},
			Names:   []string{},
		})
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, normalize(err)
	}
	rows.Close()
	sort.Slice(out, func(i, j int) bool {
		if out[i].ModelID != out[j].ModelID {
			return out[i].ModelID < out[j].ModelID
		}
		return upstreamTargetKey(out[i]) < upstreamTargetKey(out[j])
	})
	return out, nil
}

func storeQuote(q pricesource.Quote) PriceQuote {
	out := PriceQuote{
		ID:                  uuid.NewString(),
		ModelID:             q.ModelID,
		UpstreamID:          q.UpstreamID,
		SourceModelID:       q.SourceModelID,
		MatchMethod:         q.MatchMethod,
		Status:              q.Status,
		Locked:              q.Locked,
		BasePriceRevisionID: q.BasePriceRevisionID,
		BaseUpdatedAt:       q.BaseUpdatedAt,
	}
	if q.Current != nil {
		out.Current = &Billing{Input: q.Current.Input, Cached: q.Current.Cached, Output: q.Current.Output}
	}
	if q.Proposed != nil {
		out.Proposed = &Billing{Input: q.Proposed.Input, Cached: q.Proposed.Cached, Output: q.Proposed.Output}
	}
	return out
}

func targetKey(modelID string, upstreamID *string) string {
	if upstreamID == nil {
		return modelID + "\x00"
	}
	return modelID + "\x00" + *upstreamID
}

func upstreamKey(q PriceQuote) string {
	if q.UpstreamID == nil {
		return ""
	}
	return *q.UpstreamID
}

func upstreamTargetKey(t pricesource.Target) string {
	if t.UpstreamID == nil {
		return ""
	}
	return *t.UpstreamID
}

func containsString(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}

func uniqueStrings(items []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out
}

func validateQuoteBilling(b Billing) string {
	for name, v := range map[string]*float64{
		"input_usd_per_mtok":        b.Input,
		"cached_input_usd_per_mtok": b.Cached,
		"output_usd_per_mtok":       b.Output,
	} {
		if v != nil && (*v < 0 || *v >= 1e8 || math.IsNaN(*v) || math.IsInf(*v, 0)) {
			return name
		}
	}
	return ""
}

func priceAuditPayload(values map[string]any) json.RawMessage {
	raw, err := json.Marshal(values)
	if err != nil {
		return json.RawMessage("{}")
	}
	return raw
}

func rejectionCode(err error) string {
	switch {
	case errors.Is(err, ErrStalePriceQuote):
		return "stale_price_quote"
	case errors.Is(err, ErrQuoteReviewRequired):
		return "quote_review_required"
	case errors.Is(err, ErrPriceProfileLocked):
		return "price_profile_locked"
	case errors.Is(err, ErrPriceRunNotReady):
		return "price_run_not_ready"
	case errors.Is(err, ErrQuoteNotApplicable):
		return "quote_not_applicable"
	default:
		return "storage_error"
	}
}
