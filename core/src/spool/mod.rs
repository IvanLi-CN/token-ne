//! Private append-only JSONL usage spool.
use crate::proxy::usage::UsageEvent;
use serde::{Deserialize, Serialize};
use std::{
    fs::{self, File, OpenOptions},
    io::{BufRead, BufReader, Write},
    path::{Path, PathBuf},
    sync::{
        Mutex,
        atomic::{AtomicU64, Ordering},
    },
    time::{SystemTime, UNIX_EPOCH},
};

#[derive(Debug, Serialize, Deserialize)]
struct Record {
    seq: u64,
    event: UsageEvent,
}
#[derive(Debug, Default, Serialize, Deserialize)]
struct Ack {
    spool_format_version: u32,
    processed_seq: u64,
    last_ack_seq: u64,
    dropped: Vec<u64>,
}
#[derive(Debug)]
pub struct Spool {
    dir: PathBuf,
    segment_bytes: u64,
    max_bytes: u64,
    max_age_hours: u64,
    min_free_bytes: u64,
    send_accepted: AtomicU64,
    send_retry: AtomicU64,
    send_rejected: AtomicU64,
    replayed: AtomicU64,
    state: Mutex<State>,
}
#[derive(Debug)]
struct State {
    ack: Ack,
    current: File,
    current_path: PathBuf,
    current_len: u64,
    next_seq: u64,
}
impl Spool {
    pub fn open(
        dir: impl AsRef<Path>,
        max_bytes: u64,
        segment_bytes: u64,
    ) -> std::io::Result<Self> {
        Self::open_with_limits(dir, max_bytes, segment_bytes, 24, 0)
    }
    pub fn open_with_limits(
        dir: impl AsRef<Path>,
        max_bytes: u64,
        segment_bytes: u64,
        max_age_hours: u64,
        min_free_bytes: u64,
    ) -> std::io::Result<Self> {
        fs::create_dir_all(dir.as_ref())?;
        let dir = dir.as_ref().to_path_buf();
        let ack_path = dir.join("ack.json");
        let mut ack: Ack = if ack_path.exists() {
            serde_json::from_slice(&fs::read(&ack_path)?).unwrap_or_default()
        } else {
            Ack::default()
        };
        ack.spool_format_version = 1;
        let mut next_seq = ack.processed_seq + 1;
        let mut active: Option<(PathBuf, u64)> = None;
        for path in segments(&dir)? {
            for line in BufReader::new(File::open(&path)?)
                .lines()
                .map_while(Result::ok)
            {
                if let Ok(r) = serde_json::from_str::<Record>(&line) {
                    next_seq = next_seq.max(r.seq + 1);
                }
            }
            if let Some(stem) = path.file_stem().and_then(|v| v.to_str())
                && let Some(first) = stem
                    .strip_prefix("seg-")
                    .and_then(|v| v.parse::<u64>().ok())
                && first > ack.processed_seq
            {
                let len = fs::metadata(&path)?.len();
                active = Some((path.clone(), len));
            }
        }
        let (current_path, _current_len) =
            active.unwrap_or((dir.join(format!("seg-{:020}.jsonl", next_seq)), 0));
        let current = OpenOptions::new()
            .create(true)
            .append(true)
            .read(true)
            .open(&current_path)?;
        let current_len = current.metadata()?.len();
        let spool = Self {
            dir,
            max_bytes,
            segment_bytes,
            max_age_hours,
            min_free_bytes,
            send_accepted: AtomicU64::new(0),
            send_retry: AtomicU64::new(0),
            send_rejected: AtomicU64::new(0),
            replayed: AtomicU64::new(0),
            state: Mutex::new(State {
                ack,
                current,
                current_path,
                current_len,
                next_seq,
            }),
        };
        spool.prune()?;
        Ok(spool)
    }
    pub fn append(&self, event: UsageEvent) -> std::io::Result<u64> {
        let mut s = self.state.lock().unwrap();
        let seq = s.next_seq;
        let mut bytes =
            serde_json::to_vec(&Record { seq, event }).map_err(std::io::Error::other)?;
        bytes.push(b'\n');
        if bytes.len() as u64 > self.segment_bytes || bytes.len() as u64 > self.max_bytes {
            return Err(std::io::Error::other("usage event exceeds segment_bytes"));
        }
        let total = spool_bytes(&self.dir)?;
        if total.saturating_add(bytes.len() as u64) > self.max_bytes {
            let oldest = segments(&self.dir)?.first().cloned();
            if let Some(path) = oldest.filter(|p| p != &s.current_path) {
                let records = read_records(&path)?;
                let max_seq = records.last().map_or(0, |r| r.seq);
                let len = fs::metadata(&path)?.len();
                fs::remove_file(path)?;
                if max_seq > s.ack.processed_seq {
                    s.ack.processed_seq = max_seq;
                    s.ack.dropped.extend(records.iter().map(|r| r.seq));
                }
                persist_ack(&self.dir, &s.ack)?;
                if total.saturating_sub(len).saturating_add(bytes.len() as u64) > self.max_bytes {
                    return Err(std::io::Error::from(std::io::ErrorKind::StorageFull));
                }
            } else {
                return Err(std::io::Error::from(std::io::ErrorKind::StorageFull));
            }
        }
        if s.current_len > 0 && (s.current_len + bytes.len() as u64 > self.segment_bytes) {
            s.current.sync_all()?;
            s.current_path = self.dir.join(format!("seg-{:020}.jsonl", seq));
            s.current = OpenOptions::new()
                .create(true)
                .append(true)
                .read(true)
                .open(&s.current_path)?;
            s.current_len = 0;
        }
        if disk_available(&self.dir)?.saturating_sub(bytes.len() as u64) < self.min_free_bytes {
            return Err(std::io::Error::from(std::io::ErrorKind::StorageFull));
        }
        s.current.write_all(&bytes)?;
        s.current.sync_data()?;
        s.current_len += bytes.len() as u64;
        s.next_seq += 1;
        s.current.sync_all()?;
        s.current_path = self.dir.join(format!("seg-{:020}.jsonl", s.next_seq));
        s.current = OpenOptions::new()
            .create(true)
            .append(true)
            .read(true)
            .open(&s.current_path)?;
        s.current_len = 0;
        self.prune_locked(&mut s)?;
        Ok(seq)
    }
    pub fn prune(&self) -> std::io::Result<()> {
        let mut s = self.state.lock().unwrap();
        self.prune_locked(&mut s)
    }
    fn prune_locked(&self, s: &mut State) -> std::io::Result<()> {
        let now = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap_or_default()
            .as_secs();
        let max_age = self.max_age_hours.saturating_mul(3600);
        let mut total = spool_bytes(&self.dir)?;
        let mut dropping_prefix = true;
        for p in segments(&self.dir)? {
            let records = read_records(&p)?;
            if records.is_empty() {
                continue;
            }
            let old = chrono::DateTime::parse_from_rfc3339(&records[0].event.occurred_at)
                .ok()
                .is_some_and(|t| now.saturating_sub(t.timestamp().max(0) as u64) > max_age);
            let max_seq = records.last().unwrap().seq;
            if max_seq <= s.ack.processed_seq
                || old
                || total > self.max_bytes
                || disk_available(&self.dir)? < self.min_free_bytes
            {
                let len = fs::metadata(&p)?.len();
                if p == s.current_path {
                    continue;
                }
                if max_seq > s.ack.processed_seq {
                    if dropping_prefix {
                        s.ack.dropped.extend(records.iter().map(|r| r.seq));
                        s.ack.processed_seq = max_seq;
                    } else {
                        break;
                    }
                }
                fs::remove_file(&p)?;
                total = total.saturating_sub(len);
            } else {
                dropping_prefix = false;
            }
        }
        persist_ack(&self.dir, &s.ack)?;
        if spool_bytes(&self.dir)? > self.max_bytes
            || disk_available(&self.dir)? < self.min_free_bytes
        {
            return Err(std::io::Error::from(std::io::ErrorKind::StorageFull));
        }
        Ok(())
    }
    pub fn record_send(&self, result: &str) {
        match result {
            "accepted" => {
                self.send_accepted.fetch_add(1, Ordering::Relaxed);
            }
            "rejected" => {
                self.send_rejected.fetch_add(1, Ordering::Relaxed);
            }
            _ => {
                self.send_retry.fetch_add(1, Ordering::Relaxed);
            }
        }
    }
    pub fn record_replay(&self, n: u64) {
        self.replayed.fetch_add(n, Ordering::Relaxed);
    }
    pub fn stats(&self) -> std::io::Result<SpoolStats> {
        let pending = self.pending(usize::MAX)?;
        let oldest = pending
            .first()
            .and_then(|(_, e)| chrono::DateTime::parse_from_rfc3339(&e.occurred_at).ok())
            .map(|t| {
                chrono::Utc::now()
                    .timestamp()
                    .saturating_sub(t.timestamp())
                    .max(0) as u64
            });
        Ok(SpoolStats {
            bytes: self.bytes()?,
            unacked_events: pending.len() as u64,
            oldest_unacked_seconds: oldest,
            last_ack_seq: self.last_ack_seq(),
            dropped_total: self.dropped_total(),
            send_accepted: self.send_accepted.load(Ordering::Relaxed),
            send_retry: self.send_retry.load(Ordering::Relaxed),
            send_rejected: self.send_rejected.load(Ordering::Relaxed),
            replayed: self.replayed.load(Ordering::Relaxed),
        })
    }
    pub fn oldest_unacked_at(&self) -> std::io::Result<Option<String>> {
        Ok(self.pending(1)?.first().map(|(_, e)| e.occurred_at.clone()))
    }
    pub fn pending(&self, limit: usize) -> std::io::Result<Vec<(u64, UsageEvent)>> {
        let s = self.state.lock().unwrap();
        let mut out = Vec::new();
        for p in segments(&self.dir)? {
            for line in BufReader::new(File::open(p)?).lines().map_while(Result::ok) {
                if let Ok(r) = serde_json::from_str::<Record>(&line)
                    && r.seq > s.ack.processed_seq
                {
                    out.push((r.seq, r.event));
                    if out.len() >= limit {
                        return Ok(out);
                    }
                }
            }
        }
        Ok(out)
    }
    pub fn acknowledge(&self, seq: u64) -> std::io::Result<()> {
        let mut s = self.state.lock().unwrap();
        s.ack.processed_seq = s.ack.processed_seq.max(seq);
        s.ack.last_ack_seq = s.ack.last_ack_seq.max(seq);
        persist_ack(&self.dir, &s.ack)?;
        Ok(())
    }
    pub fn drop_through(&self, seq: u64) -> std::io::Result<()> {
        let mut s = self.state.lock().unwrap();
        for (n, _) in self.pending(usize::MAX)? {
            if n <= seq {
                s.ack.dropped.push(n);
            }
        }
        s.ack.processed_seq = s.ack.processed_seq.max(seq);
        persist_ack(&self.dir, &s.ack)
    }
    pub fn acknowledge_with_drops(&self, seq: u64, dropped: &[u64]) -> std::io::Result<()> {
        let mut s = self.state.lock().unwrap();
        s.ack.processed_seq = s.ack.processed_seq.max(seq);
        s.ack.last_ack_seq = s.ack.last_ack_seq.max(seq);
        s.ack.dropped.extend_from_slice(dropped);
        persist_ack(&self.dir, &s.ack)?;
        for p in segments(&self.dir)? {
            let records = read_records(&p)?;
            if records.last().is_some_and(|r| r.seq <= s.ack.processed_seq) && p != s.current_path {
                fs::remove_file(p)?;
            }
        }
        Ok(())
    }
    pub fn finalize_ack(&self) -> std::io::Result<()> {
        let s = self.state.lock().unwrap();
        persist_ack(&self.dir, &s.ack)?;
        s.current.sync_all()?;
        Ok(())
    }
    pub fn unacked(&self) -> std::io::Result<usize> {
        Ok(self.pending(usize::MAX)?.len())
    }
    pub fn bytes(&self) -> std::io::Result<u64> {
        spool_bytes(&self.dir)
    }
    fn last_ack_seq(&self) -> u64 {
        self.state.lock().unwrap().ack.last_ack_seq
    }
    fn dropped_total(&self) -> u64 {
        self.state.lock().unwrap().ack.dropped.len() as u64
    }
}
#[derive(Clone, Debug, Default)]
pub struct SpoolStats {
    pub bytes: u64,
    pub unacked_events: u64,
    pub oldest_unacked_seconds: Option<u64>,
    pub last_ack_seq: u64,
    pub dropped_total: u64,
    pub send_accepted: u64,
    pub send_retry: u64,
    pub send_rejected: u64,
    pub replayed: u64,
}
fn read_records(path: &Path) -> std::io::Result<Vec<Record>> {
    let mut out = Vec::new();
    for line in BufReader::new(File::open(path)?)
        .lines()
        .map_while(Result::ok)
    {
        if let Ok(r) = serde_json::from_str(&line) {
            out.push(r)
        }
    }
    Ok(out)
}
fn spool_bytes(dir: &Path) -> std::io::Result<u64> {
    Ok(segments(dir)?
        .iter()
        .map(|p| fs::metadata(p).map(|m| m.len()).unwrap_or(0))
        .sum())
}
#[cfg(unix)]
fn disk_available(path: &Path) -> std::io::Result<u64> {
    use std::{ffi::CString, os::unix::ffi::OsStrExt};
    let p = CString::new(path.as_os_str().as_bytes()).map_err(std::io::Error::other)?;
    let mut stat = std::mem::MaybeUninit::<libc::statvfs>::uninit();
    if unsafe { libc::statvfs(p.as_ptr(), stat.as_mut_ptr()) } != 0 {
        return Err(std::io::Error::last_os_error());
    }
    let stat = unsafe { stat.assume_init() };
    Ok(u64::from(stat.f_bavail) * stat.f_frsize)
}
#[cfg(not(unix))]
fn disk_available(_path: &Path) -> std::io::Result<u64> {
    Ok(u64::MAX)
}
fn segments(dir: &Path) -> std::io::Result<Vec<PathBuf>> {
    let mut v = fs::read_dir(dir)?
        .flatten()
        .map(|e| e.path())
        .filter(|p| {
            p.file_name()
                .and_then(|x| x.to_str())
                .is_some_and(|s| s.starts_with("seg-") && s.ends_with(".jsonl"))
        })
        .collect::<Vec<_>>();
    v.sort();
    Ok(v)
}
fn persist_ack(dir: &Path, ack: &Ack) -> std::io::Result<()> {
    let tmp = dir.join("ack.json.tmp");
    let mut f = File::create(&tmp)?;
    f.write_all(&serde_json::to_vec(ack).map_err(std::io::Error::other)?)?;
    f.sync_all()?;
    fs::rename(tmp, dir.join("ack.json"))?;
    File::open(dir)?.sync_all()?;
    Ok(())
}
