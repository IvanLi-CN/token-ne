package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

const authSettingsColumns = "revision,oidc_enabled,issuer_url,client_id,client_secret_ciphertext,client_secret_nonce,scopes,local_login_enabled,session_ttl_hours,updated_at,updated_by::text"

func scanAuthSettings(row pgx.Row) (AuthSettings, error) {
	var v AuthSettings
	e := row.Scan(&v.Revision, &v.OIDCEnabled, &v.IssuerURL, &v.ClientID, &v.ClientSecretCiphertext, &v.ClientSecretNonce, &v.Scopes, &v.LocalLoginEnabled, &v.SessionTTLHours, &v.UpdatedAt, &v.UpdatedBy)
	return v, normalize(e)
}

func (s *Store) AuthSettings(ctx context.Context) (AuthSettings, error) {
	return scanAuthSettings(s.Pool.QueryRow(ctx, "select "+authSettingsColumns+" from auth_settings where singleton"))
}

func (s *Store) SaveAuthSettings(ctx context.Context, v AuthSettings, expectedRevision int64, clearBinding bool, bumpAdminID string, a Audit) (AuthSettings, error) {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return AuthSettings{}, e
	}
	defer tx.Rollback(ctx)
	var current int64
	e = tx.QueryRow(ctx, "select revision from auth_settings where singleton for update").Scan(&current)
	switch {
	case errors.Is(e, pgx.ErrNoRows):
		if expectedRevision != 0 {
			return AuthSettings{}, ErrConflict
		}
		v, e = scanAuthSettings(tx.QueryRow(ctx,
			"insert into auth_settings(oidc_enabled,issuer_url,client_id,client_secret_ciphertext,client_secret_nonce,scopes,local_login_enabled,session_ttl_hours,revision,updated_by) values($1,$2,$3,$4,$5,$6,$7,$8,1,$9) returning "+authSettingsColumns,
			v.OIDCEnabled, v.IssuerURL, v.ClientID, v.ClientSecretCiphertext, v.ClientSecretNonce, v.Scopes, v.LocalLoginEnabled, v.SessionTTLHours, v.UpdatedBy))
	case e != nil:
		return AuthSettings{}, normalize(e)
	default:
		if current != expectedRevision {
			return AuthSettings{}, ErrConflict
		}
		v, e = scanAuthSettings(tx.QueryRow(ctx,
			"update auth_settings set oidc_enabled=$1,issuer_url=$2,client_id=$3,client_secret_ciphertext=$4,client_secret_nonce=$5,scopes=$6,local_login_enabled=$7,session_ttl_hours=$8,revision=revision+1,updated_at=now(),updated_by=$9 where singleton returning "+authSettingsColumns,
			v.OIDCEnabled, v.IssuerURL, v.ClientID, v.ClientSecretCiphertext, v.ClientSecretNonce, v.Scopes, v.LocalLoginEnabled, v.SessionTTLHours, v.UpdatedBy))
	}
	if e != nil {
		return AuthSettings{}, normalize(e)
	}
	if clearBinding {
		if _, e = tx.Exec(ctx, "delete from admin_oidc_identities"); e != nil {
			return AuthSettings{}, normalize(e)
		}
	}
	if bumpAdminID != "" {
		tag, err := tx.Exec(ctx, "update admin_users set session_version=session_version+1,updated_at=now() where id=$1", bumpAdminID)
		if err != nil {
			return AuthSettings{}, normalize(err)
		}
		if tag.RowsAffected() == 0 {
			return AuthSettings{}, ErrNotFound
		}
	}
	if e = writeAudit(ctx, tx, a); e != nil {
		return AuthSettings{}, normalize(e)
	}
	if e = tx.Commit(ctx); e != nil {
		return AuthSettings{}, normalize(e)
	}
	return v, nil
}

func (s *Store) OIDCBinding(ctx context.Context, adminID string) (OIDCBinding, error) {
	return scanOIDCBinding(s.Pool.QueryRow(ctx, "select issuer,subject,updated_at,admin_id::text from admin_oidc_identities where admin_id=$1", adminID))
}

func (s *Store) OIDCBindingAny(ctx context.Context) (OIDCBinding, error) {
	return scanOIDCBinding(s.Pool.QueryRow(ctx, "select issuer,subject,updated_at,admin_id::text from admin_oidc_identities order by updated_at desc limit 1"))
}

func scanOIDCBinding(row pgx.Row) (OIDCBinding, error) {
	var v OIDCBinding
	e := row.Scan(&v.Issuer, &v.Subject, &v.BoundAt, &v.AdminID)
	return v, normalize(e)
}

func (s *Store) ReplaceOIDCBinding(ctx context.Context, adminID, issuer, subject string, a Audit) (int, error) {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return 0, e
	}
	defer tx.Rollback(ctx)
	var version int
	if e = tx.QueryRow(ctx, "update admin_users set session_version=session_version+1,updated_at=now() where id=$1 returning session_version", adminID).Scan(&version); e != nil {
		return 0, normalize(e)
	}
	if _, e = tx.Exec(ctx, "insert into admin_oidc_identities(admin_id,issuer,subject) values($1,$2,$3) on conflict (admin_id) do update set issuer=excluded.issuer,subject=excluded.subject,updated_at=now()", adminID, issuer, subject); e != nil {
		return 0, normalize(e)
	}
	if e = writeAudit(ctx, tx, a); e != nil {
		return 0, normalize(e)
	}
	if e = tx.Commit(ctx); e != nil {
		return 0, normalize(e)
	}
	return version, nil
}

const authFlowColumns = "id::text,state_hash,nonce,pkce_verifier_ciphertext,pkce_verifier_nonce,mode,admin_id::text,return_to,expires_at,consumed_at"

func scanAuthFlow(row pgx.Row) (AuthFlow, error) {
	var v AuthFlow
	e := row.Scan(&v.ID, &v.StateHash, &v.Nonce, &v.PKCEVerifierCiphertext, &v.PKCEVerifierNonce, &v.Mode, &v.AdminID, &v.ReturnTo, &v.ExpiresAt, &v.ConsumedAt)
	return v, normalize(e)
}

func (s *Store) CreateAuthFlow(ctx context.Context, f AuthFlow) error {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "delete from auth_flows where expires_at < now()"); e != nil {
		return normalize(e)
	}
	if _, e = tx.Exec(ctx, "insert into auth_flows(id,state_hash,nonce,pkce_verifier_ciphertext,pkce_verifier_nonce,mode,admin_id,return_to,expires_at) values($1,$2,$3,$4,$5,$6,$7,$8,$9)", f.ID, f.StateHash, f.Nonce, f.PKCEVerifierCiphertext, f.PKCEVerifierNonce, f.Mode, f.AdminID, f.ReturnTo, f.ExpiresAt); e != nil {
		return normalize(e)
	}
	return tx.Commit(ctx)
}

func (s *Store) AuthFlow(ctx context.Context, stateHash []byte) (AuthFlow, error) {
	return scanAuthFlow(s.Pool.QueryRow(ctx, "select "+authFlowColumns+" from auth_flows where state_hash=$1", stateHash))
}

func (s *Store) ConsumeAuthFlow(ctx context.Context, id string, now time.Time) error {
	tag, e := s.Pool.Exec(ctx, "update auth_flows set consumed_at=$2 where id=$1 and consumed_at is null and expires_at > $2", id, now)
	if e != nil {
		return normalize(e)
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	return nil
}
