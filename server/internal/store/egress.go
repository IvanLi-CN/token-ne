package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/IvanLi-CN/token-ne/server/internal/auth"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// egressAllocationLock serialises port allocation. Without it two concurrent
// creates could both pick the same free port and one would lose to the unique
// index after doing all its validation.
const egressAllocationLock = 4927816044

// ErrEgressPortExhausted reports that every port in the configured range is
// held by a live profile. The API answers it with 409.
var ErrEgressPortExhausted = errors.New("egress proxy port range is exhausted")

const egressColumns = "id::text,name,protocol,server,port,transport::text,tls::text,enabled,runtime_state,swap_phase,inbound_port,last_error,created_at,updated_at,credentials_ciphertext,credentials_nonce"

// egressEffective is the computed routing eligibility: the admin enabled the
// profile and its runtime state still has a live routed outbound. During a swap
// the route always points at an outbound that exists, so swapping stays
// eligible; pending, degraded and disabled do not.
func egressEffective(enabled bool, runtimeState string) bool {
	return enabled && (runtimeState == "ready" || runtimeState == "swapping")
}

// jsonObject turns a stored jsonb text into an object; NULL or an unexpected
// empty value collapses to {} so responses always carry an object.
func jsonObject(text string) json.RawMessage {
	if text == "" || text == "null" {
		return json.RawMessage("{}")
	}
	return json.RawMessage(text)
}

func scanEgressProxy(row pgx.Row) (EgressProxy, error) {
	var p EgressProxy
	var transport, tlsOptions string
	e := row.Scan(&p.ID, &p.Name, &p.Protocol, &p.Server, &p.Port, &transport, &tlsOptions,
		&p.Enabled, &p.RuntimeState, &p.SwapPhase, &p.InboundPort, &p.LastError, &p.Created, &p.Updated,
		&p.Ciphertext, &p.Nonce)
	if e == nil {
		p.Transport = jsonObject(transport)
		p.TLS = jsonObject(tlsOptions)
		p.HasCredentials = len(p.Ciphertext) > 0
		p.Effective = egressEffective(p.Enabled, p.RuntimeState)
	}
	return p, normalize(e)
}

func (s *Store) EgressProxies(ctx context.Context) ([]EgressProxy, error) {
	rows, e := s.Pool.Query(ctx, "select "+egressColumns+" from egress_proxies order by created_at,id")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []EgressProxy{}
	for rows.Next() {
		p, e := scanEgressProxy(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) EgressProxy(ctx context.Context, id string) (EgressProxy, error) {
	return scanEgressProxy(s.Pool.QueryRow(ctx, "select "+egressColumns+" from egress_proxies where id=$1", id))
}

// SaveEgressProxy creates or updates one profile. Creation allocates a stable
// inbound port from ports inside the same transaction as the insert; an update
// keeps the port the row already owns, so enable/disable never reallocates.
// Runtime state, swap phase and last error belong to the reconciler and are
// never written here.
func (s *Store) SaveEgressProxy(ctx context.Context, p EgressProxy, create bool, ports PortRange, a Audit) (EgressProxy, error) {
	if create && p.ID == "" {
		p.ID = uuid.NewString()
	}
	transport, tlsOptions := jsonObject(string(p.Transport)), jsonObject(string(p.TLS))
	e := s.mutate(ctx, a, func(tx pgx.Tx) error {
		var nameTaken bool
		if err := tx.QueryRow(ctx, "select exists(select 1 from egress_proxies where name=$2 and id<>$1)", p.ID, p.Name).Scan(&nameTaken); err != nil {
			return err
		}
		if nameTaken {
			return &FieldError{Path: "name", Reason: "an egress proxy with this name already exists", Kind: ErrConflict}
		}
		var err error
		if create {
			if _, err = tx.Exec(ctx, "select pg_advisory_xact_lock($1)", egressAllocationLock); err != nil {
				return err
			}
			// g.port is qualified: an unqualified `port` inside the subquery
			// would bind to egress_proxies.port and re-pick an allocated port.
			if err = tx.QueryRow(ctx, `
select g.port from generate_series($1::int,$2::int) as g(port)
where not exists (select 1 from egress_proxies p where p.inbound_port=g.port)
order by g.port limit 1`, ports.Min, ports.Max).Scan(&p.InboundPort); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return ErrEgressPortExhausted
				}
				return err
			}
			p, err = scanEgressProxy(tx.QueryRow(ctx, `
insert into egress_proxies(id,name,protocol,server,port,transport,tls,credentials_ciphertext,credentials_nonce,enabled,inbound_port)
values($1,$2,$3,$4,$5,$6::jsonb,$7::jsonb,$8,$9,$10,$11) returning `+egressColumns,
				p.ID, p.Name, p.Protocol, p.Server, p.Port, string(transport), string(tlsOptions), p.Ciphertext, p.Nonce, p.Enabled, p.InboundPort))
			return err
		}
		p, err = scanEgressProxy(tx.QueryRow(ctx, `
update egress_proxies set name=$2,protocol=$3,server=$4,port=$5,transport=$6::jsonb,tls=$7::jsonb,
  credentials_ciphertext=$8,credentials_nonce=$9,enabled=$10,updated_at=now()
where id=$1 returning `+egressColumns,
			p.ID, p.Name, p.Protocol, p.Server, p.Port, string(transport), string(tlsOptions), p.Ciphertext, p.Nonce, p.Enabled))
		return err
	})
	return p, e
}

