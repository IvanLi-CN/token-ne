package cores

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/IvanLi-CN/token-ne/server/internal/auth"
	"github.com/IvanLi-CN/token-ne/server/internal/httpapi"
	"github.com/jackc/pgx/v5"
)

type HTTP struct {
	Runtime    *Runtime
	Token      string
	PublicMeta Meta
}

func (h *HTTP) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /internal/v1/meta", h.meta)
	mux.HandleFunc("GET /internal/v1/config", h.pull)
	mux.HandleFunc("PUT /internal/v1/config", h.push)
	mux.HandleFunc("PUT /internal/v1/cores/{instance_id}/heartbeat", h.heartbeat)
	mux.HandleFunc("GET /internal/v1/status", h.status)
	mux.HandleFunc("GET /api/cores/instances/{instance_id}", httpapi.ProtectRoute(h.instance))
	mux.HandleFunc("GET /api/cores", httpapi.ProtectRoute(h.list))
	mux.HandleFunc("POST /api/cores/versions", httpapi.ProtectRoute(h.version))
	mux.HandleFunc("POST /api/cores/deploy", httpapi.ProtectRoute(h.deploy))
	mux.HandleFunc("POST /api/cores/deployments/{deployment_id}/shift", httpapi.ProtectRoute(h.shift))
	mux.HandleFunc("POST /api/cores/deployments/{deployment_id}/rollback", httpapi.ProtectRoute(h.rollback))
	mux.HandleFunc("POST /api/cores/instances/{instance_id}/drain", httpapi.ProtectRoute(h.drain))
	mux.HandleFunc("POST /api/cores/prune", httpapi.ProtectRoute(h.prune))
}

func Registrar(h *HTTP) func(*http.ServeMux) { return h.Register }

