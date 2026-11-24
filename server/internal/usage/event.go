package usage

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	MaxBytes  = 4 << 20
	MaxEvents = 1000
)

type Event struct {
	EventID             string    `json:"event_id"`
	RequestID           string    `json:"request_id"`
	OccurredAt          time.Time `json:"occurred_at"`
	CoreVersion         string    `json:"core_version"`
	CoreInstance        string    `json:"core_instance"`
	DownstreamKeyID     *string   `json:"downstream_key_id"`
	UpstreamID          *string   `json:"upstream_id"`
	Endpoint            string    `json:"endpoint"`
	RequestedModel      *string   `json:"requested_model"`
	ResolvedModel       *string   `json:"resolved_model"`
	UpstreamModel       *string   `json:"upstream_model"`
	Stream              bool      `json:"stream"`
	HTTPStatus          *int      `json:"http_status"`
	StatusRewrittenFrom *int      `json:"status_rewritten_from"`
	Outcome             string    `json:"outcome"`
	ErrorClass          *string   `json:"error_class"`
	ErrorCode           *string   `json:"error_code"`
	ErrorMessage        *string   `json:"error_message"`
	AttemptCount        int       `json:"attempt_count"`
	InputTokens         *int64    `json:"input_tokens"`
	CachedInputTokens   *int64    `json:"cached_input_tokens"`
	OutputTokens        *int64    `json:"output_tokens"`
	ReasoningTokens     *int64    `json:"reasoning_tokens"`
	TotalTokens         *int64    `json:"total_tokens"`
	TTFT                *int      `json:"ttft_ms"`
	UpstreamTTFB        *int      `json:"upstream_ttfb_ms"`
	Duration            *int      `json:"duration_ms"`
	UpstreamDuration    *int      `json:"upstream_duration_ms"`
	RequestBytes        *int64    `json:"request_bytes"`
	ResponseBytes       *int64    `json:"response_bytes"`
}

type Batch struct {
	Instance string
	Events   []Event
}

type Problem struct {
	Status              int
	Path, Field, Reason string
}

func (p *Problem) Error() string { return p.Reason }

var ErrUnknownInstance = errors.New("core instance is not registered")

func EventProblem(index int, field, reason string) *Problem {
	return &Problem{422, fmt.Sprintf("events[%d].%s", index, field), field, reason}
}

func CanonicalUUID(s string, v7 bool) bool {
	u, err := uuid.Parse(s)
	return err == nil && s == u.String() && (!v7 || u.Version() == 7 && u.Variant() == uuid.RFC4122)
}

func utc(t time.Time) bool {
	_, offset := t.Zone()
	return !t.IsZero() && offset == 0 && t.Year() >= 1
}

// Decode ignores unknown fields as required by v1. Known fields use exact wire names.
func Decode(body []byte, messageID, batchID, instanceHeader string, now time.Time) (Batch, *Problem) {
	bad := func(reason string) (Batch, *Problem) { return Batch{}, &Problem{Status: 400, Reason: reason} }
	var envelope struct {
		Version   int       `json:"protocol_version"`
		MessageID string    `json:"message_id"`
		SentAt    time.Time `json:"sent_at"`
		Type      string    `json:"type"`
		Payload   struct {
			BatchID  string            `json:"batch_id"`
			Instance string            `json:"core_instance_id"`
			Events   []json.RawMessage `json:"events"`
		} `json:"payload"`
	}
	if !utf8.Valid(body) || json.Unmarshal(body, &envelope) != nil {
		return bad("invalid JSON or field type")
	}
	p := envelope.Payload
	if envelope.Version != 1 || envelope.Type != "usage_batch" ||
		!CanonicalUUID(envelope.MessageID, true) || messageID != envelope.MessageID ||
		!utc(envelope.SentAt) || envelope.SentAt.After(now.Add(5*time.Minute)) ||
		!CanonicalUUID(p.BatchID, false) || batchID != p.BatchID ||
		!CanonicalUUID(p.Instance, false) || instanceHeader != "" && instanceHeader != p.Instance ||
		len(p.Events) == 0 {
		return bad("invalid usage batch envelope or protocol headers")
	}
	if len(p.Events) > MaxEvents {
		return Batch{}, &Problem{Status: 413, Reason: "batch exceeds 1000 events"}
	}
	out := Batch{Instance: p.Instance, Events: make([]Event, len(p.Events))}
	seen := make(map[string]bool, len(p.Events))
	for i, raw := range p.Events {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
			return bad("usage event must be an object")
		}
		e := &out.Events[i]
		if err := json.Unmarshal(raw, e); err != nil {
			return bad("invalid usage event field type")
		}
		for _, name := range []string{"event_id", "request_id", "occurred_at", "core_version", "core_instance", "endpoint", "stream", "outcome", "attempt_count"} {
			if val := fields[name]; val == nil || string(val) == "null" {
				return Batch{}, EventProblem(i, name, "is required and cannot be null")
			}
		}
		field, reason := e.validate(p.Instance, now)
		if field != "" {
			return Batch{}, EventProblem(i, field, reason)
		}
		if seen[e.EventID] {
			return Batch{}, EventProblem(i, "event_id", "duplicate event ID in batch")
		}
		seen[e.EventID] = true
	}
	return out, nil
}

