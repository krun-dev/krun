use std::collections::HashMap;
use std::env;
use std::net::SocketAddr;
use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use async_trait::async_trait;
use pingora_core::server::ShutdownWatch;
use pingora_core::services::background::BackgroundService;
use tokio::task::JoinSet;
use tokio::time::{Instant, sleep};

use crate::rejection::{Rejection, Rejections};
use crate::{Group, count, group_limits, probe_http, try_reserve};

pub const TOKEN_HEADER: &str = "x-krun-lab-peer-token";
pub const TARGET_HEADER: &str = "x-krun-lab-target";
pub const HOP_HEADER: &str = "x-krun-lab-hop";
pub const LOAD_HEADER: &str = "x-krun-lab-load";
pub const COLD_MISS_HEADER: &str = "x-krun-lab-cold-miss";
pub const REJECT_HEADER: &str = "x-krun-lab-execution-reject";
pub const HEADROOM_HEADER: &str = "x-krun-lab-execution-headroom";

pub struct Router {
    pub name: String,
    pub token: String,
    peers: Vec<Arc<Peer>>,
    limits: [usize; 2],
    cooldown: Duration,
    use_capacity: bool,
    hint_ttl: Duration,
    pub health_interval: Duration,
    hosts: Mutex<HashMap<Arc<str>, Host>>,
    max_hosts: usize,
}

#[derive(Default)]
struct Host {
    known: u64,
    requests: usize,
}

pub struct Peer {
    name: String,
    pub address: SocketAddr,
    ready: AtomicBool,
    in_flight: [AtomicUsize; 2],
    status: Mutex<[PeerStatus; 2]>,
}

#[derive(Clone, Copy, Default)]
struct PeerStatus {
    blocked_until: Option<Instant>,
    headroom: Option<(Instant, usize)>,
}

impl Router {
    pub fn from_env(name: String, own_address: SocketAddr) -> Option<Self> {
        let peers = env::var("POOL_PEERS").ok()?;
        let token = env::var("POOL_PEER_TOKEN").expect("POOL_PEER_TOKEN required for forwarding");
        assert!(token.len() >= 16 && token.bytes().all(|byte| byte.is_ascii_graphic()));
        let mut configured = Vec::new();
        for entry in peers.split(',').filter(|entry| !entry.is_empty()) {
            let (peer_name, peer_addr) = entry.split_once('@').expect("peer must be NAME@IP:PORT");
            let address = peer_addr.parse().expect("invalid execution peer address");
            assert!(valid_name(peer_name) && peer_name != name && address != own_address);
            assert!(
                !configured
                    .iter()
                    .any(|peer: &Arc<Peer>| peer.name == peer_name || peer.address == address)
            );
            configured.push(Arc::new(Peer {
                name: peer_name.into(),
                address,
                ready: AtomicBool::new(false),
                in_flight: Default::default(),
                status: Mutex::default(),
            }));
        }
        assert!(configured.len() <= 64);
        let use_capacity = match env::var("POOL_PEER_SELECTION").as_deref() {
            Ok("capacity") => true,
            Ok("affinity") | Err(env::VarError::NotPresent) => false,
            _ => panic!("POOL_PEER_SELECTION must be affinity or capacity"),
        };
        Some(Self {
            name,
            token,
            peers: configured,
            limits: group_limits(
                "POOL_PEER_IN_FLIGHT",
                "POOL_PEER_ORDINARY_IN_FLIGHT",
                "POOL_PEER_COMPUTE_IN_FLIGHT",
                16,
            ),
            cooldown: Duration::from_millis(count("POOL_PEER_COOLDOWN_MS", 150, 1, 60000) as u64),
            use_capacity,
            hint_ttl: Duration::from_millis(count("POOL_PEER_HINT_TTL_MS", 150, 1, 60000) as u64),
            health_interval: Duration::from_millis(
                count("POOL_PEER_HEALTH_MS", 400, 50, 10000) as u64
            ),
            hosts: Mutex::default(),
            max_hosts: count("POOL_MAX_ROUTED_KEYS", 4096, 1, 100000),
        })
    }

    pub fn observe_request(&self, key: Arc<str>) {
        let mut hosts = self.hosts.lock().unwrap();
        if hosts.contains_key(&key) || hosts.len() < self.max_hosts {
            let host = hosts.entry(key).or_default();
            host.requests = host.requests.saturating_add(1);
        }
    }