func (h *HTTP) internal(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || !ConstantToken(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), h.Token) {
		writeError(w, 401, "unauthorized", "control token required")
		return false
	}
	if r.Header.Get(ProtocolHeader) != "1" {
		writeError(w, 409, "incompatible_version", "protocol version must be 1")
		return false
	}
	if !UUID(r.Header.Get(RequestHeader), true) {
		writeError(w, 400, "malformed_request", "request ID must be a UUIDv7")
		return false
	}
	return true
}
func (h *HTTP) meta(w http.ResponseWriter, r *http.Request) {
	if !h.internal(w, r) {
		return
	}
	writeCoreJSON(w, 200, h.PublicMeta)
}
func (h *HTTP) pull(w http.ResponseWriter, r *http.Request) {
	if !h.internal(w, r) {
		return
	}
	id := r.URL.Query().Get("instance_id")
	if header := r.Header.Get(InstanceHeader); header != "" && header != id {
		writeError(w, 400, "malformed_request", "instance ID mismatch")
		return
	}
	if !UUID(id, false) {
		writeError(w, 400, "malformed_request", "canonical instance_id is required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	snapshot, unchanged, e := h.Runtime.Pull(ctx, id, r.Header.Get("If-None-Match"))
	if e != nil {
		mapInternal(w, e)
		return
	}
	if unchanged {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(snapshot.Revision, 10)))
	if snapshot.Signature != "" {
		w.Header().Set(SignatureHeader, snapshot.Signature)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(snapshot.Body)
}
func (h *HTTP) push(w http.ResponseWriter, r *http.Request) {
	if !h.internal(w, r) {
		return
	}
	id := r.Header.Get(InstanceHeader)
	if !UUID(id, false) {
		writeError(w, 400, "malformed_request", "canonical instance header required")
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		writeError(w, 400, "malformed_request", "Content-Type must be application/json")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxSnapshot)
	body, e := io.ReadAll(r.Body)
	if e != nil {
		writeError(w, 413, "payload_too_large", "configuration body exceeds 8 MiB")
		return
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || fields == nil {
		writeError(w, 400, "malformed_request", "configuration must be a JSON object")
		return
	}
	var instance string
	var revision int64
	var protocol, schema int
	if json.Unmarshal(fields["instance_id"], &instance) != nil || json.Unmarshal(fields["revision"], &revision) != nil || json.Unmarshal(fields["protocol_version"], &protocol) != nil || json.Unmarshal(fields["config_schema_version"], &schema) != nil {
		writeError(w, 400, "malformed_request", "required configuration fields are invalid")
		return
	}
	if instance != id {
		writeError(w, 400, "malformed_request", "instance ID mismatch")
		return
	}
	headerRevision, err := strconv.ParseInt(r.Header.Get(RevisionHeader), 10, 64)
	if err != nil || headerRevision != revision {
		writeError(w, 400, "malformed_request", "configuration revision mismatch")
		return
	}
	if protocol != Protocol || schema != Schema {
		writeError(w, 409, "incompatible_version", "configuration protocol or schema is incompatible")
		return
	}
	signature := r.Header.Get(SignatureHeader)
	if h.Runtime.SigningKey != "" {
		if signature == "" && h.Runtime.SignatureMode == "enforce" {
			writeError(w, 422, "validation_failed", "configuration signature is required")
			return
		}
		if signature != "" && (!strings.HasPrefix(signature, "sha256=") || !auth.VerifySignature([]byte(h.Runtime.SigningKey), string(body), strings.TrimPrefix(signature, "sha256="))) && h.Runtime.SignatureMode == "enforce" {
			writeError(w, 422, "validation_failed", "configuration signature verification failed")
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	current, e := h.Runtime.Config.Build(ctx, id, false)
	if e != nil {
		mapInternal(w, e)
		return
	}
	if current.Revision != revision || current.Hash != Hash(body) {
		writeCoreError(w, 409, "stale_revision", "configuration differs from current desired revision", r.Header.Get(RequestHeader))
		return
	}
	var address string
	if e = h.Runtime.database().QueryRow(ctx, `select address from core_instances where id=$1`, id).Scan(&address); e != nil {
		mapInternal(w, e)
		return
	}
	meta, metaErr := h.Runtime.Peer.Meta(ctx, address)
	if metaErr != nil {
		mapInternal(w, metaErr)
		return
	}
	if e = meta.Compatible(); e != nil {
		mapInternal(w, e)
		return
	}
	if e = h.Runtime.Peer.Push(ctx, address, id, current); e != nil {
		mapInternal(w, e)
		return
	}
	status, statusErr := h.Runtime.Peer.Status(ctx, address, id)
	if statusErr != nil || !current.Matches(status) {
		mapInternal(w, ErrConflict)
		return
	}
	if _, e = h.Runtime.database().Exec(ctx, `update core_instances set config_revision_applied=$2,last_controlled_at=$3 where id=$1 and config_revision_desired=$2 and config_hash_desired=$4`, id, current.Revision, status.ControlledAt, current.Hash); e != nil {
		mapInternal(w, e)
		return
	}
	ack := map[string]any{"protocol_version": Protocol, "instance_id": id, "applied_revision": revision, "config_schema_version": Schema, "signature_state": "off"}
	if h.Runtime.SigningKey != "" {
		switch {
		case signature == "":
			ack["signature_state"] = "missing"
		case auth.VerifySignature([]byte(h.Runtime.SigningKey), string(body), strings.TrimPrefix(signature, "sha256=")):
			ack["signature_state"] = "valid"
		default:
			ack["signature_state"] = "invalid"
		}
	}
	writeCoreJSON(w, 200, ack)
}
func (h *HTTP) heartbeat(w http.ResponseWriter, r *http.Request) {
	if !h.internal(w, r) {
		return
	}
	id := r.PathValue("instance_id")
	if !UUID(id, false) {
		writeError(w, 400, "malformed_request", "invalid instance UUID")
		return
	}
	if r.Header.Get(InstanceHeader) != "" && r.Header.Get(InstanceHeader) != id {
		writeError(w, 400, "malformed_request", "path and header instance ID mismatch")
		return
	}
	body, e := readJSON(w, r, 64<<10)
	if e != nil {
		writeError(w, 400, "malformed_request", "invalid heartbeat JSON")
		return
	}
	var envelope Heartbeat
	if json.Unmarshal(body, &envelope) != nil {
		writeError(w, 400, "malformed_request", "invalid heartbeat envelope")
		return
	}
	if envelope.Protocol != Protocol {
		writeError(w, 409, "incompatible_version", "heartbeat protocol must match v1")
		return
	}
	if envelope.Status.Instance != id {
		writeError(w, 400, "malformed_request", "path and body instance ID mismatch")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if e = h.Runtime.Heartbeat(ctx, id, envelope); e != nil {
		slog.Warn("core heartbeat failed", "instance", id, "error", e)
		mapInternal(w, e)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (h *HTTP) status(w http.ResponseWriter, r *http.Request) {
	if !h.internal(w, r) {
		return
	}
	id := r.URL.Query().Get("instance_id")
	if header := r.Header.Get(InstanceHeader); header != "" && header != id {
		writeError(w, 400, "malformed_request", "instance ID mismatch")
		return
	}
	if !UUID(id, false) {
		writeError(w, 400, "malformed_request", "instance_id is required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var address string
	e := h.Runtime.database().QueryRow(ctx, `select address from core_instances where id=$1`, id).Scan(&address)
	if errors.Is(e, pgx.ErrNoRows) {
		writeError(w, 404, "unknown_instance", "unknown core instance")
		return
	}
	if e != nil {
		mapInternal(w, e)
		return
	}
	status, e := h.Runtime.Peer.Status(ctx, address, id)
	if e != nil {
		writeError(w, 503, "storage_unavailable", "core status unavailable")
		return
	}
	writeCoreJSON(w, 200, status)
}
func (h *HTTP) list(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	rows, e := h.Runtime.database().Query(ctx, `select id::text,version,image_ref,image_digest,protocol_version,config_schema_min,config_schema_max,build_commit,created_at from core_versions order by created_at,version`)
	if e != nil {
		writeAdminError(w, 503, "storage_unavailable", "database unavailable")
		return
	}
	versions := []map[string]any{}
	for rows.Next() {
		var id, version, image string
		var digest, commit *string
		var protocol, min, max int
		var created time.Time
		if e = rows.Scan(&id, &version, &image, &digest, &protocol, &min, &max, &commit, &created); e != nil {
			rows.Close()
			writeAdminError(w, 503, "storage_unavailable", "database unavailable")
			return
		}
		versions = append(versions, map[string]any{"id": id, "version": version, "image_ref": image, "image_digest": digest, "protocol_version": protocol, "config_schema_min": min, "config_schema_max": max, "build_commit": commit, "created_at": created})
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		writeAdminError(w, 503, "storage_unavailable", "database unavailable")
		return
	}
	instances, e := h.Runtime.instances(ctx)
	if e != nil {
		writeAdminError(w, 503, "storage_unavailable", "database unavailable")
		return
	}
	deployments, e := deploymentList(ctx, h.Runtime)
	if e != nil {
		writeAdminError(w, 503, "storage_unavailable", "database unavailable")
		return
	}
	writeAdminJSON(w, 200, map[string]any{"versions": versions, "instances": instances, "deployments": deployments})
}
func (h *HTTP) version(w http.ResponseWriter, r *http.Request) {
	var input Target
	if !decodeAdmin(w, r, &input) {
		return
	}
	actor, _ := httpapi.AdminActor(r)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	result, e := h.Runtime.RegisterVersion(ctx, input)
	if e != nil {
		mapAdmin(w, e)
		return
	}
	if e = h.Runtime.Audit(ctx, actor, "core.version.register", "core_version", result["id"].(string), map[string]any{"version": input.Version}); e != nil {
		mapAdmin(w, e)
		return
	}
	writeAdminJSON(w, 201, result)
}
func (h *HTTP) deploy(w http.ResponseWriter, r *http.Request) {
	var input DeploymentRequest
	if !decodeAdmin(w, r, &input) {
		return
	}
	actor, _ := httpapi.AdminActor(r)
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	result, e := h.Runtime.Deploy(ctx, actor, input)
	if e != nil {
		mapAdmin(w, e)
		return
	}
	writeAdminJSON(w, 202, result)
}
func (h *HTTP) shift(w http.ResponseWriter, r *http.Request) {
	var input ShiftRequest
	if !decodeAdmin(w, r, &input) {
		return
	}
	actor, _ := httpapi.AdminActor(r)
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	id := r.PathValue("deployment_id")
	if e := h.Runtime.Shift(ctx, id, input.Weights); e != nil {
		slog.Warn("core shift failed", "deployment", id, "weights", input.Weights, "error", e)
		mapAdmin(w, e)
		return
	}
	if e := h.Runtime.Audit(ctx, actor, "core.deployment.shift", "core_deployment", id, input.Weights); e != nil {
		mapAdmin(w, e)
		return
	}
	result, e := getDeployment(ctx, h.Runtime, id)
	if e != nil {
		mapAdmin(w, e)
		return
	}
	writeAdminJSON(w, 202, result)
}
func (h *HTTP) rollback(w http.ResponseWriter, r *http.Request) {
	actor, _ := httpapi.AdminActor(r)
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	id := r.PathValue("deployment_id")
	if e := h.Runtime.Rollback(ctx, id, actor); e != nil {
		mapAdmin(w, e)
		return
	}
	result, e := getDeployment(ctx, h.Runtime, id)
	if e != nil {
		mapAdmin(w, e)
		return
	}
	writeAdminJSON(w, 202, result)
}
func (h *HTTP) drain(w http.ResponseWriter, r *http.Request) {
	var input DrainRequest
	if !decodeAdmin(w, r, &input) {
		return
	}
	actor, _ := httpapi.AdminActor(r)
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	id := r.PathValue("instance_id")
	if e := h.Runtime.Drain(ctx, id, actor, input.Force, input.Reason); e != nil {
		mapAdmin(w, e)
		return
	}
	writeAdminJSON(w, 202, map[string]string{"status": "accepted"})
}
func (h *HTTP) prune(w http.ResponseWriter, r *http.Request) {
	var input PruneRequest
	if !decodeAdmin(w, r, &input) {
		return
	}
	actor, _ := httpapi.AdminActor(r)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	if e := h.Runtime.Prune(ctx, actor, input.Instances); e != nil {
		mapAdmin(w, e)
		return
	}
	writeAdminJSON(w, 202, map[string]any{"pruned": input.Instances})
}

func readJSON(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	media, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || media != "application/json" {
		return nil, errors.New("content type")
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	return io.ReadAll(r.Body)
}
func decodeAdmin(w http.ResponseWriter, r *http.Request, dst any) bool {
	body, e := readJSON(w, r, 1<<20)
	if e != nil {
		writeAdminError(w, 400, "malformed_request", "invalid JSON body")
		return false
	}
	if json.Unmarshal(body, dst) != nil {
		writeAdminError(w, 400, "malformed_request", "invalid JSON fields")
		return false
	}
	return true
}
func writeCoreJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeCoreJSON(w, status, map[string]any{"protocol_version": Protocol, "error": map[string]any{"code": code, "message": message, "retryable": status >= 500, "details": []any{}}})
}
func writeCoreError(w http.ResponseWriter, status int, code, message, id string) {
	writeCoreJSON(w, status, map[string]any{"protocol_version": Protocol, "error": map[string]any{"code": code, "message": message, "request_id": id, "retryable": false, "details": []any{}}})
}
func writeAdminJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeAdminError(w http.ResponseWriter, status int, code, message string) {
	writeAdminJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": message, "details": []any{}}})
}
func mapInternal(w http.ResponseWriter, e error) {
	switch {
	case errors.Is(e, ErrUnknown):
		writeError(w, 404, "unknown_instance", "unknown core instance")
	case errors.Is(e, ErrCompatibility):
		writeError(w, 409, "incompatible_version", "core or configuration schema is incompatible")
	case errors.Is(e, ErrFeature):
		writeError(w, 409, "unsupported_feature", "core lacks a required feature")
	case errors.Is(e, ErrOverride):
		writeError(w, 409, "override_active", "manual configuration override is active")
	case errors.Is(e, ErrConflict):
		writeError(w, 409, "stale_revision", "configuration not confirmed by core")
	case errors.Is(e, ErrStaleRevision):
		writeError(w, 409, "stale_revision", "stale configuration revision")
	case errors.Is(e, ErrInvalid):
		writeError(w, 422, "validation_failed", "invalid control-plane input")
	default:
		writeError(w, 503, "storage_unavailable", "control-plane storage unavailable")
	}
}
func mapAdmin(w http.ResponseWriter, e error) {
	switch {
	case errors.Is(e, ErrUnknown):
		writeAdminError(w, 404, "not_found", "core resource not found")
	case errors.Is(e, ErrConflict) || errors.Is(e, ErrOverride) || errors.Is(e, ErrStaleRevision):
		writeAdminError(w, 409, "conflict", e.Error())
	case errors.Is(e, ErrInvalid) || errors.Is(e, ErrCompatibility) || errors.Is(e, ErrFeature):
		writeAdminError(w, 422, "validation_failed", e.Error())
	default:
		writeAdminError(w, 503, "storage_unavailable", "core control operation unavailable")
	}
}

func (h *HTTP) instance(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("instance_id")
	if !UUID(id, false) {
		mapAdmin(w, ErrInvalid)
		return
	}
	row, e := scanInstance(h.Runtime.database().QueryRow(r.Context(), instanceSelect+` where i.id=$1`, id))
	if errors.Is(e, pgx.ErrNoRows) {
		e = ErrUnknown
	}
	if e != nil {
		mapAdmin(w, e)
		return
	}
	writeAdminJSON(w, 200, row)
}
