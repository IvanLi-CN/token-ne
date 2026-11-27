package cores

import (
	"context"
	"encoding/json"
	"time"
)

type progress struct {
	Stage    int            `json:"stage"`
	From     map[string]int `json:"from_weights"`
	To       map[string]int `json:"to_weights"`
	Started  time.Time      `json:"stage_started_at"`
	Minimum  int            `json:"minimum_stage_seconds"`
	Deadline int            `json:"drain_deadline_seconds"`
	// AutoAdvance moves the rollout from ready to the first weight stage without
	// an operator shift. A missing value means true: deployments recorded before
	// the field existed follow the current default.
	AutoAdvance *bool `json:"auto_advance"`
}

func (p progress) autoAdvance() bool {
	return p.AutoAdvance == nil || *p.AutoAdvance
}

func (p progress) canAdvance(now time.Time) bool {
	minimum := p.Minimum
	if minimum < 60 {
		minimum = 60
	}
	return p.Stage >= 0 && p.Stage < len(stages)-1 && now.Sub(p.Started) >= time.Duration(minimum)*time.Second
}
func (p progress) weights(stage int) map[string]int {
	result := map[string]int{}
	fraction := stages[stage]
	for id, w := range p.From {
		result[id] = w * (100 - fraction) / 100
	}
	for id, w := range p.To {
		result[id] = w * fraction / 100
	}
	return result
}
func drainDecision(i instanceRow, now time.Time, timeout time.Duration) string {
	if i.DrainStarted == nil {
		return "wait"
	}
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	if now.Sub(*i.DrainStarted) > timeout {
		return "timeout"
	}
	if now.Sub(*i.DrainStarted) < 10*time.Second || i.Observed == nil || now.Sub(*i.Observed) > 30*time.Second || i.ZeroSamples < 3 || i.Inflight != 0 || i.Spool == nil || *i.Spool != 0 {
		return "wait"
	}
	return "stop"
}
func (r *Runtime) observeDrains(ctx context.Context) error {
	r.deployMu.Lock()
	defer r.deployMu.Unlock()
	instances, e := r.instances(ctx)
	if e != nil {
		return e
	}
	for _, i := range instances {
		if i.State != "draining" {
			continue
		}
		status, e := r.Peer.Status(ctx, i.Address, i.ID)
		if e != nil {
			continue
		}
		if status.Validate(time.Now()) != nil || status.Instance != i.ID {
			continue
		}
		i.Inflight = *status.Inflight
		i.Spool = status.Spool.Unacked
		now := time.Now().UTC()
		i.Observed = &now
		if i.Inflight == 0 {
			i.ZeroSamples++
		} else {
			i.ZeroSamples = 0
		}
		raw, _ := json.Marshal(status)
		// This observation is taken while the instance drains, so a zero spool and
		// zero inflight here is a valid zero proof; a nonzero one invalidates an
		// earlier proof and a zero lifts a cleanup block.
		zeroAt := spoolProof(i.DrainStarted, status, now)
		if _, e = r.database().Exec(ctx, `update core_instances set inflight=$2,spool_unacked=$3,zero_samples=$4,observed_at=now(),control_status=$5,
 spool_zero_at=case when $3>0 then null when $6::timestamptz is not null then $6 else spool_zero_at end,
 cleanup_blocked_at=case when $6::timestamptz is not null then null else cleanup_blocked_at end
 where id=$1 and state='draining'`, i.ID, i.Inflight, *i.Spool, i.ZeroSamples, raw, zeroAt); e != nil {
			return e
		}
		if drainDecision(i, now, 30*time.Minute) == "stop" {
			if e = r.stopInstance(ctx, i); e != nil {
				return e
			}
		}
	}
	return nil
}
