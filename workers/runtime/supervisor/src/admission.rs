use std::collections::HashMap;
use std::fs;
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::{Arc, Mutex, RwLock};
use std::time::Duration;

use async_trait::async_trait;
use pingora_core::server::ShutdownWatch;
use pingora_core::services::background::BackgroundService;
use tokio::time::{Instant, sleep};

use crate::{Pool, count, rejection::Rejection};

pub struct Policy {
    pub cpu_high: usize,
    pub cpu_resume: usize,
    cpu_budget: usize,
    cpu_reserve: usize,
    memory_budget: usize,
    memory_reserve: usize,
    pub sample_interval: Duration,
    pub max_loaded: usize,
    max_loading: usize,
    cold_load_estimate: usize,
    loading: AtomicUsize,
    resources: RwLock<Resources>,
}

impl Policy {
    pub fn from_env() -> Self {
        let cpu_high = count("POOL_CPU_HIGH_MCPU", 650, 1, 5000);
        let cpu_resume = count("POOL_CPU_RESUME_MCPU", cpu_high * 7 / 10, 0, cpu_high - 1);
        let cpu_budget = count("POOL_CPU_BUDGET_MCPU", 4000, 1, 256000);
        let cpu_reserve = count(
            "POOL_CPU_RESERVE_MCPU",
            500.min(cpu_budget - 1),
            0,
            cpu_budget - 1,
        );
        let memory_budget_mib = count("POOL_MEMORY_BUDGET_MIB", 8192, 1, 1024 * 1024);
        let memory_reserve_mib = count(
            "POOL_MEMORY_RESERVE_MIB",
            1639.min(memory_budget_mib - 1),
            0,
            memory_budget_mib - 1,
        );
        Self {
            cpu_high,
            cpu_resume,
            cpu_budget,
            cpu_reserve,
            memory_budget: memory_budget_mib * 1024 * 1024,
            memory_reserve: memory_reserve_mib * 1024 * 1024,
            sample_interval: Duration::from_millis(count("POOL_SAMPLE_MS", 100, 25, 5000) as u64),
            max_loaded: count("POOL_MAX_LOADED", 500, 1, 100000),
            max_loading: count("POOL_MAX_COLD_IN_FLIGHT", 4, 1, 1024),
            cold_load_estimate: count("POOL_COLD_LOAD_ESTIMATE_MIB", 16, 1, 65536) * 1024 * 1024,
            loading: AtomicUsize::new(0),
            resources: RwLock::default(),
        }
    }

    fn snapshot(&self) -> Resources {
        *self.resources.read().unwrap()
    }

    pub fn check_cpu(&self) -> Result<(), Rejection> {
        let sample = self.snapshot();
        let cpu = sample
            .cpu
            .filter(|_| sample.fresh(self.sample_interval))
            .ok_or(Rejection::PodCpuUnavailable)?;
        if cpu < sample.cpu_ceiling {
            Ok(())
        } else {
            Err(Rejection::PodCpuHigh)
        }
    }

    pub fn cold_permit(self: &Arc<Self>) -> Result<ColdPermit, Rejection> {
        self.reserve_cold()?;
        Ok(ColdPermit(Arc::clone(self)))
    }

    fn reserve_cold(&self) -> Result<(), Rejection> {
        let sample = self.snapshot();
        let memory = sample
            .memory
            .filter(|_| sample.fresh(self.sample_interval))
            .ok_or(Rejection::ColdLoadMemoryUnavailable)?;
        self.loading
            .fetch_update(Ordering::Relaxed, Ordering::Relaxed, |loading| {
                let projected =
                    memory.saturating_add((loading + 1).saturating_mul(self.cold_load_estimate));
                (loading < self.max_loading && projected <= sample.memory_ceiling)
                    .then_some(loading + 1)
            })
            .map(|_| ())
            .map_err(|loading| {
                if loading >= self.max_loading {
                    Rejection::ColdLoadInFlight
                } else {
                    Rejection::ColdLoadMemory
                }
            })
    }

