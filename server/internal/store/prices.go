package store

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// billingColumns carries the six token dimensions at numeric(14,6) scale.
type billingColumns struct {
	input, cached, write, audioIn, audioOut, reasoning, output *float64
}

func (v billingColumns) params() []any {
	return []any{v.input, v.cached, v.write, v.audioIn, v.audioOut, v.reasoning, v.output}
}

// AppendGlobalRevision appends one immutable global price revision inside the
// caller's transaction. Directory import (T4) and SaveModel both use it so a
// model's pointer and its first revision commit together.
func AppendGlobalRevision(ctx context.Context, tx pgx.Tx, modelID string, b Billing, source string, sourceID *string, locked bool) (string, error) {
	id := uuid.NewString()
	tiers, err := marshalTiers(b.Tiers)
	if err != nil {
		return "", err
	}
	var returned string
	err = tx.QueryRow(ctx, `
insert into price_revisions(id,model_id,upstream_id,input_usd_per_mtok,cached_input_usd_per_mtok,cache_write_usd_per_mtok,audio_input_usd_per_mtok,audio_output_usd_per_mtok,reasoning_usd_per_mtok,output_usd_per_mtok,tiers,source,source_id,locked,scope)
values($1,$2,null,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,'global') returning id::text`,
		append([]any{id, modelID}, append(roundedBilling(b).params(), tiers, source, sourceID, locked)...)...).Scan(&returned)
	return returned, normalize(err)
}

func appendUpstreamRevision(ctx context.Context, tx pgx.Tx, modelID, upstreamID string, b Billing, locked bool) (string, error) {
	id := uuid.NewString()
	tiers, err := marshalTiers(b.Tiers)
	if err != nil {
		return "", err
	}
	var returned string
	err = tx.QueryRow(ctx, `
insert into price_revisions(id,model_id,upstream_id,input_usd_per_mtok,cached_input_usd_per_mtok,cache_write_usd_per_mtok,audio_input_usd_per_mtok,audio_output_usd_per_mtok,reasoning_usd_per_mtok,output_usd_per_mtok,tiers,source,source_id,locked,scope)
values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'manual',null,$12,'upstream') returning id::text`,
		append([]any{id, modelID, upstreamID}, append(roundedBilling(b).params(), tiers, locked)...)...).Scan(&returned)
	return returned, normalize(err)
}

func marshalTiers(tiers []PriceTier) ([]byte, error) {
	if len(tiers) == 0 {
		return []byte("[]"), nil
	}
	return json.Marshal(tiers)
}

func unmarshalTiers(raw []byte) []PriceTier {
	tiers := []PriceTier{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &tiers)
	}
	return tiers
}

// roundedBilling applies the numeric(14,6) scale before comparison and
// insertion so resubmitting the same JSON number reuses the revision instead
// of appending one per floating-point representation.
func roundedBilling(b Billing) billingColumns {
	return billingColumns{
		input: roundedPrice(b.Input), cached: roundedPrice(b.Cached), write: roundedPrice(b.CacheWrite),
		audioIn: roundedPrice(b.AudioIn), audioOut: roundedPrice(b.AudioOut),
		reasoning: roundedPrice(b.Reasoning), output: roundedPrice(b.Output),
	}
}

func roundedPrice(v *float64) *float64 {
	if v == nil {
		return nil
	}
	r := math.Round(*v*1e6) / 1e6
	return &r
}

