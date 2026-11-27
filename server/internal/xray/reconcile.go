package xray

import (
	"context"
	"errors"
	"sort"

	"github.com/xtls/xray-core/app/router"
)

// Swap phases, in order, as persisted in egress_proxies.swap_phase. A value
// records the last completed step; the next pass resumes at the following one.
const (
	PhaseStagedAdded = "staged_added"
	PhaseRouteStaged = "route_staged"
	PhaseOldRemoved  = "old_removed"
	PhaseStableAdded = "stable_added"
	PhaseRouteStable = "route_stable"
)

var phaseOrder = map[string]int{
	"":               0,
	PhaseStagedAdded: 1,
	PhaseRouteStaged: 2,
	PhaseOldRemoved:  3,
	PhaseStableAdded: 4,
	PhaseRouteStable: 5,
}

// SwapPhases lists the phases in order, for the layer that validates the
// egress_proxies.swap_phase column.
func SwapPhases() []string {
	return []string{PhaseStagedAdded, PhaseRouteStaged, PhaseOldRemoved, PhaseStableAdded, PhaseRouteStable}
}

// Target is one profile's desired state plus the effective-enabled value the
// caller last published. The caller supplies the latter because only the
// database layer knows the published revision.
type Target struct {
	Desired                    Desired
	PreviouslyEffectiveEnabled bool
}

// Result is the reconciliation outcome for one profile.
type Result struct {
	ProfileID string
	Status    Status
	// Phase is the swap phase to persist; "" when no swap is in flight.
	Phase string
	// Error is the non-credential reason for a degraded or swapping result.
	Error string
	// Written reports whether this pass changed Xray state.
	Written bool
	// EffectiveEnabled is the routing eligibility after this pass.
	EffectiveEnabled bool
	// EffectiveEnabledChanged reports whether that value flipped. The caller
	// turns that into a published config revision; a swap keeps it true and so
	// never flips it.
	EffectiveEnabledChanged bool
}

// Summary aggregates one reconcile pass.
type Summary struct {
	Results []Result
	// Unreachable is set when the Xray API could not be contacted at all: how
	// the caller tells "Xray is down" apart from "a profile was rejected".
	Unreachable bool
	// Error carries the unreachable reason; per-profile rejections live in
	// Results.
	Error string
}

// Degraded reports whether any profile ended unusable, so bound upstreams must
// lose their candidates.
func (s Summary) Degraded() bool {
	for _, r := range s.Results {
		if r.Status == StatusDegraded {
			return true
		}
	}
	return false
}

// Engine reconciles a set of profiles against one Xray instance. It is
// stateless apart from the client and starts no goroutine: the caller owns the
// timer, the database and the revision bookkeeping.
//
// Routing rules are global in Xray, so every entry point takes the complete
// target set and publishes the rule set as one replacement. Passing a subset
// would drop the other profiles' routes.
type Engine struct {
	client *Client
}

// NewEngine wraps a client.
func NewEngine(client *Client) *Engine { return &Engine{client: client} }

// ReconcileAll brings every profile in targets in line with Xray, reaps orphaned
// egress: objects and publishes the complete routing rule set.
//
// It is the entry point for control-plane start, an Xray restart, the periodic
// timer and any single-profile change: a full pass is cheap and keeps the rule
// set exact.
func (e *Engine) ReconcileAll(ctx context.Context, targets []Target) Summary {
	return e.reconcile(ctx, targets, "")
}

// ReconcileOne applies one profile's object changes and still publishes and
// verifies the complete rule set. It exists for the API path that changed a
// single profile and does not want to pay a full object pass; the desired state
// of the other profiles is still required.
func (e *Engine) ReconcileOne(ctx context.Context, targets []Target, profileID string) Summary {
	return e.reconcile(ctx, targets, profileID)
}

