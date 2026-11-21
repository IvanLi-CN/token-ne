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
func canPrune(i instanceRow) bool {
	return i.State == "stopped" && i.Weight == 0 && i.Heartbeat != nil && i.Spool != nil && *i.Spool == 0 && i.Observed != nil
}

func (r *Runtime) observeDrains(ctx context.Context) error {
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
		if _, e = r.database().Exec(ctx, `update core_instances set inflight=$2,spool_unacked=$3,zero_samples=$4,observed_at=now(),control_status=$5 where id=$1 and state='draining'`, i.ID, i.Inflight, *i.Spool, i.ZeroSamples, raw); e != nil {
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
