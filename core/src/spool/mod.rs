//! Private append-only JSONL usage spool.
use crate::proxy::usage::UsageEvent;
use serde::{Deserialize, Serialize};
use std::{
    fs::{self, File, OpenOptions},
    io::{BufRead, BufReader, Write},
    path::{Path, PathBuf},
    sync::Mutex,
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
        Ok(Self {
            dir,
            max_bytes,
            segment_bytes,
            state: Mutex::new(State {
                ack,
                current,
                current_path,
                current_len,
                next_seq,
            }),
        })
    }
    pub fn append(&self, event: UsageEvent) -> std::io::Result<u64> {
        let mut s = self.state.lock().unwrap();
        let seq = s.next_seq;
        let mut bytes =
            serde_json::to_vec(&Record { seq, event }).map_err(std::io::Error::other)?;
        bytes.push(b'\n');
        if s.current_len > 0 && s.current_len + bytes.len() as u64 > self.segment_bytes {
            s.current.sync_all()?;
            s.current_path = self.dir.join(format!("seg-{:020}.jsonl", seq));
            s.current = OpenOptions::new()
                .create(true)
                .append(true)
                .read(true)
                .open(&s.current_path)?;
            s.current_len = 0;
        }
        s.current.write_all(&bytes)?;
        s.current.sync_data()?;
        s.current_len += bytes.len() as u64;
        s.next_seq += 1;
        self.enforce_capacity(&mut s)?;
        Ok(seq)
    }
    fn enforce_capacity(&self, s: &mut State) -> std::io::Result<()> {
        let mut total = segments(&self.dir)?
            .iter()
            .map(|p| fs::metadata(p).map(|m| m.len()).unwrap_or(0))
            .sum::<u64>();
        while total > self.max_bytes {
            let paths = segments(&self.dir)?;
            let Some(p) = paths.first() else { break };
            if p == &s.current_path {
                break;
            }
            let len = fs::metadata(p)?.len();
            let seq = p
                .file_stem()
                .and_then(|n| n.to_str())
                .and_then(|n| n.rsplit('-').next())
                .and_then(|n| n.parse::<u64>().ok())
                .unwrap_or(0);
            fs::remove_file(p)?;
            s.ack.dropped.push(seq);
            s.ack.processed_seq = s.ack.processed_seq.max(seq);
            total = total.saturating_sub(len);
        }
        persist_ack(&self.dir, &s.ack)?;
        Ok(())
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
        persist_ack(&self.dir, &s.ack)
    }
    pub fn unacked(&self) -> std::io::Result<usize> {
        Ok(self.pending(usize::MAX)?.len())
    }
    pub fn bytes(&self) -> std::io::Result<u64> {
        Ok(segments(&self.dir)?
            .iter()
            .map(|p| fs::metadata(p).map(|m| m.len()).unwrap_or(0))
            .sum())
    }
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