// StartupReconcile is the helper for control-plane start, Xray restart and the
// periodic timer. It is ReconcileAll; the distinct name documents the call
// sites and leaves room for startup-only reporting.
func (e *Engine) StartupReconcile(ctx context.Context, targets []Target) Summary {
	return e.ReconcileAll(ctx, targets)
}

func (e *Engine) reconcile(ctx context.Context, targets []Target, only string) Summary {
	c := e.client
	ordered := append([]Target{}, targets...)
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].Desired.Profile.ID < ordered[j].Desired.Profile.ID
	})

	obs, err := c.observe(ctx)
	if err != nil {
		return Summary{Unreachable: IsUnreachable(err), Error: err.Error()}
	}

	// routeTarget is the authoritative routing intent for this pass; it is what
	// every publication renders, and per-profile steps update it before
	// publishing.
	routeTarget := make(map[string]string, len(ordered))
	for _, target := range ordered {
		if target.Desired.Profile.Enabled {
			routeTarget[target.Desired.Profile.ID] = OutboundTag(target.Desired.Profile.ID)
		}
	}

	if err := e.reapOrphans(ctx, ordered, obs); err != nil {
		if IsUnreachable(err) {
			return Summary{Unreachable: true, Error: err.Error()}
		}
		// Clean-up failures are reported per profile by the pass below, which
		// re-renders and re-applies the affected profile.
	}

	summary := Summary{Results: make([]Result, 0, len(ordered))}
	for _, target := range ordered {
		var res Result
		if only != "" && target.Desired.Profile.ID != only {
			// Out of scope for this pass, but its route must still be published
			// and verified: the rule set is global.
			res = Result{ProfileID: target.Desired.Profile.ID, Status: StatusUnchanged, EffectiveEnabled: target.Desired.Profile.Enabled}
		} else {
			res, err = e.reconcileOne(ctx, target, routeTarget)
			if err != nil {
				if IsUnreachable(err) {
					return Summary{Unreachable: true, Error: err.Error()}
				}
				res = Result{ProfileID: target.Desired.Profile.ID, Status: StatusDegraded, Error: err.Error()}
			}
		}
		summary.Results = append(summary.Results, res)
	}

	// Verification covers only the profiles this pass reported usable. A profile
	// that failed already has its own result; verifying it again here would let one
	// broken profile mark every healthy one degraded, because failUsable downgrades
	// the whole summary.
	usable := make(map[string]string, len(routeTarget))
	for _, res := range summary.Results {
		if res.Status != StatusReady && res.Status != StatusUnchanged {
			continue
		}
		if target, ok := routeTarget[res.ProfileID]; ok {
			usable[res.ProfileID] = target
		}
	}
	if err := c.verifyRoutes(ctx, sortedKeys(usable), usable); err != nil {
		if IsUnreachable(err) {
			return Summary{Unreachable: true, Error: err.Error()}
		}
		summary.Results = failUsable(summary.Results, err)
	}
	return summary
}

// reconcileOne applies a single profile and returns its result. It updates
// routeTarget and publishes the complete rule set whenever the route has to
// move.
func (e *Engine) reconcileOne(ctx context.Context, target Target, routeTarget map[string]string) (Result, error) {
	c := e.client
	d := target.Desired
	p := d.Profile

	obs, err := c.observe(ctx)
	if err != nil {
		return Result{ProfileID: p.ID}, err
	}

	if !p.Enabled {
		return e.disable(ctx, target, routeTarget, obs)
	}

	// Render before touching Xray: a rejected profile must not be half-applied.
	objects, renderErr := Render(d)
	if renderErr != nil {
		res := Result{ProfileID: p.ID, Status: StatusDegraded, Error: renderErr.Error()}
		res.EffectiveEnabledChanged = target.PreviouslyEffectiveEnabled
		return res, nil
	}
	if p.RuntimeState == "swapping" {
		return e.swap(ctx, target, routeTarget, objects, obs)
	}
	return e.ensureStable(ctx, target, routeTarget, objects, obs)
}