func (e Event) validate(instance string, now time.Time) (string, string) {
	if !CanonicalUUID(e.EventID, true) {
		return "event_id", "must be a canonical UUIDv7"
	}
	if e.CoreInstance != instance {
		return "core_instance", "must match the batch instance"
	}
	if !utc(e.OccurredAt) || e.OccurredAt.After(now.Add(5*time.Minute)) {
		return "occurred_at", "must be UTC and no more than five minutes in the future"
	}
	for _, v := range []struct {
		name  string
		value *string
	}{{"downstream_key_id", e.DownstreamKeyID}, {"upstream_id", e.UpstreamID}} {
		if v.value != nil && !CanonicalUUID(*v.value, false) {
			return v.name, "must be a canonical UUID"
		}
	}
	for _, v := range []struct {
		name     string
		value    *string
		min, max int
	}{
		{"request_id", &e.RequestID, 1, 128}, {"core_version", &e.CoreVersion, 1, 128}, {"endpoint", &e.Endpoint, 1, 256},
		{"requested_model", e.RequestedModel, 0, 256}, {"resolved_model", e.ResolvedModel, 1, 256}, {"upstream_model", e.UpstreamModel, 0, 256},
		{"error_code", e.ErrorCode, 0, 128}, {"error_message", e.ErrorMessage, 0, 1024},
	} {
		if v.value != nil {
			n := utf8.RuneCountInString(*v.value)
			if n < v.min || n > v.max || strings.ContainsRune(*v.value, 0) {
				return v.name, "invalid string length or NUL character"
			}
		}
	}
	switch e.Outcome {
	case "success", "upstream_error", "local_error", "client_disconnected", "aborted":
	default:
		return "outcome", "invalid outcome"
	}
	if e.AttemptCount < 0 || e.AttemptCount > 10 || e.AttemptCount == 0 && e.Outcome != "local_error" {
		return "attempt_count", "must be 1 to 10, or 0 for a local error"
	}
	if e.ErrorMessage != nil && *e.ErrorMessage != "" && e.Outcome == "success" {
		return "error_message", "success cannot carry an error message"
	}
	if e.ErrorClass != nil && *e.ErrorClass != "" && e.Outcome == "success" {
		return "error_class", "success cannot carry an error class"
	}
	if e.ErrorClass != nil {
		switch *e.ErrorClass {
		case "connect_error", "dns_error", "tls_error", "first_byte_timeout", "idle_timeout", "upstream_http_error", "upstream_stream_error", "local_validation_error", "client_disconnected", "aborted", "unknown":
		default:
			return "error_class", "invalid error class"
		}
	}
	if e.HTTPStatus != nil && (*e.HTTPStatus < 100 || *e.HTTPStatus > 599) {
		return "http_status", "must be between 100 and 599"
	}
	if from := e.StatusRewrittenFrom; from != nil {
		if *from < 100 || *from > 599 || e.HTTPStatus == nil || *from == *e.HTTPStatus {
			return "status_rewritten_from", "must be a valid upstream status different from http_status"
		}
		if e.ErrorClass != nil {
			switch *e.ErrorClass {
			case "connect_error", "dns_error", "tls_error", "first_byte_timeout", "idle_timeout":
				return "status_rewritten_from", "cannot rewrite a status without an upstream HTTP error"
			}
		}
	}
	for _, v := range []struct {
		name  string
		value *int
	}{
		{"ttft_ms", e.TTFT}, {"upstream_ttfb_ms", e.UpstreamTTFB}, {"duration_ms", e.Duration}, {"upstream_duration_ms", e.UpstreamDuration},
	} {
		if v.value != nil && (*v.value < 0 || *v.value > 86400000) {
			return v.name, "must be between 0 and 86400000"
		}
	}
	if !e.Stream && e.TTFT != nil {
		return "ttft_ms", "must be null for a non-streaming request"
	}
	for _, v := range []struct {
		name  string
		value *int64
	}{
		{"input_tokens", e.InputTokens}, {"cached_input_tokens", e.CachedInputTokens}, {"output_tokens", e.OutputTokens},
		{"reasoning_tokens", e.ReasoningTokens}, {"total_tokens", e.TotalTokens}, {"request_bytes", e.RequestBytes}, {"response_bytes", e.ResponseBytes},
	} {
		if v.value != nil && *v.value < 0 {
			return v.name, "must be non-negative"
		}
	}
	if e.CachedInputTokens != nil && e.InputTokens != nil && *e.CachedInputTokens > *e.InputTokens {
		return "cached_input_tokens", "cannot exceed input_tokens"
	}
	return "", ""
}

func (e *Event) NormalizeTotal() bool {
	if e.InputTokens == nil || e.OutputTokens == nil || e.TotalTokens == nil {
		return false
	}
	const max = int64(1<<63 - 1)
	if *e.InputTokens > max-*e.OutputTokens {
		e.TotalTokens = nil
		return true
	}
	total := *e.InputTokens + *e.OutputTokens
	if total == *e.TotalTokens {
		return false
	}
	e.TotalTokens = &total
	return true
}
