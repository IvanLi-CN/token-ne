package cores

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	defaultPruneGrace  = 300 * time.Second
	defaultPruneMaxAge = 24 * time.Hour
	pruneBackoffBase   = 60 * time.Second
	pruneBackoffMax    = time.Hour
	pruneLabelsError   = "ownership labels incomplete; automatic destruction blocked"
	replayMarker       = "spool replay in progress"
)

// ErrSpoolNotDrained marks a manual prune whose instance has no post-drain spool
// zero proof. It matches ErrConflict so generic conflict handling keeps working,
// while the admin layer can answer 409 spool_not_drained.
var ErrSpoolNotDrained error = spoolNotDrainedError{}

type spoolNotDrainedError struct{}

func (spoolNotDrainedError) Error() string { return "spool zero not confirmed after drain" }
func (spoolNotDrainedError) Is(target error) bool {
	return target == ErrConflict || target == ErrSpoolNotDrained
}

// pruneConfig is the automatic-destruction policy. CORE_AUTO_PRUNE is the only
// feature switch and is deliberately not part of the gate formula; the gate is
// the stop-anchored grace window plus the zero proof.
type pruneConfig struct {
	enabled bool
	grace   time.Duration
	maxAge  time.Duration
}

func envSeconds(name string) (time.Duration, bool) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return 0, false
	}
	n, e := strconv.Atoi(raw)
	if e != nil || n < 0 {
		return 0, false
	}
	return time.Duration(n) * time.Second, true
}

func (r *Runtime) pruneConfig() pruneConfig {
	cfg := pruneConfig{enabled: true, grace: defaultPruneGrace, maxAge: defaultPruneMaxAge}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("CORE_AUTO_PRUNE")), "false") {
		cfg.enabled = false
	}
	if d, ok := envSeconds("CORE_AUTO_PRUNE_GRACE_SECONDS"); ok {
		cfg.grace = d
	}
	if d, ok := envSeconds("CORE_AUTO_PRUNE_MAX_AGE_SECONDS"); ok {
		cfg.maxAge = d
	}
	return cfg
}

// pruneCandidate is one retired instance with the durable bookkeeping the gate
// needs and the state of the deployment that retired it. `deployment_id` is the
// deployment that created the instance and only feeds the ownership-label check;
// the retiring deployment is the newest one whose `from` weights named this
// instance, and its `completed` transition anchors the grace window.
type pruneCandidate struct {
	ID                  string
	Version             string
	ContainerID         *string
	ContainerName       string
	State               string
	Weight              int
	Deployment          *string
	StateDir            string
	Spool               *int64
	Ack                 *int64
	ZeroSamples         int
	DrainStarted        *time.Time
	StoppedAt           *time.Time
	ZeroAt              *time.Time
	BlockedAt           *time.Time
	Failures            int
	NextAttempt         *time.Time
	PrunedAt            *time.Time
	LifecycleError      *string
	RetiringState       *string
	RetiringCompletedAt *time.Time
}

const pruneCandidateSelect = `select i.id::text,v.version,i.container_id,i.container_name,i.state,i.weight,i.deployment_id::text,
 i.state_dir,i.spool_unacked,nullif(i.control_status->'spool'->>'last_ack_seq','')::bigint,i.zero_samples,i.draining_started_at,
 i.stopped_at,i.spool_zero_at,i.cleanup_blocked_at,i.prune_failures,i.prune_next_attempt_at,i.pruned_at,i.lifecycle_error,
 r.state,r.updated_at
 from core_instances i join core_versions v on v.id=i.version_id
 left join lateral (
   select d.state,d.updated_at from core_deployments d
   where d.id is distinct from i.deployment_id and (d.progress->'from_weights') ? (i.id::text)
   order by d.created_at desc limit 1
 ) r on true`

func scanPruneCandidate(row pgx.Row) (c pruneCandidate, e error) {
	e = row.Scan(&c.ID, &c.Version, &c.ContainerID, &c.ContainerName, &c.State, &c.Weight, &c.Deployment,
		&c.StateDir, &c.Spool, &c.Ack, &c.ZeroSamples, &c.DrainStarted, &c.StoppedAt, &c.ZeroAt, &c.BlockedAt,
		&c.Failures, &c.NextAttempt, &c.PrunedAt, &c.LifecycleError, &c.RetiringState, &c.RetiringCompletedAt)
	return
}

