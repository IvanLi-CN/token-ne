package store

import (
	"context"

	"github.com/IvanLi-CN/token-ne/server/internal/auth"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const upstreamColumns = "id::text,name,kind,base_url,auth_style,api_key_hint,enabled,connect_timeout_ms,first_byte_timeout_ms,idle_timeout_ms,created_at,updated_at,api_key_ciphertext,api_key_nonce,egress_proxy_id::text"

func scanUpstream(row pgx.Row) (Upstream, error) {
	var u Upstream
	e := row.Scan(&u.ID, &u.Name, &u.Kind, &u.BaseURL, &u.AuthStyle, &u.Hint, &u.Enabled, &u.Connect, &u.FirstByte, &u.Idle, &u.Created, &u.Updated, &u.Ciphertext, &u.Nonce, &u.EgressProxyID)
	u.LastFour = u.Hint
	u.Masked = auth.Mask(u.Hint)
	u.Models = []Capability{}
	return u, normalize(e)
}
func (s *Store) capabilities(ctx context.Context, id string) ([]Capability, error) {
	rows, e := s.Pool.Query(ctx, "select upstream_model_id,model_id,enabled from upstream_models where upstream_id=$1 order by upstream_model_id", id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Capability{}
	for rows.Next() {
		var c Capability
		if e = rows.Scan(&c.UpstreamModelID, &c.ModelID, &c.Enabled); e != nil {
			return nil, e
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
func (s *Store) Upstream(ctx context.Context, id string) (Upstream, error) {
	u, e := scanUpstream(s.Pool.QueryRow(ctx, "select "+upstreamColumns+" from upstreams where id=$1", id))
	if e == nil {
		u.Models, e = s.capabilities(ctx, id)
	}
	return u, e
}
func (s *Store) Upstreams(ctx context.Context) ([]Upstream, error) {
	rows, e := s.Pool.Query(ctx, "select "+upstreamColumns+" from upstreams order by created_at,id")
	if e != nil {
		return nil, e
	}
	out := []Upstream{}
	for rows.Next() {
		u, e := scanUpstream(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		out = append(out, u)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	for i := range out {
		out[i].Models, e = s.capabilities(ctx, out[i].ID)
		if e != nil {
			return nil, e
		}
	}
	return out, nil
}
func (s *Store) SaveUpstream(ctx context.Context, u Upstream, create bool, a Audit) (Upstream, error) {
	if create && u.ID == "" {
		u.ID = uuid.NewString()
	}
	models := u.Models
	e := s.mutate(ctx, a, func(tx pgx.Tx) error {
		q := "update upstreams set name=$2,kind=$3,base_url=$4,auth_style=$5,api_key_hint=$6,enabled=$7,connect_timeout_ms=$8,first_byte_timeout_ms=$9,idle_timeout_ms=$10,api_key_ciphertext=$11,api_key_nonce=$12,egress_proxy_id=$13,updated_at=now() where id=$1 returning "
		if create {
			q = "insert into upstreams(id,name,kind,base_url,auth_style,api_key_hint,enabled,connect_timeout_ms,first_byte_timeout_ms,idle_timeout_ms,api_key_ciphertext,api_key_nonce,egress_proxy_id) values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) returning "
		}
		// A dangling binding is a field error so the API can answer 422 with
		// details[].path instead of a bare reference failure.
		if u.EgressProxyID != nil {
			var bound bool
			if err := tx.QueryRow(ctx, "select exists(select 1 from egress_proxies where id=$1)", *u.EgressProxyID).Scan(&bound); err != nil {
				return err
			}
			if !bound {
				return &FieldError{Path: "egress_proxy_id", Reason: "referenced egress proxy does not exist", Kind: ErrReference}
			}
		}
		var err error
		u, err = scanUpstream(tx.QueryRow(ctx, q+upstreamColumns, u.ID, u.Name, u.Kind, u.BaseURL, u.AuthStyle, u.Hint, u.Enabled, u.Connect, u.FirstByte, u.Idle, u.Ciphertext, u.Nonce, u.EgressProxyID))
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "delete from upstream_models where upstream_id=$1", u.ID); err != nil {
			return err
		}
		for _, c := range models {
			if _, err = tx.Exec(ctx, "insert into upstream_models(upstream_id,upstream_model_id,model_id,enabled) values($1,$2,$3,$4)", u.ID, c.UpstreamModelID, c.ModelID, c.Enabled); err != nil {
				return err
			}
		}
		return nil
	})
	u.Models = models
	return u, e
}

const keyColumns = "id::text,name,key_prefix,enabled,allowed_models,max_inflight,created_at,last_used_at,key_sha256"

func scanKey(row pgx.Row) (Key, error) {
	var k Key
	e := row.Scan(&k.ID, &k.Name, &k.Prefix, &k.Enabled, &k.Allowed, &k.MaxInflight, &k.Created, &k.LastUsed, &k.Hash)
	return k, normalize(e)
}
func (s *Store) Key(ctx context.Context, id string) (Key, error) {
	return scanKey(s.Pool.QueryRow(ctx, "select "+keyColumns+" from downstream_api_keys where id=$1", id))
}
func (s *Store) Keys(ctx context.Context) ([]Key, error) {
	rows, e := s.Pool.Query(ctx, "select "+keyColumns+" from downstream_api_keys order by created_at,id")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Key{}
	for rows.Next() {
		k, e := scanKey(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, k)
	}
	return out, rows.Err()
}
func (s *Store) SaveKey(ctx context.Context, k Key, create bool, a Audit) (Key, error) {
	plain := k.Plaintext
	e := s.mutate(ctx, a, func(tx pgx.Tx) error {
		q := "update downstream_api_keys set name=$2,key_prefix=$3,enabled=$4,allowed_models=$5,max_inflight=$6,key_sha256=$7 where id=$1 returning "
		if create {
			q = "insert into downstream_api_keys(id,name,key_prefix,enabled,allowed_models,max_inflight,key_sha256) values($1,$2,$3,$4,$5,$6,$7) returning "
		}
		var err error
		k, err = scanKey(tx.QueryRow(ctx, q+keyColumns, k.ID, k.Name, k.Prefix, k.Enabled, k.Allowed, k.MaxInflight, k.Hash))
		return err
	})
	k.Plaintext = plain
	return k, e
}
func (s *Store) Audits(ctx context.Context, c *Cursor, limit int) ([]Audit, error) {
	q := "select id::text,actor,action,target_type,target_id,payload,host(client_ip),created_at from audit_events"
	args := []any{}
	if c != nil {
		q += " where (created_at,id)<($1,$2::uuid)"
		args = append(args, c.Time, c.ID)
	}
	q += " order by created_at desc,id desc limit "
	if c != nil {
		q += "$3"
	} else {
		q += "$1"
	}
	args = append(args, limit)
	rows, e := s.Pool.Query(ctx, q, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Audit{}
	for rows.Next() {
		var a Audit
		if e = rows.Scan(&a.ID, &a.Actor, &a.Action, &a.TargetType, &a.TargetID, &a.Payload, &a.ClientIP, &a.Created); e != nil {
			return nil, e
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