// ensureStable applies a profile with no swap in flight: add the inbound and
// the stable outbound if missing, drop a stale :staged outbound, then make sure
// the route points at the stable tag and verifies.
func (e *Engine) ensureStable(ctx context.Context, target Target, routeTarget map[string]string, objects Objects, obs observed) (Result, error) {
	c := e.client
	p := target.Desired.Profile
	res := Result{ProfileID: p.ID}

	if !obs.inbounds[InboundTag(p.ID)] {
		if err := c.withCall(ctx, func(call context.Context) error { return c.addInbound(call, objects.Inbound) }); err != nil {
			return e.degrade(target, err), nil
		}
		res.Written = true
	}
	if !obs.outbounds[OutboundTag(p.ID)] {
		if err := c.withCall(ctx, func(call context.Context) error { return c.addOutbound(call, objects.Outbound) }); err != nil {
			return e.degrade(target, err), nil
		}
		res.Written = true
	}
	// A :staged outbound is only desired while a swap is in flight.
	if obs.outbounds[StagedOutboundTag(p.ID)] {
		if err := c.withCall(ctx, func(call context.Context) error { return c.removeOutbound(call, StagedOutboundTag(p.ID)) }); err != nil {
			return e.degrade(target, err), nil
		}
		res.Written = true
	}
	if obs.rules[RuleTag(p.ID)] != OutboundTag(p.ID) || res.Written {
		if err := e.publishRoute(ctx, p.ID, OutboundTag(p.ID), routeTarget); err != nil {
			return e.degrade(target, err), nil
		}
		res.Written = true
	}
	if err := c.verifyRoute(ctx, p.ID, OutboundTag(p.ID)); err != nil {
		return e.degrade(target, err), nil
	}
	res.Status = StatusUnchanged
	if res.Written {
		res.Status = StatusReady
	}
	res.EffectiveEnabled = true
	res.EffectiveEnabledChanged = !target.PreviouslyEffectiveEnabled
	return res, nil
}