    pub fn metrics(&self, out: &mut String) {
        use std::fmt::Write as _;
        let s = self.snapshot();
        if let Some(cpu) = s.cpu {
            writeln!(out, "krun_execution_pod_cpu_cores {}", cpu / 1000.0).unwrap();
        }
        if let Some(memory) = s.memory {
            writeln!(out, "krun_execution_pod_memory_bytes {memory}").unwrap();
        }
        writeln!(
            out,
            "krun_execution_pod_cpu_admission_ceiling_cores {}",
            s.cpu_ceiling / 1000.0
        )
        .unwrap();
        writeln!(
            out,
            "krun_execution_pod_cpu_reserved_cores {}",
            self.cpu_reserve as f64 / 1000.0
        )
        .unwrap();
        writeln!(
            out,
            "krun_execution_pod_memory_admission_ceiling_bytes {}",
            s.memory_ceiling
        )
        .unwrap();
        writeln!(
            out,
            "krun_execution_pod_cold_loads_in_flight {}",
            self.loading.load(Ordering::Relaxed)
        )
        .unwrap();
    }
}

#[derive(Clone, Copy, Default)]
struct Resources {
    at: Option<Instant>,
    cpu: Option<f64>,
    cpu_ceiling: f64,
    memory: Option<usize>,
    memory_ceiling: usize,
}

impl Resources {
    fn fresh(self, interval: Duration) -> bool {
        self.at.is_some_and(|at| at.elapsed() <= interval * 4)
    }
}

#[derive(Default)]
pub struct LoadLedger {
    entries: Mutex<HashMap<Arc<str>, LoadEntry>>,
}

#[derive(Default)]
struct LoadEntry {
    pending: usize,
    warm: bool,
}

impl LoadLedger {
    pub fn is_warm(&self, key: &str) -> bool {
        self.entries
            .lock()
            .unwrap()
            .get(key)
            .is_some_and(|entry| entry.warm)
    }

    pub fn reserve(
        self: &Arc<Self>,
        policy: &Arc<Policy>,
        key: Arc<str>,
    ) -> Result<LoadPermit, Rejection> {
        let mut entries = self.entries.lock().unwrap();
        let is_new = !entries.contains_key(key.as_ref());
        if is_new && entries.len() >= policy.max_loaded {
            return Err(Rejection::WorkerdLoaded);
        }
        let warm = entries.get(key.as_ref()).is_some_and(|entry| entry.warm);
        let loading = entries
            .get(key.as_ref())
            .is_some_and(|entry| entry.pending > 0);
        if !warm && !loading {
            policy.reserve_cold()?;
        }
        if !warm {
            entries.entry(key.clone()).or_default().pending += 1;
        }
        Ok(LoadPermit {
            ledger: self.clone(),
            policy: policy.clone(),
            key,
            may_load: !warm,
        })
    }

    pub fn loaded(&self) -> usize {
        self.entries
            .lock()
            .unwrap()
            .values()
            .filter(|entry| entry.warm)
            .count()
    }
}

pub struct LoadPermit {
    ledger: Arc<LoadLedger>,
    policy: Arc<Policy>,
    key: Arc<str>,
    pub may_load: bool,
}

impl LoadPermit {
    pub fn mark_warm(&self) {
        if let Some(entry) = self.ledger.entries.lock().unwrap().get_mut(&self.key) {
            entry.warm = true;
        }
    }

    pub fn forget(&self) {
        let mut entries = self.ledger.entries.lock().unwrap();
        if let Some(entry) = entries.get_mut(&self.key) {
            entry.warm = false;
            if entry.pending == 0 {
                entries.remove(&self.key);
            }
        }
    }
}

impl Drop for LoadPermit {
    fn drop(&mut self) {
        if !self.may_load {
            return;
        }
        let mut entries = self.ledger.entries.lock().unwrap();
        let entry = entries.get_mut(&self.key).unwrap();
        entry.pending -= 1;
        if entry.pending == 0 {
            self.policy.loading.fetch_sub(1, Ordering::Relaxed);
            if !entry.warm {
                entries.remove(&self.key);
            }
        }
    }
}

pub struct ColdPermit(Arc<Policy>);

impl Drop for ColdPermit {
    fn drop(&mut self) {
        self.0.loading.fetch_sub(1, Ordering::Relaxed);
    }
}

#[derive(Default)]
pub struct SlotCpu {
    last: Option<(Instant, u64)>,
    pub mcpu: Option<f64>,
    pub high: bool,
}