// pruneCandidates reads instances the deployment machinery owns: a legacy
// Compose core has no deployment label and never participates. A row this pass
// itself put back into drain is included so the replay can be bounded by the
// cleanup max age; a normal deployment drain is never a candidate.
func (r *Runtime) pruneCandidates(ctx context.Context) ([]pruneCandidate, error) {
	rows, e := r.database().Query(ctx, pruneCandidateSelect+`
 where i.deployment_id is not null and i.weight=0 and i.pruned_at is null
   and (i.state='stopped' or (i.state='draining' and i.lifecycle_error=$1))
 order by i.updated_at,i.id`, replayMarker)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []pruneCandidate{}
	for rows.Next() {
		c, e := scanPruneCandidate(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// pruneReason names the single gate condition that kept an instance alive. The
// empty string means the instance is a deletion candidate.
const (
	pruneReasonNone       = ""
	pruneReasonState      = "state"
	pruneReasonDeployment = "deployment_incomplete"
	pruneReasonStoppedAt  = "stopped_at_missing"
	pruneReasonSpool      = "spool_not_zeroed"
	pruneReasonBlocked    = "blocked"
	pruneReasonBackoff    = "backoff"
	pruneReasonGrace      = "grace"
	pruneReasonLabels     = "labels_incomplete"
)

const (
	pruneOutcomeSuccess      = "success"
	pruneOutcomeFailed       = "failed"
	pruneOutcomeBlocked      = "blocked"
	pruneOutcomeLabelsBroken = "labels_incomplete"
)

// ownershipLabelsMatch verifies the four ownership labels the contract makes
// mandatory before the control plane may destroy a container it believes it
// owns. The deployment label must agree with the recorded instance row.
func ownershipLabelsMatch(labels map[string]string, c pruneCandidate) bool {
	if labels == nil || c.Deployment == nil || *c.Deployment == "" {
		return false
	}
	return labels[RoleLabel] == "core" && labels[VersionLabel] == c.Version &&
		labels[InstanceLabel] == c.ID && labels[DeploymentLabel] == *c.Deployment
}

// pruneEligibility is the pure gate: the deployment that retired the instance
// reached `completed`, the instance is stopped at weight zero, a post-drain
// zero observation is still valid, cleanup is not blocked or backing off, the
// ownership labels are complete, and the grace window anchored at the later of
// the stop and the completion has elapsed.
func pruneEligibility(c pruneCandidate, containers map[string]Container, now time.Time, cfg pruneConfig) string {
	if c.State != "stopped" || c.Weight != 0 || c.PrunedAt != nil || c.Deployment == nil {
		return pruneReasonState
	}
	if c.RetiringState == nil || *c.RetiringState != "completed" {
		return pruneReasonDeployment
	}
	if c.StoppedAt == nil {
		return pruneReasonStoppedAt
	}
	if !hasZeroProof(c) {
		return pruneReasonSpool
	}
	// cleanup_blocked is not terminal: a zero proof observed after the block was
	// set re-enters the instance into the deletion candidate set. The observation
	// paths also clear the column, so this also covers a stale row.
	if c.BlockedAt != nil && (c.ZeroAt == nil || !c.ZeroAt.After(*c.BlockedAt)) {
		return pruneReasonBlocked
	}
	if c.NextAttempt != nil && now.Before(*c.NextAttempt) {
		return pruneReasonBackoff
	}
	anchor := *c.StoppedAt
	if c.RetiringCompletedAt != nil && c.RetiringCompletedAt.After(anchor) {
		anchor = *c.RetiringCompletedAt
	}
	if now.Before(anchor.Add(cfg.grace)) {
		return pruneReasonGrace
	}
	// A container that still exists must be stopped and carry the complete
	// ownership labels. An absent container is already gone, so the recorded row
	// is the only remaining identity; rollback uses the same distinction.
	if container, ok := containers[c.ID]; ok {
		if container.State == "running" || container.State == "paused" || container.State == "restarting" {
			return pruneReasonState
		}
		if !ownershipLabelsMatch(container.Labels, c) {
			return pruneReasonLabels
		}
	}
	return pruneReasonNone
}

// postDrainZero reports whether one status observation is a valid zero proof:
// the control plane had already put the instance into drain when the observation
// was taken, nothing was in flight and the spool reported no unacked events. An
// observation that predates the drain mark, a missing counter, or a status taken
// outside a drain never proves zero. A core restarted after the drain mark also
// qualifies: its observation post-dates the drain, so a zero there is the rescan
// the contract requires before destruction.
func postDrainZero(drainStarted *time.Time, status Status, observedAt time.Time) bool {
	return drainStarted != nil && !observedAt.Before(*drainStarted) &&
		status.Inflight != nil && *status.Inflight == 0 &&
		status.Spool.Unacked != nil && *status.Spool.Unacked == 0
}

// spoolProof is the durable effect of one observation on the zero proof. A
// nonzero spool invalidates any earlier proof; a valid post-drain zero
// observation establishes it. Other observations return nil so callers keep the
// stored value. A proof also lifts a cleanup block, because the block is not
// terminal.
func spoolProof(drainStarted *time.Time, status Status, observedAt time.Time) *time.Time {
	if status.Spool.Unacked == nil || *status.Spool.Unacked > 0 {
		return nil
	}
	if postDrainZero(drainStarted, status, observedAt) {
		zero := observedAt
		return &zero
	}
	return nil
}

// manualZeroProof is the operator-facing proof: the instance is stopped at
// weight zero and a status observation taken at or after drain completion
// reported an empty spool. The column is cleared by any later nonzero
// observation, so its presence is the proof.
func manualZeroProof(c pruneCandidate) bool {
	return c.State == "stopped" && c.Weight == 0 && hasZeroProof(c)
}

func pruneBackoff(failures int) time.Duration {
	if failures < 1 {
		failures = 1
	}
	delay := pruneBackoffBase
	for n := 1; n < failures && delay < pruneBackoffMax; n++ {
		delay *= 2
	}
	if delay > pruneBackoffMax {
		delay = pruneBackoffMax
	}
	return delay
}

// autoPrune destroys every retired instance whose gate is open. The
// reconciliation loop gives it its own deadline because deleting a state
// directory starts a short-lived Docker helper.
func (r *Runtime) autoPrune(ctx context.Context) error {
	cfg := r.pruneConfig()
	if !cfg.enabled || r.Docker == nil {
		return nil
	}
	// A crash between claiming an instance and deleting its directory leaves it
	// in `pruning`. Both deletions are idempotent, so re-arm it; rows touched by a
	// concurrent pass are younger than the window and stay claimed.
	if _, e := r.database().Exec(ctx, `update core_instances set state='stopped',updated_at=now() where state='pruning' and updated_at < now()-interval '5 minutes'`); e != nil {
		return e
	}
	// Rows stopped before this bookkeeping existed have no anchor. updated_at can
	// only delay the grace window, never shorten it.
	if _, e := r.database().Exec(ctx, `update core_instances set stopped_at=updated_at where state='stopped' and stopped_at is null`); e != nil {
		return e
	}
	candidates, e := r.pruneCandidates(ctx)
	if e != nil {
		return e
	}
	if len(candidates) == 0 {
		return nil
	}
	containers, e := r.Docker.List(ctx)
	if e != nil {
		return e
	}
	byInstance := map[string]Container{}
	for _, container := range containers {
		labels := container.Labels
		if labels == nil || labels[RoleLabel] != "core" {
			continue
		}
		if r.Project != "" && labels["com.docker.compose.project"] != r.Project {
			continue
		}
		if id := labels[InstanceLabel]; UUID(id, false) {
			byInstance[id] = container
		}
	}
	now := time.Now().UTC()
	var failures []error
	for _, c := range candidates {
		// A replay in progress is bounded by the same cleanup max age: an
		// instance whose spool never reaches zero must not stay in drain forever.
		if c.State == "draining" {
			if c.StoppedAt != nil && now.Sub(*c.StoppedAt) < cfg.maxAge && c.BlockedAt == nil {
				// A replay core that died before proving zero is restarted, so a
				// crash cannot stall the instance until the cleanup max age.
				if container, ok := byInstance[c.ID]; ok && container.State != "running" {
					if e := r.Docker.Start(ctx, container.ID); e != nil {
						failures = append(failures, e)
					}
				}
				continue
			}
			if e := r.stopReplay(ctx, c); e != nil {
				failures = append(failures, e)
			}
			if c.BlockedAt == nil {
				if e := r.blockCleanup(ctx, c, now, cfg); e != nil {
					failures = append(failures, e)
				}
			}
			continue
		}
		// cleanup_blocked counts from the instance stop, not from the retiring
		// deployment's completion, so it applies before and independently of the
		// deletion gate. A blocked instance keeps its container and state
		// directory, stops automatic retries, and leaves the candidate path here.
		if !hasZeroProof(c) && c.StoppedAt != nil && now.Sub(*c.StoppedAt) >= cfg.maxAge {
			if e := r.blockCleanup(ctx, c, now, cfg); e != nil {
				failures = append(failures, e)
			}
			continue
		}
		reason := pruneEligibility(c, byInstance, now, cfg)
		// A fixed label set must not leave the alert marker behind, or a later
		// incompleteness would go unreported.
		if reason != pruneReasonLabels && c.LifecycleError != nil && *c.LifecycleError == pruneLabelsError {
			if _, e := r.database().Exec(ctx, `update core_instances set lifecycle_error=null,updated_at=now() where id=$1`, c.ID); e != nil {
				failures = append(failures, e)
			}
		}
		switch reason {
		case pruneReasonNone:
			if e := r.destroyInstance(ctx, "system", c, now, true); e != nil {
				failures = append(failures, e)
			}
		case pruneReasonLabels:
			if e := r.reportIncompleteLabels(ctx, c); e != nil {
				failures = append(failures, e)
			}
		case pruneReasonSpool:
			// The contract never destroys on a nonzero or missing observation: the
			// instance is restarted as a compatible core so it rescans and replays
			// its spool, then re-enters the candidate set once stopped. Replay waits
			// for the retirement deployment to finish: reviving an instance the
			// deployment still tracks as retiring would make its drain step wait
			// again.
			if c.RetiringState == nil || *c.RetiringState != "completed" {
				continue
			}
			if e := r.replayRetired(ctx, c); e != nil {
				failures = append(failures, e)
			}
		}
	}
	return errors.Join(failures...)
}

// hasZeroProof reports whether the durable column holds a proof that post-dates
// the drain mark. The cleanup block, the automatic gate and the manual prune all
// use this one definition so their decisions can never disagree.
func hasZeroProof(c pruneCandidate) bool {
	return c.DrainStarted != nil && c.ZeroAt != nil && !c.ZeroAt.Before(*c.DrainStarted)
}

// replayRetired restarts a retired instance so a compatible core can rescan the
// retained spool. It returns the instance to `draining` with a fresh drain
// anchor, which keeps it out of traffic and lets the existing drain observation
// produce a new post-drain zero proof; the original stop anchor is preserved so
// the cleanup max age still counts from the first stop.
func (r *Runtime) replayRetired(ctx context.Context, c pruneCandidate) error {
	if c.State != "stopped" {
		return nil
	}
	// A forced prune may have removed the container while the directory was
	// retained. `c` is this pass's snapshot, so its ContainerID stays nil even
	// after the row is updated; restoreContainer already created and started the
	// replacement, which is why the recreated case must not start it again.
	containerID := c.ContainerID
	recreated := false
	if containerID == nil {
		exists, e := r.Docker.StateDirExists(ctx, r.StateHostDir, c.ID)
		if e != nil || !exists {
			return e
		}
		row, e := scanInstance(r.database().QueryRow(ctx, instanceSelect+` where i.id=$1`, c.ID))
		if e != nil {
			return e
		}
		// restoreContainer both creates and starts the replacement and records its
		// id itself, so there is nothing to start afterwards.
		if e = r.restoreContainer(ctx, row); e != nil {
			return e
		}
		recreated = true
	}
	if _, e := r.database().Exec(ctx, `update core_instances set state='draining',zero_samples=0,draining_started_at=now(),
 spool_zero_at=null,lifecycle_error=$2,updated_at=now() where id=$1 and state='stopped'`, c.ID, replayMarker); e != nil {
		return e
	}
	if !recreated {
		if e := r.Docker.Start(ctx, *containerID); e != nil {
			_, persistErr := r.database().Exec(ctx, `update core_instances set state='stopped',lifecycle_error=$2 where id=$1`, c.ID, e.Error())
			return errors.Join(e, persistErr)
		}
	}
	return r.Audit(ctx, "system", "core.instance.replay", "core_instance", c.ID,
		map[string]any{"reason": "spool not confirmed zero", "last_ack_seq": c.Ack, "spool_unacked": c.Spool})
}

// stopReplay ends a blocked replay attempt: the container stops, the state
// directory and record remain, and no further automatic restart happens.
func (r *Runtime) stopReplay(ctx context.Context, c pruneCandidate) error {
	if c.ContainerID != nil {
		if e := r.Docker.Stop(ctx, *c.ContainerID, 30*time.Second); e != nil {
			return e
		}
	}
	_, e := r.database().Exec(ctx, `update core_instances set state='stopped',weight=0,updated_at=now() where id=$1 and state='draining'`, c.ID)
	return e
}

// reportIncompleteLabels alerts once per instance: the metric is a counter and
// the audit records the same condition, so repeating them every pass would only
// inflate both. lifecycle_error marks that the report was made.
func (r *Runtime) reportIncompleteLabels(ctx context.Context, c pruneCandidate) error {
	if c.LifecycleError != nil && *c.LifecycleError == pruneLabelsError {
		return nil
	}
	if r.Metrics != nil {
		r.Metrics.ObserveCoreAutoPrune(pruneOutcomeLabelsBroken)
	}
	slog.Warn("retired core retained because ownership labels are incomplete", "instance", c.ID, "container", c.ContainerName)
	if _, e := r.database().Exec(ctx, `update core_instances set lifecycle_error=$2,updated_at=now() where id=$1`, c.ID, pruneLabelsError); e != nil {
		return e
	}
	return r.Audit(ctx, "system", "core.instance.prune", "core_instance", c.ID,
		map[string]any{"automatic": true, "result": pruneOutcomeLabelsBroken, "last_ack_seq": c.Ack})
}

// blockCleanup marks an instance whose spool never reached zero before the max
// age, counted from the stop. The block stops retries but is not terminal: a
// later zero observation clears it in the observation paths.
func (r *Runtime) blockCleanup(ctx context.Context, c pruneCandidate, now time.Time, cfg pruneConfig) error {
	if c.BlockedAt != nil {
		return nil
	}
	tag, e := r.database().Exec(ctx, `update core_instances set cleanup_blocked_at=$2,updated_at=now(),
 lifecycle_error='spool zero not confirmed before cleanup max age' where id=$1 and cleanup_blocked_at is null`, c.ID, now)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return nil
	}
	if r.Metrics != nil {
		r.Metrics.ObserveCoreAutoPrune(pruneOutcomeBlocked)
	}
	slog.Warn("retired core cleanup blocked: spool never reached zero", "instance", c.ID, "stopped_at", c.StoppedAt, "max_age_seconds", cfg.maxAge.Seconds())
	return nil
}

// destroyInstance claims one stopped instance, deletes its container and then
// its state directory under the same zero proof, and records audit plus metric.
// A failed removal keeps the record, persists a backoff retry and never marks
// the instance destroyed.
func (r *Runtime) destroyInstance(ctx context.Context, actor string, c pruneCandidate, now time.Time, automatic bool) error {
	// The claim re-checks the proof against the drain mark: a concurrent nonzero
	// observation may have cleared or superseded the column since the read.
	tag, e := r.database().Exec(ctx, `update core_instances set state='pruning',updated_at=now()
 where id=$1 and state='stopped' and weight=0 and spool_zero_at is not null and spool_zero_at >= draining_started_at`, c.ID)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return nil
	}
	payload := map[string]any{"automatic": automatic, "result": pruneOutcomeSuccess, "force": false,
		"spool_unacked": 0, "last_ack_seq": c.Ack, "container_removed": c.ContainerID != nil, "state_dir_removed": true}
	if e = r.removeArtifacts(ctx, c); e != nil {
		next := now.Add(pruneBackoff(c.Failures + 1))
		_, persistErr := r.database().Exec(ctx, `update core_instances set state='stopped',prune_failures=prune_failures+1,
		 prune_next_attempt_at=$2,lifecycle_error=$3,updated_at=now() where id=$1`, c.ID, next, e.Error())
		if r.Metrics != nil {
			r.Metrics.ObserveCoreAutoPrune(pruneOutcomeFailed)
		}
		slog.Warn("automatic core destruction failed", "instance", c.ID, "error", e, "next_attempt_at", next)
		auditErr := r.Audit(ctx, actor, "core.instance.prune", "core_instance", c.ID,
			map[string]any{"automatic": automatic, "result": pruneOutcomeFailed, "error": e.Error(), "last_ack_seq": c.Ack, "retry_at": next})
		return errors.Join(e, persistErr, auditErr)
	}
	if _, e = r.database().Exec(ctx, `update core_instances set state='pruned',pruned_at=now(),prune_next_attempt_at=null,lifecycle_error=null,updated_at=now() where id=$1`, c.ID); e != nil {
		return e
	}
	if r.Metrics != nil {
		r.Metrics.ObserveCoreAutoPrune(pruneOutcomeSuccess)
	}
	return r.Audit(ctx, actor, "core.instance.prune", "core_instance", c.ID, payload)
}

// removeArtifacts deletes the container first and the state directory second,
// both only under the caller's zero proof. The helper removes the directory
// because the API container never mounts the state root.
func (r *Runtime) removeArtifacts(ctx context.Context, c pruneCandidate) error {
	if c.ContainerID != nil {
		if e := r.Docker.Remove(ctx, *c.ContainerID, false); e != nil {
			return fmt.Errorf("container removal: %w", e)
		}
	}
	if e := r.Docker.RemoveStateDir(ctx, r.StateHostDir, c.ID); e != nil {
		return fmt.Errorf("state directory removal: %w", e)
	}
	return nil
}