// swap resumes the staged sequence. The order is fixed and every step is
// idempotent, so a crash between a write and the database update merely re-runs
// the recorded step:
//
//	staged_added -> route_staged -> old_removed -> stable_added -> route_stable
//
// The route moves to :staged before the old stable outbound is deleted and back
// to the stable tag only after the new outbound exists, so it always points at
// a live outbound: that is why a swapping profile keeps its eligibility.
func (e *Engine) swap(ctx context.Context, target Target, routeTarget map[string]string, objects Objects, obs observed) (Result, error) {
	c := e.client
	p := target.Desired.Profile
	id := p.ID
	stable := OutboundTag(id)
	staged := StagedOutboundTag(id)

	// A route that already points at a nonexistent outbound is a real outage,
	// not a resumable swap.
	if current, ok := obs.rules[RuleTag(id)]; ok && current != "" && !obs.outbounds[current] {
		return e.degrade(target, errors.New("routing rule points at a missing outbound "+current)), nil
	}
	if p.SwapPhase != "" && phaseOrder[p.SwapPhase] == 0 && p.SwapPhase != PhaseStagedAdded {
		return e.degrade(target, errors.New("unknown swap phase")), nil
	}

	stagedOut, renderErr := RenderStagedOutbound(target.Desired)
	if renderErr != nil {
		return e.degrade(target, renderErr), nil
	}

	phase := p.SwapPhase
	fail := func(completed string, stepErr error) (Result, error) {
		// Keep the routing intent aligned with where the sequence stopped: a
		// failed step after route_staged leaves the route on :staged, which is a
		// live outbound.
		if phaseOrder[completed] >= phaseOrder[PhaseRouteStaged] {
			routeTarget[id] = staged
		}
		return Result{
			ProfileID:        id,
			Status:           StatusSwapping,
			Phase:            completed,
			Error:            stepErr.Error(),
			Written:          true,
			EffectiveEnabled: true,
		}, nil
	}

	// The swap only rewires an existing endpoint.
	if !obs.inbounds[InboundTag(id)] {
		if err := c.withCall(ctx, func(call context.Context) error { return c.addInbound(call, objects.Inbound) }); err != nil {
			return fail(phase, err)
		}
	}

	// staged_added: add the :staged outbound.
	if phaseOrder[phase] < phaseOrder[PhaseStagedAdded] && !obs.outbounds[staged] {
		if err := c.withCall(ctx, func(call context.Context) error { return c.addOutbound(call, stagedOut) }); err != nil {
			return fail(phase, err)
		}
	}
	// route_staged: move the route to :staged and verify it.
	if phaseOrder[phase] < phaseOrder[PhaseRouteStaged] {
		if err := e.publishRoute(ctx, id, staged, routeTarget); err != nil {
			return fail(PhaseStagedAdded, err)
		}
		if err := c.verifyRoute(ctx, id, staged); err != nil {
			return fail(PhaseStagedAdded, err)
		}
		phase = PhaseRouteStaged
		routeTarget[id] = staged
	}
	// old_removed: the route is off the old stable outbound, so it may go.
	if phaseOrder[phase] < phaseOrder[PhaseOldRemoved] {
		if obs.outbounds[stable] {
			if err := c.withCall(ctx, func(call context.Context) error { return c.removeOutbound(call, stable) }); err != nil {
				return fail(PhaseRouteStaged, err)
			}
		}
		phase = PhaseOldRemoved
	}
	// stable_added: publish the new outbound under the stable tag.
	if phaseOrder[phase] < phaseOrder[PhaseStableAdded] {
		if err := c.withCall(ctx, func(call context.Context) error { return c.addOutbound(call, objects.Outbound) }); err != nil {
			return fail(PhaseOldRemoved, err)
		}
		phase = PhaseStableAdded
	}
	// route_stable: confirm the new outbound exists, then move the route back and
	// verify it.
	if phaseOrder[phase] < phaseOrder[PhaseRouteStable] {
		exists, err := c.outboundExists(ctx, stable)
		if err != nil {
			return fail(PhaseStableAdded, err)
		}
		if !exists {
			return fail(PhaseStableAdded, errors.New("stable outbound is missing before route_stable"))
		}
		if err := e.publishRoute(ctx, id, stable, routeTarget); err != nil {
			return fail(PhaseStableAdded, err)
		}
		if err := c.verifyRoute(ctx, id, stable); err != nil {
			return fail(PhaseStableAdded, err)
		}
		phase = PhaseRouteStable
		routeTarget[id] = stable
	}

	// Clean-up comes last: only now may the staged outbound disappear.
	if err := c.withCall(ctx, func(call context.Context) error { return c.removeOutbound(call, staged) }); err != nil {
		return fail(PhaseRouteStable, err)
	}
	if err := c.verifyRoute(ctx, id, stable); err != nil {
		return fail(PhaseRouteStable, err)
	}

	return Result{
		ProfileID:               id,
		Status:                  StatusReady,
		Written:                 true,
		EffectiveEnabled:        true,
		EffectiveEnabledChanged: !target.PreviouslyEffectiveEnabled,
	}, nil
}

