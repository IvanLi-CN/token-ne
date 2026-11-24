package store

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// AppendGlobalRevision appends one immutable global price revision inside the
// caller's transaction. Directory import (T4) and SaveModel both use it so a
// model's pointer and its first revision commit together.
func AppendGlobalRevision(ctx context.Context, tx pgx.Tx, modelID string, b Billing, source string, sourceID *string, locked bool) (string, error) {
	id := uuid.NewString()
	input, cached, output := roundedBilling(b)
	err := tx.QueryRow(ctx, `
insert into price_revisions(id,model_id,upstream_id,input_usd_per_mtok,cached_input_usd_per_mtok,output_usd_per_mtok,source,source_id,locked,scope)
values($1,$2,null,$3,$4,$5,$6,$7,$8,'global') returning id::text`,
		id, modelID, input, cached, output, source, sourceID, locked).Scan(&id)
	return id, normalize(err)
}

func appendUpstreamRevision(ctx context.Context, tx pgx.Tx, modelID, upstreamID string, b Billing, locked bool) (string, error) {
	id := uuid.NewString()
	input, cached, output := roundedBilling(b)
	var returned string
	err := tx.QueryRow(ctx, `
insert into price_revisions(id,model_id,upstream_id,input_usd_per_mtok,cached_input_usd_per_mtok,output_usd_per_mtok,source,source_id,locked,scope)
values($1,$2,$3,$4,$5,$6,'manual',null,$7,'upstream') returning id::text`,
		id, modelID, upstreamID, input, cached, output, locked).Scan(&returned)
	return returned, normalize(err)
}

// roundedBilling applies the numeric(14,6) scale before comparison and
// insertion so resubmitting the same JSON number reuses the revision instead
// of appending one per floating-point representation.
func roundedBilling(b Billing) (*float64, *float64, *float64) {
	return roundedPrice(b.Input), roundedPrice(b.Cached), roundedPrice(b.Output)
}

func roundedPrice(v *float64) *float64 {
	if v == nil {
		return nil
	}
	r := math.Round(*v*1e6) / 1e6
	return &r
}

func sameBilling(a, b Billing) bool {
	ai, ac, ao := roundedBilling(a)
	bi, bc, bo := roundedBilling(b)
	return samePrice(ai, bi) && samePrice(ac, bc) && samePrice(ao, bo)
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
		_, err = tx.Exec(ctx, `
insert into models(model_id,enabled,default_upstream_model,shutdown_date,input_usd_per_mtok,cached_input_usd_per_mtok,output_usd_per_mtok,billing_revision_id,billing_revision_scope)
values($1,$2,$3,$4::text::date,$5,$6,$7,$8,'global')`,
			m.ID, m.Enabled, m.Mapping, m.Shutdown, m.Billing.Input, m.Billing.Cached, m.Billing.Output, revision)
		if err != nil {
			return Model{}, normalize(err)
		}
	} else {
		var current Billing
		var revisionID string
		var locked bool
		err := tx.QueryRow(ctx, `
select m.input_usd_per_mtok::float8,m.cached_input_usd_per_mtok::float8,m.output_usd_per_mtok::float8,m.billing_revision_id::text,r.locked
from models m join price_revisions r on r.id=m.billing_revision_id
where m.model_id=$1 for update of m`, m.ID).Scan(&current.Input, &current.Cached, &current.Output, &revisionID, &locked)
		if errors.Is(err, pgx.ErrNoRows) {
			return Model{}, ErrNotFound
		}
		if err != nil {
			return Model{}, err
		}
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
		tag, err := tx.Exec(ctx, `
update models
set enabled=$2,default_upstream_model=$3,shutdown_date=$4::text::date,
    input_usd_per_mtok=$5,cached_input_usd_per_mtok=$6,output_usd_per_mtok=$7,
    billing_revision_id=$8,billing_revision_scope='global',updated_at=now()
where model_id=$1`,
			m.ID, m.Enabled, m.Mapping, m.Shutdown, m.Billing.Input, m.Billing.Cached, m.Billing.Output, revisionID)
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
select mp.model_id,mp.upstream_id::text,mp.input_usd_per_mtok::float8,mp.cached_input_usd_per_mtok::float8,mp.output_usd_per_mtok::float8,
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
		if err = rows.Scan(&p.ModelID, &p.UpstreamID, &p.Input, &p.Cached, &p.Output,
			&p.Locked, &p.Source, &p.SourceID, &p.PriceRevisionID, &p.RevisionCreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
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
		err := tx.QueryRow(ctx, `
select mp.input_usd_per_mtok::float8,mp.cached_input_usd_per_mtok::float8,mp.output_usd_per_mtok::float8,
       mp.locked,r.source,r.source_id,r.id::text,r.created_at,mp.updated_at
from model_prices mp join price_revisions r on r.id=mp.price_revision_id
where mp.model_id=$1 and mp.upstream_id=$2 for update of mp`, modelID, upstreamID).
			Scan(&current.Input, &current.Cached, &current.Output, &current.Locked,
				&current.Source, &current.SourceID, &current.PriceRevisionID, &current.RevisionCreatedAt, &current.UpdatedAt)
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
		profile.Billing = Billing{}
		profile.Input, profile.Cached, profile.Output = roundedBilling(b)
		profile.Locked = locked
		if !changed {
			return nil
		}

		revision, err := appendUpstreamRevision(ctx, tx, modelID, upstreamID, b, locked)
		if err != nil {
			return err
		}
		if found {
			err = tx.QueryRow(ctx, `
update model_prices
set input_usd_per_mtok=$3,cached_input_usd_per_mtok=$4,output_usd_per_mtok=$5,source='manual',locked=$6,price_revision_id=$7,updated_at=now()
where model_id=$1 and upstream_id=$2 returning updated_at`,
				modelID, upstreamID, profile.Input, profile.Cached, profile.Output, locked, revision).Scan(&profile.UpdatedAt)
		} else {
			err = tx.QueryRow(ctx, `
insert into model_prices(id,model_id,upstream_id,input_usd_per_mtok,cached_input_usd_per_mtok,output_usd_per_mtok,source,locked,price_revision_id)
values($1,$2,$3,$4,$5,$6,'manual',$7,$8) returning updated_at`,
				uuid.NewString(), modelID, upstreamID, profile.Input, profile.Cached, profile.Output, locked, revision).Scan(&profile.UpdatedAt)
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