impl SlotCpu {
    fn update(&mut self, policy: &Policy, at: Instant, total_ns: Option<u64>) {
        self.mcpu = delta_mcpu(self.last, at, total_ns);
        self.last = total_ns.map(|value| (at, value));
        if let Some(mcpu) = self.mcpu {
            let threshold = if self.high {
                policy.cpu_resume
            } else {
                policy.cpu_high
            };
            self.high = mcpu >= threshold as f64;
        }
    }

    pub fn check(&self, interval: Duration) -> Result<(), Rejection> {
        if self.mcpu.is_none()
            || !self
                .last
                .is_some_and(|(at, _)| at.elapsed() <= interval * 4)
        {
            Err(Rejection::WorkerdCpuUnavailable)
        } else if self.high {
            Err(Rejection::WorkerdCpuHigh)
        } else {
            Ok(())
        }
    }
}

fn delta_mcpu(previous: Option<(Instant, u64)>, now: Instant, total: Option<u64>) -> Option<f64> {
    let (previous_at, previous_total) = previous?;
    let elapsed = now.duration_since(previous_at).as_nanos();
    let delta = total?.checked_sub(previous_total)?;
    (elapsed > 0).then(|| delta as f64 * 1000.0 / elapsed as f64)
}

fn number<T: std::str::FromStr>(file: impl AsRef<Path>) -> Option<T> {
    fs::read_to_string(file).ok()?.trim().parse().ok()
}

enum Cgroup {
    V1 { cpu: PathBuf, memory: PathBuf },
    V2(PathBuf),
}

impl Cgroup {
    fn detect() -> Option<Self> {
        let entries = fs::read_to_string("/proc/self/cgroup").ok()?;
        let root = Path::new("/sys/fs/cgroup");
        let controller = |name| {
            entries.lines().find_map(|line| {
                let (_, tail) = line.split_once(':')?;
                let (controllers, path) = tail.split_once(':')?;
                controllers
                    .split(',')
                    .any(|item| item == name)
                    .then_some(path.trim_start_matches('/'))
            })
        };
        let path = |base: PathBuf, relative, file| {
            let scoped = base.join(relative);
            if scoped.join(file).is_file() {
                scoped
            } else {
                base
            }
        };
        if let (Some(cpu), Some(memory)) = (controller("cpuacct"), controller("memory")) {
            return Some(Self::V1 {
                cpu: path(root.join("cpu,cpuacct"), cpu, "cpuacct.usage"),
                memory: path(root.join("memory"), memory, "memory.usage_in_bytes"),
            });
        }
        Some(Self::V2(path(root.into(), controller("")?, "cpu.stat")))
    }

    fn cpu(&self) -> Option<u64> {
        match self {
            Self::V1 { cpu, .. } => number(cpu.join("cpuacct.usage")),
            Self::V2(path) => {
                let stat = fs::read_to_string(path.join("cpu.stat")).ok()?;
                stat.lines()
                    .find_map(|line| line.strip_prefix("usage_usec "))?
                    .parse::<u64>()
                    .ok()?
                    .checked_mul(1000)
            }
        }
    }

    fn quota(&self) -> Option<f64> {
        let (quota, period) = match self {
            Self::V1 { cpu, .. } => (
                number(cpu.join("cpu.cfs_quota_us"))?,
                number(cpu.join("cpu.cfs_period_us"))?,
            ),
            Self::V2(path) => {
                let value = fs::read_to_string(path.join("cpu.max")).ok()?;
                let (quota, period) = value.trim().split_once(' ')?;
                (quota.parse::<u64>().ok()?, period.parse::<u64>().ok()?)
            }
        };
        (period > 0).then(|| quota as f64 / period as f64 * 1000.0)
    }

    fn memory(&self) -> Option<usize> {
        match self {
            Self::V1 { memory, .. } => number(memory.join("memory.usage_in_bytes")),
            Self::V2(path) => number(path.join("memory.current")),
        }
    }

    fn memory_limit(&self) -> Option<usize> {
        match self {
            Self::V1 { memory, .. } => number(memory.join("memory.limit_in_bytes")),
            Self::V2(path) => number(path.join("memory.max")),
        }
    }
}