func sameBilling(a, b Billing) bool {
	av, bv := roundedBilling(a), roundedBilling(b)
	if !samePrice(av.input, bv.input) || !samePrice(av.cached, bv.cached) || !samePrice(av.write, bv.write) ||
		!samePrice(av.audioIn, bv.audioIn) || !samePrice(av.audioOut, bv.audioOut) ||
		!samePrice(av.reasoning, bv.reasoning) || !samePrice(av.output, bv.output) {
		return false
	}
	at, bt := a.Tiers, b.Tiers
	if len(at) != len(bt) {
		return false
	}
	for i := range at {
		if at[i].ThresholdTokens != bt[i].ThresholdTokens ||
			!samePrice(roundedPrice(at[i].Input), roundedPrice(bt[i].Input)) ||
			!samePrice(roundedPrice(at[i].Cached), roundedPrice(bt[i].Cached)) ||
			!samePrice(roundedPrice(at[i].CacheWrite), roundedPrice(bt[i].CacheWrite)) ||
			!samePrice(roundedPrice(at[i].AudioIn), roundedPrice(bt[i].AudioIn)) ||
			!samePrice(roundedPrice(at[i].AudioOut), roundedPrice(bt[i].AudioOut)) ||
			!samePrice(roundedPrice(at[i].Reasoning), roundedPrice(bt[i].Reasoning)) ||
			!samePrice(roundedPrice(at[i].Output), roundedPrice(bt[i].Output)) {
			return false
		}
	}
	return true
}

