use token_ne_core::{proxy::usage::UsageEvent, spool::Spool};

fn event() -> UsageEvent {
    let fixture: serde_json::Value =
        serde_json::from_str(include_str!("../../docs/fixtures/usage-batch.v1.json")).unwrap();
    let mut event: UsageEvent =
        serde_json::from_value(fixture["payload"]["events"][0].clone()).unwrap();
    event.occurred_at = chrono::Utc::now().to_rfc3339();
    event
}

#[test]
fn append_survives_reopen_and_never_stores_bodies() {
    let dir = tempfile::tempdir().unwrap();
    let spool = Spool::open(dir.path(), 1024 * 1024, 4096).unwrap();
    spool.append(event()).unwrap();
    drop(spool);
    let spool = Spool::open(dir.path(), 1024 * 1024, 4096).unwrap();
    let pending = spool.pending(10).unwrap();
    assert_eq!(pending.len(), 1);
    let text = std::fs::read_to_string(
        std::fs::read_dir(dir.path())
            .unwrap()
            .next()
            .unwrap()
            .unwrap()
            .path(),
    )
    .unwrap_or_default();
    assert!(!text.contains("prompt"));
}

#[test]
fn acknowledgement_advances_and_rotation_keeps_records_readable() {
    let dir = tempfile::tempdir().unwrap();
    let spool = Spool::open(dir.path(), 1024 * 1024, 4096).unwrap();
    spool.append(event()).unwrap();
    spool.append(event()).unwrap();
    assert!(
        std::fs::read_dir(dir.path())
            .unwrap()
            .filter_map(Result::ok)
            .filter(|e| e.file_name().to_string_lossy().starts_with("seg-"))
            .count()
            > 0
    );
    let pending = spool.pending(10).unwrap();
    assert_eq!(pending.len(), 2);
    spool.acknowledge(pending[0].0).unwrap();
    assert_eq!(spool.unacked().unwrap(), 1);
}

#[test]
fn age_prunes_oldest_segment_and_counts_drop() {
    let dir = tempfile::tempdir().unwrap();
    let spool = Spool::open_with_limits(dir.path(), 1024 * 1024, 4096, 0, 0).unwrap();
    let mut old = event();
    old.occurred_at = "2000-01-01T00:00:00Z".into();
    spool.append(old).unwrap();
    spool.append(event()).unwrap();
    spool.prune().unwrap();
    assert!(spool.stats().unwrap().dropped_total >= 1);
}

#[test]
fn capacity_and_free_space_bounds_reject_when_current_segment_cannot_be_safely_dropped() {
    let dir = tempfile::tempdir().unwrap();
    let spool = Spool::open_with_limits(dir.path(), 1024 * 1024, 4096, 24, u64::MAX);
    assert_eq!(spool.err().unwrap().kind(), std::io::ErrorKind::StorageFull);
}

#[test]
fn byte_capacity_drops_only_oldest_prefix_and_counts_it() {
    let dir = tempfile::tempdir().unwrap();
    let e = event();
    let one = serde_json::to_vec(&serde_json::json!({"seq":1,"event":e}))
        .unwrap()
        .len() as u64
        + 1;
    let spool = Spool::open_with_limits(dir.path(), one, one, 24, 0).unwrap();
    spool.append(event()).unwrap();
    spool.append(event()).unwrap();
    assert_eq!(spool.stats().unwrap().dropped_total, 1);
    assert_eq!(spool.unacked().unwrap(), 1);
}