fn thread_cpu_ns(pid: u32, ticks_per_second: u64) -> Option<u64> {
    let stat = fs::read_to_string(format!("/proc/{pid}/task/{pid}/stat")).ok()?;
    let (_, fields) = stat.rsplit_once(')')?;
    let mut fields = fields.split_whitespace().skip(11);
    let user = fields.next()?.parse::<u64>().ok()?;
    let system = fields.next()?.parse::<u64>().ok()?;
    user.checked_add(system)?
        .checked_mul(1_000_000_000)?
        .checked_div(ticks_per_second)
}

pub struct Sampler {
    pub pool: Arc<Pool>,
    pub policy: Arc<Policy>,
}

#[async_trait]
impl BackgroundService for Sampler {
    async fn start(&self, mut shutdown: ShutdownWatch) {
        let Some(cgroup) = Cgroup::detect() else {
            eprintln!("pool admission could not find a cgroup");
            return;
        };
        let ticks_per_second = unsafe { libc::sysconf(libc::_SC_CLK_TCK) }.max(0) as u64;
        let mut last = None;
        let cpu_budget = self.policy.cpu_budget as f64;
        let cpu_ceiling = (cgroup.quota().unwrap_or(cpu_budget).min(cpu_budget)
            - self.policy.cpu_reserve as f64)
            .max(0.0);
        let memory_ceiling = cgroup
            .memory_limit()
            .unwrap_or(self.policy.memory_budget)
            .min(self.policy.memory_budget)
            .saturating_sub(self.policy.memory_reserve);
        while !*shutdown.borrow() {
            let total = cgroup.cpu();
            let at = Instant::now();
            *self.policy.resources.write().unwrap() = Resources {
                at: Some(at),
                cpu: delta_mcpu(last, at, total),
                cpu_ceiling,
                memory: cgroup.memory(),
                memory_ceiling,
            };
            last = total.map(|value| (at, value));
            for slot in &self.pool.slots {
                if let Some(endpoint) = slot.endpoint.read().unwrap().clone() {
                    endpoint.cpu.write().unwrap().update(
                        &self.policy,
                        at,
                        endpoint
                            .pid
                            .and_then(|pid| thread_cpu_ns(pid, ticks_per_second)),
                    );
                }
            }
            tokio::select! {
                _ = shutdown.changed() => break,
                _ = sleep(self.policy.sample_interval) => {},
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn policy() -> Arc<Policy> {
        let policy = Policy {
            cpu_high: 650,
            cpu_resume: 450,
            cpu_budget: 4000,
            cpu_reserve: 500,
            memory_budget: 100,
            memory_reserve: 10,
            sample_interval: Duration::from_millis(100),
            max_loaded: 2,
            max_loading: 2,
            cold_load_estimate: 20,
            loading: AtomicUsize::new(0),
            resources: RwLock::new(Resources {
                at: Some(Instant::now()),
                memory: Some(65),
                memory_ceiling: 90,
                ..Resources::default()
            }),
        };
        Arc::new(policy)
    }

    #[test]
    fn coalesced_load_uses_one_pod_reservation() {
        let policy = policy();
        let first = Arc::new(LoadLedger::default());
        let second = Arc::new(LoadLedger::default());
        let load = first.reserve(&policy, "same-version".into()).unwrap();
        let coalesced = first.reserve(&policy, "same-version".into()).unwrap();
        assert_eq!(
            second.reserve(&policy, "same-version".into()).err(),
            Some(Rejection::ColdLoadMemory),
        );
        assert_eq!(policy.loading.load(Ordering::Relaxed), 1);
        load.mark_warm();
        drop(load);
        drop(coalesced);
        let warm = first.reserve(&policy, "same-version".into()).unwrap();
        assert!(!warm.may_load);
        warm.forget();
        assert_eq!(first.loaded(), 0);
        assert!(second.reserve(&policy, "same-version".into()).is_ok());
    }

    #[test]
    fn thread_cpu_requires_the_lower_threshold_to_resume() {
        let policy = policy();
        let at = Instant::now();
        let interval = policy.sample_interval;
        let mut cpu = SlotCpu::default();
        cpu.update(&policy, at, Some(0));
        cpu.update(&policy, at + interval, Some(80_000_000));
        assert!(cpu.high);
        cpu.update(&policy, at + interval * 2, Some(140_000_000));
        assert!(cpu.high);
        cpu.update(&policy, at + interval * 3, Some(180_000_000));
        assert!(!cpu.high);
    }
}