    pub fn acquire(
        self: &Arc<Self>,
        key: Arc<str>,
        group: Group,
        tried: Option<SocketAddr>,
    ) -> Result<RemoteLease, Rejections> {
        if self.peers.is_empty() {
            return Err(Rejection::NoPeers.into());
        }
        let mut reasons = Rejections::default();
        let known = self
            .hosts
            .lock()
            .unwrap()
            .get(&key)
            .map_or(0, |host| host.known);
        let group = group.index();
        let limit = self.limits[group];
        let now = Instant::now();
        let mut candidates = Vec::with_capacity(self.peers.len());
        for (index, peer) in self.peers.iter().enumerate() {
            if Some(peer.address) == tried {
                reasons.insert(Rejection::PeerAlreadyTried);
                continue;
            }
            if !peer.ready.load(Ordering::Relaxed) {
                reasons.insert(Rejection::PeerNotReady);
                continue;
            }
            let status = peer.status.lock().unwrap()[group];
            let pending = peer.in_flight[group].load(Ordering::Relaxed);
            if status.blocked_until.is_some_and(|until| until > now) {
                reasons.insert(Rejection::PeerCooldown);
                continue;
            }
            if pending >= limit {
                reasons.insert(Rejection::PeerInFlight);
                continue;
            }
            let low_headroom = self.use_capacity
                && status.headroom.is_some_and(|(at, slots)| {
                    slots == 0 && now.saturating_duration_since(at) <= self.hint_ttl
                });
            let order = peer_order(
                known & (1 << index) != 0,
                low_headroom,
                if self.use_capacity { pending } else { 0 },
                peer_score(&key, &peer.name),
            );
            candidates.push((order, index, peer));
        }
        // Snapshot mutable inputs once so concurrent requests cannot change sort ordering.
        candidates.sort_unstable_by_key(|(order, _, _)| std::cmp::Reverse(*order));
        for (_, index, peer) in candidates {
            if !peer.ready.load(Ordering::Relaxed) {
                reasons.insert(Rejection::PeerNotReady);
                continue;
            }
            if peer.status.lock().unwrap()[group]
                .blocked_until
                .is_some_and(|until| until > now)
            {
                reasons.insert(Rejection::PeerCooldown);
                continue;
            }
            if !try_reserve(&peer.in_flight[group], limit) {
                reasons.insert(Rejection::PeerInFlight);
                continue;
            }
            return Ok(RemoteLease {
                router: self.clone(),
                peer: peer.clone(),
                key,
                index,
                group,
            });
        }
        Err(reasons)
    }

    pub fn metrics(&self, out: &mut String) {
        use std::fmt::Write as _;
        writeln!(
            out,
            "krun_execution_routed_keys {}",
            self.hosts.lock().unwrap().len()
        )
        .unwrap();
        for group in [Group::Ordinary, Group::Compute] {
            writeln!(
                out,
                "krun_execution_peer_in_flight_limit{{execution_group=\"{}\"}} {}",
                group.name(),
                self.limits[group.index()],
            )
            .unwrap();
        }
    }
}

fn peer_order(
    warm: bool,
    low_headroom: bool,
    pending: usize,
    stable_hash: u64,
) -> (bool, bool, std::cmp::Reverse<usize>, u64) {
    (!low_headroom, warm, std::cmp::Reverse(pending), stable_hash)
}

pub fn valid_name(name: &str) -> bool {
    !name.is_empty()
        && name.len() <= 128
        && name
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || matches!(b, b'.' | b'_' | b'-'))
}

fn peer_score(key: &str, peer: &str) -> u64 {
    let mut hash = 0xcbf29ce484222325u64;
    for byte in key.bytes().chain([0]).chain(peer.bytes()) {
        hash = (hash ^ u64::from(byte)).wrapping_mul(0x100000001b3);
    }
    // Avalanche short peer names, which otherwise rank similarly across hosts.
    hash ^= hash >> 33;
    hash = hash.wrapping_mul(0xff51afd7ed558ccd);
    hash ^= hash >> 33;
    hash.wrapping_mul(0xc4ceb9fe1a85ec53) ^ (hash >> 33)
}

pub struct RemoteLease {
    router: Arc<Router>,
    peer: Arc<Peer>,
    key: Arc<str>,
    index: usize,
    group: usize,
}

impl RemoteLease {
    pub fn address(&self) -> SocketAddr {
        self.peer.address
    }
    pub fn target(&self) -> &str {
        &self.peer.name
    }

    pub fn accepted(&self) {
        if let Some(host) = self.router.hosts.lock().unwrap().get_mut(&self.key) {
            host.known |= 1 << self.index;
        }
    }

    pub fn observe_headroom(&self, slots: usize) {
        self.peer.status.lock().unwrap()[self.group].headroom = Some((Instant::now(), slots));
    }

    pub fn rejected(&self) {
        self.peer.status.lock().unwrap()[self.group].blocked_until =
            Some(Instant::now() + self.router.cooldown);
    }
}

impl Drop for RemoteLease {
    fn drop(&mut self) {
        self.peer.in_flight[self.group].fetch_sub(1, Ordering::Relaxed);
    }
}

pub struct PeerHealth(pub Arc<Router>);

#[async_trait]
impl BackgroundService for PeerHealth {
    async fn start(&self, mut shutdown: ShutdownWatch) {
        let router = &self.0;
        while !*shutdown.borrow() {
            let mut checks = JoinSet::new();
            for peer in &router.peers {
                let peer = peer.clone();
                checks.spawn(async move {
                    let ready = probe_http(
                        peer.address, b"GET /__pool/ready HTTP/1.1\r\nHost: ready.test\r\nConnection: close\r\n\r\n",
                    ).await;
                    peer.ready.store(ready, Ordering::Relaxed);
                });
            }
            while checks.join_next().await.is_some() {}
            tokio::select! {
                _ = shutdown.changed() => break,
                _ = sleep(router.health_interval) => {},
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn capacity_scoring_spreads_warm_traffic_and_deprioritizes_busy_peers() {
        assert!(peer_order(true, false, 0, 1) > peer_order(true, false, 2, 100));
        assert!(peer_order(true, false, 2, 1) > peer_order(false, false, 0, 100));
        assert!(peer_order(false, false, 0, 1) > peer_order(true, true, 0, 100));
        assert!(peer_order(true, false, 0, 100) > peer_order(true, false, 0, 1));
    }
}
