package store

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/IvanLi-CN/token-ne/server/internal/modelglob"
	"github.com/IvanLi-CN/token-ne/server/internal/pricesource"
	"github.com/jackc/pgx/v5"
)

// SourceModel is one row of a controlled price source joined with its local
// installation state. Vendor and dates come from the snapshot, never from a
// quote target.
type SourceModel struct {
	SourceModelID string     `json:"source_model_id"`
	Vendor        string     `json:"vendor"`
	ReleasedAt    *time.Time `json:"released_at"`
	DeprecatedAt  *time.Time `json:"deprecated_at"`
	Billing       Billing    `json:"billing"`
	ModelID       string     `json:"model_id"`
	Installed     bool       `json:"installed"`
	Synced        bool       `json:"synced"`
	WebSearch     *float64   `json:"web_search_usd_per_call"`
	CacheWrite1h  *float64   `json:"cache_write_1h_usd_per_mtok"`
}

// PriceSourceCatalog is the controllable browsing surface for one source.
type PriceSourceCatalog struct {
	SourceID  string        `json:"source_id"`
	FetchedAt time.Time     `json:"fetched_at"`
	Vendors   []string      `json:"vendors"`
	Data      []SourceModel `json:"data"`
}

// SourceModelInstall reports one row of a batch installation.
type SourceModelInstall struct {
	SourceModelID string `json:"source_model_id"`
	ModelID       string `json:"model_id"`
	Outcome       string `json:"outcome"`
	Reason        string `json:"reason,omitempty"`
}

// PriceSourceInstall is the batch result. Outcomes are created, exists and
// conflict; a conflict leaves every other row untouched.
type PriceSourceInstall struct {
	SourceID string               `json:"source_id"`
	Data     []SourceModelInstall `json:"data"`
}

// PriceSourceCatalog lists the source models once, with the installed flag
// resolved against the global model table.
func (s *Store) PriceSourceCatalog(ctx context.Context, sourceID string) (PriceSourceCatalog, error) {
	dataset, err := (pricesource.Builtin{}).Fetch(sourceID)
	if err != nil {
		if errors.Is(err, pricesource.ErrSourceNotFound) {
			return PriceSourceCatalog{}, ErrNotFound
		}
		return PriceSourceCatalog{}, normalize(err)
	}
	targets := make([]string, 0, len(dataset.Models))
	for _, entry := range dataset.Models {
		targets = append(targets, entry.TargetModelID())
	}
	installed := make(map[string]bool, len(targets))
	rows, err := s.Pool.Query(ctx, "select model_id from models where model_id = any($1)", targets)
	if err != nil {
		return PriceSourceCatalog{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return PriceSourceCatalog{}, err
		}
		installed[id] = true
	}
	if err = rows.Err(); err != nil {
		return PriceSourceCatalog{}, err
	}
	rows.Close()
	synced := make(map[string]bool, len(dataset.Models))
	markRows, err := s.Pool.Query(ctx, "select source_model_id from price_source_sync_marks where source_id=$1", sourceID)
	if err != nil {
		return PriceSourceCatalog{}, err
	}
	defer markRows.Close()
	for markRows.Next() {
		var id string
		if err = markRows.Scan(&id); err != nil {
			return PriceSourceCatalog{}, err
		}
		synced[id] = true
	}
	if err = markRows.Err(); err != nil {
		return PriceSourceCatalog{}, err
	}
	out := PriceSourceCatalog{SourceID: dataset.SourceID, FetchedAt: dataset.FetchedAt, Vendors: []string{}, Data: []SourceModel{}}
	seen := map[string]bool{}
	for _, entry := range dataset.Models {
		target := entry.TargetModelID()
		out.Data = append(out.Data, SourceModel{
			SourceModelID: entry.SourceModelID,
			Vendor:        entry.Vendor,
			ReleasedAt:    entry.ReleasedAt,
			DeprecatedAt:  entry.DeprecatedAt,
			Billing: Billing{
				Input: entry.Input, Cached: entry.Cached, CacheWrite: entry.CacheWrite,
				AudioIn: entry.AudioIn, AudioOut: entry.AudioOut,
				Reasoning: entry.Reasoning, Output: entry.Output,
				Tiers: catalogTiers(entry.Tiers),
			},
			ModelID:      target,
			Installed:    installed[target],
			Synced:       synced[entry.SourceModelID],
			WebSearch:    entry.WebSearch,
			CacheWrite1h: entry.CacheWrite1h,
		})
		if entry.Vendor != "" && !seen[entry.Vendor] {
			seen[entry.Vendor] = true
			out.Vendors = append(out.Vendors, entry.Vendor)
		}
	}
	sort.Strings(out.Vendors)
	return out, nil
}