// disable removes the profile's inbound, both outbounds and its route. The
// persisted inbound port is untouched, so a later enable reuses it.
func (e *Engine) disable(ctx context.Context, target Target, routeTarget map[string]string, obs observed) (Result, error) {
	c := e.client
	p := target.Desired.Profile
	res := Result{ProfileID: p.ID}

	delete(routeTarget, p.ID)

	if obs.inbounds[InboundTag(p.ID)] {
		if err := c.withCall(ctx, func(call context.Context) error { return c.removeInbound(call, InboundTag(p.ID)) }); err != nil {
			return e.degrade(target, err), nil
		}
		res.Written = true
	}
	for _, tag := range []string{OutboundTag(p.ID), StagedOutboundTag(p.ID)} {
		if obs.outbounds[tag] {
			if err := c.withCall(ctx, func(call context.Context) error { return c.removeOutbound(call, tag) }); err != nil {
				return e.degrade(target, err), nil
			}
			res.Written = true
		}
	}
	if err := e.publishRoute(ctx, p.ID, "", routeTarget); err != nil {
		return e.degrade(target, err), nil
	}
	if obs.rules[RuleTag(p.ID)] != "" {
		res.Written = true
	}
	res.Status = StatusDisabled
	res.EffectiveEnabled = false
	res.EffectiveEnabledChanged = target.PreviouslyEffectiveEnabled
	return res, nil
}

// publishRoute records the routing intent for one profile and republishes the
// complete rule set. An empty outbound means the profile has no rule.
func (e *Engine) publishRoute(ctx context.Context, profileID, outbound string, routeTarget map[string]string) error {
	if outbound == "" {
		delete(routeTarget, profileID)
	} else {
		routeTarget[profileID] = outbound
	}
	return e.client.withCall(ctx, func(call context.Context) error {
		return e.client.publishRules(call, buildRules(routeTarget))
	})
}

// buildRules renders the complete rule set from the routing intent. It is a
// replacement, never an append, so repeated publications cannot duplicate rules.
func buildRules(routeTarget map[string]string) []*router.RoutingRule {
	rules := make([]*router.RoutingRule, 0, len(routeTarget))
	for _, id := range sortedKeys(routeTarget) {
		rules = append(rules, NewRule(id, routeTarget[id]))
	}
	return rules
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (e *Engine) degrade(target Target, err error) Result {
	return Result{
		ProfileID:               target.Desired.Profile.ID,
		Status:                  StatusDegraded,
		Error:                   err.Error(),
		EffectiveEnabled:        false,
		EffectiveEnabledChanged: target.PreviouslyEffectiveEnabled,
	}
}

// reapOrphans removes egress: objects that are not part of the desired state. A
// swapping profile's :staged outbound is desired and survives.
func (e *Engine) reapOrphans(ctx context.Context, targets []Target, obs observed) error {
	c := e.client
	wantIn := map[string]bool{}
	wantOut := map[string]bool{}
	for _, target := range targets {
		p := target.Desired.Profile
		if !p.Enabled {
			continue
		}
		wantIn[InboundTag(p.ID)] = true
		wantOut[OutboundTag(p.ID)] = true
		if p.RuntimeState == "swapping" {
			wantOut[StagedOutboundTag(p.ID)] = true
		}
	}
	var orphanIn, orphanOut []string
	for tag := range obs.inbounds {
		if _, owned := ProfileIDFromTag(tag); owned && !wantIn[tag] {
			orphanIn = append(orphanIn, tag)
		}
	}
	for tag := range obs.outbounds {
		if _, owned := ProfileIDFromTag(tag); owned && !wantOut[tag] {
			orphanOut = append(orphanOut, tag)
		}
	}
	sort.Strings(orphanIn)
	sort.Strings(orphanOut)
	for _, tag := range orphanIn {
		tag := tag
		if err := c.withCall(ctx, func(call context.Context) error { return c.removeInbound(call, tag) }); err != nil {
			return err
		}
	}
	for _, tag := range orphanOut {
		tag := tag
		if err := c.withCall(ctx, func(call context.Context) error { return c.removeOutbound(call, tag) }); err != nil {
			return err
		}
	}
	return nil
}

// failUsable turns profiles reported usable into degraded results when the
// final route verification failed.
func failUsable(results []Result, err error) []Result {
	for i := range results {
		switch results[i].Status {
		case StatusReady, StatusUnchanged:
			results[i].Status = StatusDegraded
			results[i].Error = err.Error()
			results[i].EffectiveEnabled = false
			results[i].EffectiveEnabledChanged = true
		}
	}
	return results
}