func samePrice(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func boolean(p *bool) bool { return p != nil && *p }

// saveModel runs inside SaveModel's audit transaction. A create appends the
// initial global revision and inserts the pointer atomically. An update reuses
// the current revision only when the rounded prices and lock state are
// unchanged; any price or lock transition appends a new immutable revision.
func saveModel(ctx context.Context, tx pgx.Tx, m Model, create bool) (Model, error) {
	if create {
		revision, err := AppendGlobalRevision(ctx, tx, m.ID, m.Billing, "manual", nil, boolean(m.BillingLocked))
		if err != nil {
			return Model{}, err
		}
		r := roundedBilling(m.Billing)
		_, err = tx.Exec(ctx, `
insert into models(model_id,enabled,shutdown_date,input_usd_per_mtok,cached_input_usd_per_mtok,output_usd_per_mtok,billing_revision_id,billing_revision_scope)
values($1,$2,$3::text::date,$4,$5,$6,$7,'global')`,
			m.ID, m.Enabled, m.Shutdown, r.input, r.cached, r.output, revision)
		if err != nil {
			return Model{}, normalize(err)
		}
	} else {
		var current Billing
		var revisionID, tiersJSON string
		var locked bool
		err := tx.QueryRow(ctx, `
select r.input_usd_per_mtok::float8,r.cached_input_usd_per_mtok::float8,r.cache_write_usd_per_mtok::float8,
       r.audio_input_usd_per_mtok::float8,r.audio_output_usd_per_mtok::float8,r.reasoning_usd_per_mtok::float8,
       r.output_usd_per_mtok::float8,r.tiers::text,m.billing_revision_id::text,r.locked
from models m join price_revisions r on r.id=m.billing_revision_id
where m.model_id=$1 for update of m`, m.ID).Scan(&current.Input, &current.Cached, &current.CacheWrite,
			&current.AudioIn, &current.AudioOut, &current.Reasoning, &current.Output, &tiersJSON, &revisionID, &locked)
		if errors.Is(err, pgx.ErrNoRows) {
			return Model{}, ErrNotFound
		}
		if err != nil {
			return Model{}, err
		}
		current.Tiers = unmarshalTiers([]byte(tiersJSON))
		desiredLocked := locked
		if m.BillingLocked != nil {
			desiredLocked = *m.BillingLocked
		}
		changed := !sameBilling(current, m.Billing) || desiredLocked != locked
		if locked && changed && !boolean(m.ConfirmLocked) {
			return Model{}, ErrPriceProfileLocked
		}
		if changed {
			revisionID, err = AppendGlobalRevision(ctx, tx, m.ID, m.Billing, "manual", nil, desiredLocked)
			if err != nil {
				return Model{}, err
			}
		}
		r := roundedBilling(m.Billing)
		tag, err := tx.Exec(ctx, `
update models
set enabled=$2,shutdown_date=$3::text::date,
    input_usd_per_mtok=$4,cached_input_usd_per_mtok=$5,output_usd_per_mtok=$6,
    billing_revision_id=$7,billing_revision_scope='global',updated_at=now()
where model_id=$1`,
			m.ID, m.Enabled, m.Shutdown, r.input, r.cached, r.output, revisionID)
		if err != nil {
			return Model{}, normalize(err)
		}
		if tag.RowsAffected() == 0 {
			return Model{}, ErrNotFound
		}
	}
	return scanModel(tx.QueryRow(ctx, "select "+modelColumns+" from models m join price_revisions r on r.id=m.billing_revision_id where m.model_id=$1", m.ID))
}

// Prices lists every upstream override with its current revision. Global
// defaults are exposed through model billing_state instead.
func (s *Store) Prices(ctx context.Context) ([]PriceProfile, error) {
	rows, err := s.Pool.Query(ctx, `
select mp.model_id,mp.upstream_id::text,mp.input_usd_per_mtok::float8,mp.cached_input_usd_per_mtok::float8,
       mp.cache_write_usd_per_mtok::float8,mp.audio_input_usd_per_mtok::float8,mp.audio_output_usd_per_mtok::float8,
       mp.reasoning_usd_per_mtok::float8,mp.output_usd_per_mtok::float8,mp.tiers::text,
       mp.locked,r.source,r.source_id,r.id::text,r.created_at,mp.updated_at
from model_prices mp join price_revisions r on r.id=mp.price_revision_id
order by mp.model_id,mp.upstream_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PriceProfile{}
	for rows.Next() {
		var p PriceProfile
		var tiers string
		if err = rows.Scan(&p.ModelID, &p.UpstreamID, &p.Input, &p.Cached, &p.CacheWrite, &p.AudioIn, &p.AudioOut, &p.Reasoning, &p.Output,
			&tiers, &p.Locked, &p.Source, &p.SourceID, &p.PriceRevisionID, &p.RevisionCreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		p.Tiers = unmarshalTiers([]byte(tiers))
		p.Scope = "upstream"
		out = append(out, p)
	}
	return out, rows.Err()
}

// SaveUpstreamPrice creates or replaces one upstream override and appends a
// revision only when the rounded prices or lock state change. A locked
// profile needs confirm=true for any price or lock transition.
func (s *Store) SaveUpstreamPrice(ctx context.Context, modelID, upstreamID string, b Billing, locked, confirm bool, a Audit) (PriceProfile, error) {
	var profile PriceProfile
	err := s.mutate(ctx, a, func(tx pgx.Tx) error {
		var modelExists, upstreamExists bool
		if err := tx.QueryRow(ctx, "select exists(select 1 from models where model_id=$1),exists(select 1 from upstreams where id=$2)", modelID, upstreamID).Scan(&modelExists, &upstreamExists); err != nil {
			return normalize(err)
		}
		if !modelExists || !upstreamExists {
			return ErrNotFound
		}

		var current PriceProfile
		var currentTiers string
		err := tx.QueryRow(ctx, `
select mp.input_usd_per_mtok::float8,mp.cached_input_usd_per_mtok::float8,
       mp.cache_write_usd_per_mtok::float8,mp.audio_input_usd_per_mtok::float8,mp.audio_output_usd_per_mtok::float8,
       mp.reasoning_usd_per_mtok::float8,mp.output_usd_per_mtok::float8,mp.tiers::text,
       mp.locked,r.source,r.source_id,r.id::text,r.created_at,mp.updated_at
from model_prices mp join price_revisions r on r.id=mp.price_revision_id
where mp.model_id=$1 and mp.upstream_id=$2 for update of mp`, modelID, upstreamID).
			Scan(&current.Input, &current.Cached, &current.CacheWrite, &current.AudioIn, &current.AudioOut, &current.Reasoning, &current.Output,
				&currentTiers, &current.Locked,
				&current.Source, &current.SourceID, &current.PriceRevisionID, &current.RevisionCreatedAt, &current.UpdatedAt)
		current.Tiers = unmarshalTiers([]byte(currentTiers))
		found := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		changed := !found || !sameBilling(current.Billing, b) || current.Locked != locked
		if found && current.Locked && changed && !confirm {
			return ErrPriceProfileLocked
		}
		profile = current
		profile.ModelID, profile.UpstreamID, profile.Scope = modelID, upstreamID, "upstream"
		v := roundedBilling(b)
		profile.Billing = Billing{
			Input: v.input, Cached: v.cached, CacheWrite: v.write,
			AudioIn: v.audioIn, AudioOut: v.audioOut, Reasoning: v.reasoning,
			Output: v.output, Tiers: b.Tiers,
		}
		profile.Locked = locked
		if !changed {
			return nil
		}

		tiersJSON, err := marshalTiers(b.Tiers)
		if err != nil {
			return err
		}
		revision, err := appendUpstreamRevision(ctx, tx, modelID, upstreamID, b, locked)
		if err != nil {
			return err
		}
		if found {
			err = tx.QueryRow(ctx, `
update model_prices
set input_usd_per_mtok=$3,cached_input_usd_per_mtok=$4,cache_write_usd_per_mtok=$5,
    audio_input_usd_per_mtok=$6,audio_output_usd_per_mtok=$7,reasoning_usd_per_mtok=$8,output_usd_per_mtok=$9,
    tiers=$10::jsonb,source='manual',locked=$11,price_revision_id=$12,updated_at=now()
where model_id=$1 and upstream_id=$2 returning updated_at`,
				modelID, upstreamID, v.input, v.cached, v.write, v.audioIn, v.audioOut, v.reasoning, v.output, tiersJSON, locked, revision).Scan(&profile.UpdatedAt)
		} else {
			err = tx.QueryRow(ctx, `
insert into model_prices(id,model_id,upstream_id,input_usd_per_mtok,cached_input_usd_per_mtok,cache_write_usd_per_mtok,
  audio_input_usd_per_mtok,audio_output_usd_per_mtok,reasoning_usd_per_mtok,output_usd_per_mtok,tiers,source,locked,price_revision_id)
values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::jsonb,'manual',$12,$13) returning updated_at`,
				uuid.NewString(), modelID, upstreamID, v.input, v.cached, v.write, v.audioIn, v.audioOut, v.reasoning, v.output, tiersJSON, locked, revision).Scan(&profile.UpdatedAt)
		}
		if err != nil {
			return normalize(err)
		}
		profile.Source, profile.SourceID = "manual", nil
		profile.PriceRevisionID = revision
		profile.RevisionCreatedAt = profile.UpdatedAt
		if created, err := revisionCreatedAt(ctx, tx, revision); err != nil {
			return err
		} else {
			profile.RevisionCreatedAt = created
		}
		return nil
	})
	return profile, err
}

func revisionCreatedAt(ctx context.Context, tx pgx.Tx, id string) (t time.Time, err error) {
	err = tx.QueryRow(ctx, "select created_at from price_revisions where id=$1", id).Scan(&t)
	return t, normalize(err)
}

// DeleteUpstreamPrice removes only the current override; historical revisions
// stay for usage attribution. A locked profile requires confirm.
func (s *Store) DeleteUpstreamPrice(ctx context.Context, modelID, upstreamID string, confirm bool, a Audit) error {
	return s.mutate(ctx, a, func(tx pgx.Tx) error {
		var locked bool
		err := tx.QueryRow(ctx, "select locked from model_prices where model_id=$1 and upstream_id=$2 for update", modelID, upstreamID).Scan(&locked)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return normalize(err)
		}
		if locked && !confirm {
			return ErrPriceProfileLocked
		}
		tag, err := tx.Exec(ctx, "delete from model_prices where model_id=$1 and upstream_id=$2", modelID, upstreamID)
		if err != nil {
			return normalize(err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}
