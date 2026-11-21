use token_ne_core::{proxy::usage::UsageEvent, spool::Spool};

fn event() -> UsageEvent {
    let fixture: serde_json::Value =
        serde_json::from_str(include_str!("../../docs/fixtures/usage-batch.v1.json")).unwrap();
    serde_json::from_value(fixture["payload"]["events"][0].clone()).unwrap()
}

#[test]
fn append_survives_reopen_and_never_stores_bodies() {
    let dir = tempfile::tempdir().unwrap();
    let spool = Spool::open(dir.path(), 1024 * 1024, 200).unwrap();
    spool.append(event()).unwrap();
    drop(spool);
    let spool = Spool::open(dir.path(), 1024 * 1024, 200).unwrap();
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
    let spool = Spool::open(dir.path(), 1024 * 1024, 1).unwrap();
    spool.append(event()).unwrap();
    spool.append(event()).unwrap();
    let pending = spool.pending(10).unwrap();
    assert_eq!(pending.len(), 2);
    spool.acknowledge(pending[0].0).unwrap();
    assert_eq!(spool.unacked().unwrap(), 1);
}
