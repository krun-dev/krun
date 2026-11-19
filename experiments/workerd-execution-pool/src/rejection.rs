use std::fmt::Write as _;
use std::sync::atomic::{AtomicUsize, Ordering};

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum Rejection {
    PodCpuHigh,
    PodCpuUnavailable,
    WorkerdNotReady,
    WorkerdCpuHigh,
    WorkerdCpuUnavailable,
    WorkerdInFlight,
    WorkerdLoaded,
    ColdLoadInFlight,
    ColdLoadMemory,
    ColdLoadMemoryUnavailable,
    PeerNotReady,
    PeerCooldown,
    PeerInFlight,
    PeerAlreadyTried,
    NoPeers,
}

impl Rejection {
    const ALL: [Self; 15] = [
        Self::PodCpuHigh,
        Self::PodCpuUnavailable,
        Self::WorkerdNotReady,
        Self::WorkerdCpuHigh,
        Self::WorkerdCpuUnavailable,
        Self::WorkerdInFlight,
        Self::WorkerdLoaded,
        Self::ColdLoadInFlight,
        Self::ColdLoadMemory,
        Self::ColdLoadMemoryUnavailable,
        Self::PeerNotReady,
        Self::PeerCooldown,
        Self::PeerInFlight,
        Self::PeerAlreadyTried,
        Self::NoPeers,
    ];

    fn name(self) -> &'static str {
        match self {
            Self::PodCpuHigh => "pod_cpu_high",
            Self::PodCpuUnavailable => "pod_cpu_unavailable",
            Self::WorkerdNotReady => "workerd_not_ready",
            Self::WorkerdCpuHigh => "workerd_cpu_high",
            Self::WorkerdCpuUnavailable => "workerd_cpu_unavailable",
            Self::WorkerdInFlight => "workerd_in_flight",
            Self::WorkerdLoaded => "workerd_loaded",
            Self::ColdLoadInFlight => "cold_load_in_flight",
            Self::ColdLoadMemory => "cold_load_memory",
            Self::ColdLoadMemoryUnavailable => "cold_load_memory_unavailable",
            Self::PeerNotReady => "peer_not_ready",
            Self::PeerCooldown => "peer_cooldown",
            Self::PeerInFlight => "peer_in_flight",
            Self::PeerAlreadyTried => "peer_already_tried",
            Self::NoPeers => "no_peers",
        }
    }

    fn bit(self) -> u16 {
        1 << self as usize
    }
}

#[derive(Clone, Copy, Default)]
pub struct Rejections(u16);

impl Rejections {
    pub fn insert(&mut self, reason: Rejection) {
        self.0 |= reason.bit();
    }

    pub fn extend(&mut self, other: Self) {
        self.0 |= other.0;
    }
}

impl From<Rejection> for Rejections {
    fn from(reason: Rejection) -> Self {
        Self(reason.bit())
    }
}

#[derive(Default)]
pub struct RejectionCounts([AtomicUsize; Rejection::ALL.len()]);

impl RejectionCounts {
    pub fn record(&self, reason: Rejection) {
        self.0[reason as usize].fetch_add(1, Ordering::Relaxed);
    }

    pub fn record_all(&self, rejected: Rejections) {
        for reason in Rejection::ALL {
            if rejected.0 & reason.bit() != 0 {
                self.record(reason);
            }
        }
    }

    pub fn metrics(&self, out: &mut String, metric: &str, labels: &str) {
        for reason in Rejection::ALL {
            let count = self.0[reason as usize].load(Ordering::Relaxed);
            writeln!(
                out,
                "{metric}{{{labels},reason=\"{}\"}} {count}",
                reason.name()
            )
            .unwrap();
        }
    }
}
