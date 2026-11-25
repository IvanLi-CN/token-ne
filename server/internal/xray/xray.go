// Package xray owns the control-plane side of the single Xray egress converter:
// it renders one proxy profile into an Xray SOCKS5 inbound plus a protocol
// outbound, and reconciles that desired state through the Xray gRPC API
// (HandlerService, RoutingService).
//
// The contract it implements is FR-25 in docs/prd.md and "Xray 出站代理对账" in
// docs/architecture.md:
//
//   - The database is the desired state. Xray runtime is rebuilt from it, never
//     the other way around.
//   - Tags are fixed: egress:<profile_id>:in, egress:<profile_id>:out, the swap
//     tag egress:<profile_id>:out:staged and the routing rule tag
//     egress:<profile_id>:route.
//   - Routing rules are published as a complete replacement (a single AddRule
//     call with shouldAppend=false), matched by inbound tag.
//   - A profile whose server, credentials, transport or TLS options change is
//     swapped through the staged tag so the route always points at a live
//     outbound.
//   - The package never logs or embeds proxy credentials in errors, never
//     restarts Xray, never falls back to direct egress and never starts a
//     goroutine: the caller owns scheduling.
//
// The package is deliberately free of database and HTTP concerns. The API layer
// validates a profile with Validate before persisting it, and calls Reconcile on
// control-plane start, after an Xray restart and on its timer.
package xray

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var errNoAddress = errors.New("xray api address is empty")

// TagPrefix marks every Xray object this package owns. Anything else in Xray
// belongs to the bootstrap configuration and is left alone.
const TagPrefix = "egress:"

const (
	tagInboundSuffix  = ":in"
	tagOutboundSuffix = ":out"
	tagStagedSuffix   = ":out:staged"
	tagRuleSuffix     = ":route"
)

// InboundTag is the stable SOCKS5 inbound tag for one profile.
func InboundTag(profileID string) string { return TagPrefix + profileID + tagInboundSuffix }

// OutboundTag is the stable protocol outbound tag for one profile.
func OutboundTag(profileID string) string { return TagPrefix + profileID + tagOutboundSuffix }

// StagedOutboundTag is the temporary outbound tag used while a profile swaps.
func StagedOutboundTag(profileID string) string { return TagPrefix + profileID + tagStagedSuffix }

// RuleTag is the routing rule tag for one profile.
func RuleTag(profileID string) string { return TagPrefix + profileID + tagRuleSuffix }

// ProfileIDFromTag extracts the profile id from a tag this package owns.
func ProfileIDFromTag(tag string) (string, bool) {
	if !strings.HasPrefix(tag, TagPrefix) {
		return "", false
	}
	rest := strings.TrimPrefix(tag, TagPrefix)
	i := strings.Index(rest, ":")
	if i <= 0 {
		return "", false
	}
	return rest[:i], true
}

// Defaults fixed by docs/architecture.md. They are duplicated here as exported
// constants so the configuration layer does not have to re-state them; the
// DEFAULTS ARE THE CONTRACT, the env names live in internal/config.
const (
	// DefaultAPIAddress is XRAY_API_URL.
	DefaultAPIAddress = "10.42.0.10:10085"
	// DefaultDialTimeout bounds how long Dial waits for a ready connection.
	DefaultDialTimeout = 5 * time.Second
	// DefaultCallTimeout bounds one gRPC call so a hung Xray cannot stall the
	// caller's reconcile tick.
	DefaultCallTimeout = 10 * time.Second
	// DefaultReconcileInterval is XRAY_RECONCILE_INTERVAL_SECONDS. The package
	// does not schedule anything; this is for the caller's ticker.
	DefaultReconcileInterval = 30 * time.Second
)

// Status is the reconciliation result for one profile.
type Status string

const (
	// StatusReady means the profile's desired state is applied and verified.
	StatusReady Status = "ready"
	// StatusUnchanged means the desired state was already in place and
	// verified; nothing was written.
	StatusUnchanged Status = "unchanged"
	// StatusSwapping means a swap step failed but the inbound still routes to a
	// live outbound. The caller keeps the profile eligible and resumes from the
	// reported phase on the next reconcile.
	StatusSwapping Status = "swapping"
	// StatusDegraded means the profile is not usable; bound upstreams must lose
	// their candidates.
	StatusDegraded Status = "degraded"
	// StatusDisabled means the administrator disabled the profile and its Xray
	// objects were removed.
	StatusDisabled Status = "disabled"
)

// RuntimeState maps a result onto the egress_proxies.runtime_state enum.
func (r Result) RuntimeState() string {
	switch r.Status {
	case StatusReady, StatusUnchanged:
		return "ready"
	case StatusSwapping:
		return "swapping"
	case StatusDisabled:
		return "disabled"
	default:
		return "degraded"
	}
}

// ValidationError reports a caller-supplied profile field that is outside the
// enumerated option set. Path is a request field path (for example
// "transport.network" or "tls.mode") so the API layer can answer 422 with
// details[].path without guessing a downgrade.
type ValidationError struct {
	Path   string
	Reason string
}

func (e *ValidationError) Error() string {
	if e.Path == "" {
		return e.Reason
	}
	return e.Path + ": " + e.Reason
}

func invalid(path, reason string) error {
	return &ValidationError{Path: path, Reason: reason}
}

// UnreachableError means the Xray API could not be contacted. It is distinct
// from a rejected profile so the caller can alert on "Xray is down" instead of
// blaming a profile.
type UnreachableError struct {
	Err error
}

func (e *UnreachableError) Error() string {
	if e.Err == nil {
		return "xray api unreachable"
	}
	return "xray api unreachable: " + e.Err.Error()
}

func (e *UnreachableError) Unwrap() error { return e.Err }

// RejectedError means Xray refused an object this package generated, or the
// package could not generate one, for a specific profile. Step names the swap
// phase or operation that failed.
type RejectedError struct {
	ProfileID string
	Step      string
	Reason    string
}

func (e *RejectedError) Error() string {
	switch {
	case e.ProfileID != "" && e.Step != "":
		return fmt.Sprintf("xray rejected profile %s at %s: %s", e.ProfileID, e.Step, e.Reason)
	case e.ProfileID != "":
		return fmt.Sprintf("xray rejected profile %s: %s", e.ProfileID, e.Reason)
	default:
		return "xray rejected config: " + e.Reason
	}
}

// IsValidationError reports whether err is a caller validation failure.
func IsValidationError(err error) bool {
	var target *ValidationError
	return errors.As(err, &target)
}

// IsUnreachable reports whether err means the Xray API was unreachable.
func IsUnreachable(err error) bool {
	var target *UnreachableError
	return errors.As(err, &target)
}