// DeleteEgressProxy refuses to delete a profile that an upstream still binds.
// The foreign key already RESTRICTs the row; reporting the binding here keeps
// the answer a 409 with a field path instead of a bare constraint violation.
// Historical usage keeps its egress_proxy_id: usage_events has no foreign key.
func (s *Store) DeleteEgressProxy(ctx context.Context, id string, a Audit) error {
	return s.mutate(ctx, a, func(tx pgx.Tx) error {
		rows, e := tx.Query(ctx, "select id::text from upstreams where egress_proxy_id=$1 order by id", id)
		if e != nil {
			return e
		}
		refs := []string{}
		for rows.Next() {
			var ref string
			if e = rows.Scan(&ref); e != nil {
				rows.Close()
				return e
			}
			refs = append(refs, ref)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		if len(refs) > 0 {
			return &FieldError{Path: "egress_proxy_id", Reason: "upstream " + refs[0] + " still binds this profile", Kind: ErrConflict}
		}
		tag, e := tx.Exec(ctx, "delete from egress_proxies where id=$1", id)
		if e == nil && tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		// A binding can land between the check and the delete; keep it a conflict.
		if errors.Is(normalize(e), ErrReference) {
			return &FieldError{Path: "egress_proxy_id", Reason: "an upstream still binds this profile", Kind: ErrConflict}
		}
		return e
	})
}

// SetEgressProxyState writes the reconciler's projection for one profile.
func (s *Store) SetEgressProxyState(ctx context.Context, id string, state EgressProxyState, a Audit) error {
	return s.mutate(ctx, a, func(tx pgx.Tx) error {
		tag, e := tx.Exec(ctx, `
update egress_proxies set runtime_state=$2,swap_phase=$3,last_error=$4,updated_at=now()
where id=$1`, id, state.RuntimeState, state.SwapPhase, state.LastError)
		if e == nil && tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return e
	})
}

// EgressProxyCredential decrypts the stored credentials of one profile for the
// internal test path. It is the only reader of the ciphertext; nothing in this
// package logs or serialises the result.
func (s *Store) EgressProxyCredential(ctx context.Context, id string, master []byte) (json.RawMessage, error) {
	var ciphertext, nonce []byte
	if e := s.Pool.QueryRow(ctx, "select credentials_ciphertext,credentials_nonce from egress_proxies where id=$1", id).Scan(&ciphertext, &nonce); e != nil {
		return nil, normalize(e)
	}
	if len(ciphertext) == 0 {
		return nil, ErrNotFound
	}
	plain, e := auth.Decrypt(master, ciphertext, nonce)
	if e != nil {
		return nil, e
	}
	return json.RawMessage(plain), nil
}