// InstallPriceSourceModels creates disabled global models (with their initial
// price revision) for the selected source rows. Existing models and names
// reserved by route rules are reported instead of overwritten.
func (s *Store) InstallPriceSourceModels(ctx context.Context, sourceID string, ids []string, a Audit) (PriceSourceInstall, error) {
	dataset, err := (pricesource.Builtin{}).Fetch(sourceID)
	if err != nil {
		if errors.Is(err, pricesource.ErrSourceNotFound) {
			return PriceSourceInstall{}, ErrNotFound
		}
		return PriceSourceInstall{}, normalize(err)
	}
	byID := make(map[string]pricesource.Entry, len(dataset.Models))
	for _, entry := range dataset.Models {
		byID[entry.SourceModelID] = entry
	}
	out := PriceSourceInstall{SourceID: sourceID, Data: []SourceModelInstall{}}
	seen := make(map[string]bool, len(ids))
	err = s.mutate(ctx, a, func(tx pgx.Tx) error {
		for _, id := range ids {
			if seen[id] {
				continue
			}
			seen[id] = true
			entry, ok := byID[id]
			if !ok {
				out.Data = append(out.Data, SourceModelInstall{SourceModelID: id, Outcome: "conflict", Reason: "source model not found"})
				continue
			}
			row, err := ensureSourceModel(ctx, tx, sourceID, entry)
			if err != nil {
				return err
			}
			if row.Outcome != "conflict" {
				if err = markSourceModel(ctx, tx, sourceID, entry.SourceModelID, row.ModelID); err != nil {
					return err
				}
			}
			out.Data = append(out.Data, row)
		}
		return nil
	})
	return out, err
}

// ensureSourceModel creates the global model for one source entry without
// touching an existing model or a name reserved by a route rule.
func ensureSourceModel(ctx context.Context, tx pgx.Tx, sourceID string, entry pricesource.Entry) (SourceModelInstall, error) {
	target := entry.TargetModelID()
	out := SourceModelInstall{SourceModelID: entry.SourceModelID, ModelID: target}
	// Same name-reservation rule as model creation and catalog import.
	if _, err := tx.Exec(ctx, "select pg_advisory_xact_lock($1)", modelglob.AdvisoryKey(target)); err != nil {
		return out, err
	}
	var exists bool
	if err := tx.QueryRow(ctx, "select exists(select 1 from models where model_id=$1)", target).Scan(&exists); err != nil {
		return out, err
	}
	if exists {
		out.Outcome = "exists"
		return out, nil
	}
	var reserved bool
	if err := tx.QueryRow(ctx, "select exists(select 1 from model_route_rules where pattern=$1)", target).Scan(&reserved); err != nil {
		return out, err
	}
	if reserved {
		out.Outcome = "conflict"
		out.Reason = "a route rule already uses this pattern"
		return out, nil
	}
	billing := Billing{
		Input: entry.Input, Cached: entry.Cached, CacheWrite: entry.CacheWrite,
		AudioIn: entry.AudioIn, AudioOut: entry.AudioOut,
		Reasoning: entry.Reasoning, Output: entry.Output,
		Tiers: catalogTiers(entry.Tiers),
	}
	revision, err := AppendGlobalRevision(ctx, tx, target, billing, "price_source", &sourceID, false)
	if err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `
insert into models(model_id,enabled,default_upstream_model,shutdown_date,input_usd_per_mtok,cached_input_usd_per_mtok,output_usd_per_mtok,billing_revision_id,billing_revision_scope)
values($1,false,null,null,$2,$3,$4,$5,'global')`, target, billing.Input, billing.Cached, billing.Output, revision); err != nil {
		return out, normalize(err)
	}
	out.Outcome = "created"
	return out, nil
}

func markSourceModel(ctx context.Context, tx pgx.Tx, sourceID, sourceModelID, modelID string) error {
	_, err := tx.Exec(ctx, `
insert into price_source_sync_marks(source_id, source_model_id, model_id, marked_at)
values($1,$2,$3,now())
on conflict (source_id, source_model_id) do update set model_id = excluded.model_id, marked_at = now()`,
		sourceID, sourceModelID, modelID)
	return err
}

// ClearPriceSourceSyncMarks drops marks for the selected rows and never touches
// the global models they point at.
func (s *Store) ClearPriceSourceSyncMarks(ctx context.Context, sourceID string, ids []string, a Audit) (int, error) {
	if _, err := (pricesource.Builtin{}).Fetch(sourceID); err != nil {
		if errors.Is(err, pricesource.ErrSourceNotFound) {
			return 0, ErrNotFound
		}
		return 0, normalize(err)
	}
	cleared := 0
	err := s.mutate(ctx, a, func(tx pgx.Tx) error {
		tag, e := tx.Exec(ctx, "delete from price_source_sync_marks where source_id=$1 and source_model_id = any($2)", sourceID, ids)
		if e != nil {
			return e
		}
		cleared = int(tag.RowsAffected())
		return nil
	})
	return cleared, err
}

// catalogTiers copies the source tiers into the store shape.
func catalogTiers(tiers []pricesource.Tier) []PriceTier {
	out := make([]PriceTier, 0, len(tiers))
	for _, tier := range tiers {
		out = append(out, PriceTier{
			ThresholdTokens: tier.ThresholdTokens,
			Input:           tier.Input, Cached: tier.Cached, CacheWrite: tier.CacheWrite,
			AudioIn: tier.AudioIn, AudioOut: tier.AudioOut, Reasoning: tier.Reasoning, Output: tier.Output,
		})
	}
	return out
}
