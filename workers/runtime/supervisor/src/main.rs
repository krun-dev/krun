mod admission;
mod config_agent;
mod rejection;
mod routing;
mod serving;

use std::collections::HashSet;
use std::env;
use std::io;
use std::net::{SocketAddr, TcpListener};
use std::os::fd::AsRawFd;
use std::os::unix::process::CommandExt;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::{Arc, RwLock};
use std::time::Duration;

use async_trait::async_trait;
use pingora_core::server::configuration::ServerConf;
use pingora_core::server::{Server, ShutdownWatch};
use pingora_core::services::background::{BackgroundService, background_service};
use pingora_core::upstreams::peer::HttpPeer;
use pingora_core::{Error, ErrorSource, ErrorType, Result};
use pingora_proxy::{FailToProxy, ProxyHttp, Session, http_proxy_service};
use subtle::ConstantTimeEq;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::TcpStream;
use tokio::time::{Instant, sleep, timeout};

use admission::{LoadLedger, LoadPermit, Policy, Sampler, SlotCpu};
use rejection::{Rejection, RejectionCounts, Rejections};
use routing::{
    COLD_MISS_HEADER, HEADROOM_HEADER, HOP_HEADER, LOAD_HEADER, PeerHealth, REJECT_HEADER,
    RemoteLease, Router, TARGET_HEADER, TOKEN_HEADER,
};

#[derive(Clone, Copy)]
enum Group {
    Ordinary,
    Compute,
}
impl Group {
    fn name(self) -> &'static str {
        match self {
            Self::Ordinary => "ordinary",
            Self::Compute => "compute",
        }
    }

    fn index(self) -> usize {
        match self {
            Self::Ordinary => 0,
            Self::Compute => 1,
        }
    }
}

#[derive(Clone, Copy, PartialEq, Eq)]
enum Traffic {
    Short,
    Stream,
}

impl Traffic {
    fn index(self) -> usize {
        self as usize
    }

    fn routing_index(self, group: Group) -> usize {
        self.index() * 2 + group.index()
    }
}

struct StreamPermit(Arc<AtomicUsize>);

impl StreamPermit {
    fn acquire(counter: &Arc<AtomicUsize>, limit: usize) -> Option<Self> {
        try_reserve(counter, limit).then(|| Self(Arc::clone(counter)))
    }
}

impl Drop for StreamPermit {
    fn drop(&mut self) {
        self.0.fetch_sub(1, Ordering::Relaxed);
    }
}

struct Streams {
    slot_limit: usize,
    pod_limit: usize,
    ingress_limit: usize,
    ingress: Arc<AtomicUsize>,
    execution: Arc<AtomicUsize>,
    sse_paths: HashSet<String>,
}

impl Streams {
    fn from_env() -> Self {
        Self {
            slot_limit: count("POOL_STREAM_SLOT_LIMIT", 300, 1, 4096),
            pod_limit: count("POOL_STREAM_POD_LIMIT", 600, 1, 32768),
            ingress_limit: count("POOL_STREAM_INGRESS_LIMIT", 600, 1, 32768),
            ingress: Arc::default(),
            execution: Arc::default(),
            sse_paths: env::var("POOL_SSE_PATHS")
                .unwrap_or_default()
                .split(',')
                .filter(|path| !path.is_empty())
                .map(str::to_owned)
                .collect(),
        }
    }

    fn matches(&self, request: &pingora_http::RequestHeader) -> bool {
        self.sse_paths.contains(request.uri.path())
            || request
                .headers
                .get("upgrade")
                .is_some_and(|v| v.as_bytes().eq_ignore_ascii_case(b"websocket"))
            || request
                .headers
                .get_all("accept")
                .iter()
                .filter_map(|v| v.to_str().ok())
                .any(|accept| {
                    accept.split(',').any(|range| {
                        range
                            .split(';')
                            .next()
                            .unwrap_or_default()
                            .trim()
                            .eq_ignore_ascii_case("text/event-stream")
                    })
                })
    }
}

fn try_reserve(counter: &AtomicUsize, limit: usize) -> bool {
    counter
        .fetch_update(Ordering::Relaxed, Ordering::Relaxed, |count| {
            (count < limit).then_some(count + 1)
        })
        .is_ok()
}

fn group_limits(shared: &str, ordinary: &str, compute: &str, fallback: usize) -> [usize; 2] {
    let default = count(shared, fallback, 1, 4096);
    [
        count(ordinary, default, 1, 4096),
        count(compute, default, 1, 4096),
    ]
}

struct Endpoint {
    address: SocketAddr,
    pid: Option<u32>,
    serving: Option<serving::ChildSockets>,
    in_flight: [AtomicUsize; 2],
    cpu: RwLock<SlotCpu>,
    ledger: Arc<LoadLedger>,
}

impl Endpoint {
    fn new(address: SocketAddr, pid: Option<u32>) -> Self {
        Self {
            address,
            pid,
            serving: None,
            in_flight: Default::default(),
            cpu: RwLock::default(),
            ledger: Arc::default(),
        }
    }
}

struct Lease {
    endpoint: Arc<Endpoint>,
    slot: Arc<Slot>,
    load: Option<LoadPermit>,
    traffic: Traffic,
    _stream: Option<StreamPermit>,
}

impl Drop for Lease {
    fn drop(&mut self) {
        self.endpoint.in_flight[self.traffic.index()].fetch_sub(1, Ordering::Relaxed);
    }
}

struct Slot {
    name: String,
    group: Group,
    endpoint: RwLock<Option<Arc<Endpoint>>>,
    requests: AtomicUsize,
    cold: AtomicUsize,
    busy: AtomicUsize,
    rejected_by_reason: RejectionCounts,
}

impl Slot {
    fn reject(&self, reason: Rejection, reasons: &mut Rejections) {
        self.busy.fetch_add(1, Ordering::Relaxed);
        self.rejected_by_reason.record(reason);
        reasons.insert(reason);
    }
}

struct Pool {
    slots: Vec<Arc<Slot>>,
    ordinary: usize,
    serving: Option<serving::Settings>,
    in_flight_limits: [usize; 2],
    streams: Option<Streams>,
    policy: Option<Arc<Policy>>,
    local: [AtomicUsize; 4],
    forwarded: [AtomicUsize; 4],
    refused: [AtomicUsize; 4],
    execution_refused: [AtomicUsize; 4],
    ingress_rejections: [RejectionCounts; 4],
    execution_rejections: [RejectionCounts; 4],
    retried: [AtomicUsize; 4],
}

impl Pool {
    fn group_slots(&self, group: Group) -> &[Arc<Slot>] {
        match group {
            Group::Compute if self.slots.len() > self.ordinary => &self.slots[self.ordinary..],
            _ => &self.slots[..self.ordinary],
        }
    }

    fn slot_limit(&self, group: Group, traffic: Traffic) -> usize {
        match traffic {
            Traffic::Short => self.in_flight_limits[group.index()],
            Traffic::Stream => self.streams.as_ref().expect("stream settings").slot_limit,
        }
    }

    fn acquire(
        &self,
        host: &str,
        key: &Arc<str>,
        group: Group,
        traffic: Traffic,
    ) -> std::result::Result<Lease, Rejections> {
        if let Some(policy) = &self.policy {
            policy.check_cpu()?;
        }
        let mut stream = if traffic == Traffic::Stream {
            let streams = self.streams.as_ref().expect("stream settings");
            Some(
                StreamPermit::acquire(&streams.execution, streams.pod_limit)
                    .ok_or(Rejection::PodStreamInFlight)?,
            )
        } else {
            None
        };
        let mut reasons = Rejections::default();
        let slots = self.group_slots(group);
        let limit = self.slot_limit(slots[0].group, traffic);
        let hash = host.bytes().fold(0x811c9dc5u32, |hash, byte| {
            (hash ^ u32::from(byte)).wrapping_mul(0x01000193)
        });
        let first = hash as usize % slots.len();
        let mut candidates = Vec::with_capacity(slots.len());
        for offset in 0..slots.len() {
            let slot = &slots[(first + offset) % slots.len()];
            if let Some(endpoint) = slot.endpoint.read().unwrap().clone() {
                candidates.push((slot, endpoint));
            } else {
                reasons.insert(Rejection::WorkerdNotReady);
            }
        }
        if self.policy.is_some() && self.serving.is_none() {
            candidates.sort_by_key(|(_, endpoint)| !endpoint.ledger.is_warm(key));
        }
        for (slot, endpoint) in candidates {
            if let Some(policy) = &self.policy
                && let Err(reason) = endpoint.cpu.read().unwrap().check(policy.sample_interval)
            {
                slot.reject(reason, &mut reasons);
                continue;
            }
            if !try_reserve(&endpoint.in_flight[traffic.index()], limit) {
                slot.reject(Rejection::WorkerdInFlight, &mut reasons);
                continue;
            }
            let mut lease = Lease {
                endpoint,
                slot: slot.clone(),
                load: None,
                traffic,
                _stream: stream.take(),
            };
            if let Some(policy) = &self.policy
                && self.serving.is_none()
            {
                match lease.endpoint.ledger.reserve(policy, key.clone()) {
                    Ok(permit) => lease.load = Some(permit),
                    Err(reason) => {
                        slot.reject(reason, &mut reasons);
                        continue;
                    }
                }
            }
            slot.requests.fetch_add(1, Ordering::Relaxed);
            return Ok(lease);
        }
        Err(reasons)
    }

    fn headroom_after_response(
        &self,
        group: Group,
        traffic: Traffic,
        completed: &Arc<Endpoint>,
    ) -> usize {
        if self
            .policy
            .as_ref()
            .is_some_and(|policy| policy.check_cpu().is_err())
        {
            return 0;
        }
        let slots = self.group_slots(group);
        let limit = self.slot_limit(slots[0].group, traffic);
        let headroom = slots
            .iter()
            .filter_map(|slot| slot.endpoint.read().unwrap().clone())
            .filter(|endpoint| {
                self.policy.as_ref().is_none_or(|policy| {
                    endpoint
                        .cpu
                        .read()
                        .unwrap()
                        .check(policy.sample_interval)
                        .is_ok()
                })
            })
            .map(|endpoint| {
                let in_flight = endpoint.in_flight[traffic.index()].load(Ordering::Relaxed);
                let remaining = in_flight.saturating_sub(usize::from(
                    traffic == Traffic::Short && Arc::ptr_eq(completed, &endpoint),
                ));
                limit.saturating_sub(remaining)
            })
            .sum::<usize>();
        if traffic == Traffic::Stream {
            let streams = self.streams.as_ref().expect("stream settings");
            return headroom.min(
                streams
                    .pod_limit
                    .saturating_sub(streams.execution.load(Ordering::Relaxed)),
            );
        }
        headroom
    }

    fn ready(&self) -> bool {
        if self.serving.is_some() {
            let policy = self.policy.as_ref().expect("serving admission");
            return !matches!(policy.check_cpu(), Err(Rejection::PodCpuUnavailable))
                && self.slots[..self.ordinary].iter().any(|slot| {
                    slot.endpoint
                        .read()
                        .unwrap()
                        .as_ref()
                        .is_some_and(|endpoint| {
                            !matches!(
                                endpoint.cpu.read().unwrap().check(policy.sample_interval),
                                Err(Rejection::WorkerdCpuUnavailable)
                            )
                        })
                });
        }
        self.slots
            .iter()
            .all(|slot| slot.endpoint.read().unwrap().is_some())
    }

    fn metrics(&self) -> String {
        use std::fmt::Write as _;
        let mut out = String::new();
        for slot in &self.slots {
            let labels = format!(
                "{{workerd_slot=\"{}\",execution_group=\"{}\"}}",
                slot.name,
                slot.group.name()
            );
            let endpoint = slot.endpoint.read().unwrap();
            writeln!(
                out,
                "krun_execution_slot_in_flight_limit{labels} {}",
                self.in_flight_limits[slot.group.index()],
            )
            .unwrap();
            writeln!(
                out,
                "krun_execution_slot_ready{labels} {}",
                u8::from(endpoint.is_some())
            )
            .unwrap();
            if let Some(policy) = &self.policy {
                writeln!(
                    out,
                    "krun_execution_slot_cpu_high_watermark_cores{labels} {}",
                    policy.cpu_high as f64 / 1000.0
                )
                .unwrap();
                writeln!(
                    out,
                    "krun_execution_slot_cpu_resume_watermark_cores{labels} {}",
                    policy.cpu_resume as f64 / 1000.0
                )
                .unwrap();
            }
            if let Some(endpoint) = endpoint.as_ref() {
                let cpu = endpoint.cpu.read().unwrap();
                if let Some(mcpu) = cpu.mcpu {
                    writeln!(
                        out,
                        "krun_execution_slot_cpu_cores{labels} {}",
                        mcpu / 1000.0
                    )
                    .unwrap();
                    writeln!(
                        out,
                        "krun_execution_slot_cpu_high{labels} {}",
                        u8::from(cpu.high)
                    )
                    .unwrap();
                }
                writeln!(
                    out,
                    "krun_execution_slot_in_flight{labels} {}",
                    endpoint.in_flight[Traffic::Short.index()].load(Ordering::Relaxed)
                )
                .unwrap();
                if let Some(streams) = &self.streams {
                    writeln!(
                        out,
                        "krun_execution_slot_stream_in_flight{labels} {}",
                        endpoint.in_flight[Traffic::Stream.index()].load(Ordering::Relaxed)
                    )
                    .unwrap();
                    writeln!(
                        out,
                        "krun_execution_slot_stream_in_flight_limit{labels} {}",
                        streams.slot_limit
                    )
                    .unwrap();
                }
                if self.serving.is_none() {
                    writeln!(
                        out,
                        "krun_execution_slot_loaded{labels} {}",
                        endpoint.ledger.loaded()
                    )
                    .unwrap();
                }
            }
            writeln!(
                out,
                "krun_execution_slot_requests_total{labels} {}",
                slot.requests.load(Ordering::Relaxed)
            )
            .unwrap();
            writeln!(
                out,
                "krun_execution_slot_cold_loads_total{labels} {}",
                slot.cold.load(Ordering::Relaxed)
            )
            .unwrap();
            writeln!(
                out,
                "krun_execution_slot_admission_checks_rejected_total{labels} {}",
                slot.busy.load(Ordering::Relaxed)
            )
            .unwrap();
            slot.rejected_by_reason.metrics(
                &mut out,
                "krun_execution_slot_admission_rejections_total",
                &labels[1..labels.len() - 1],
            );
        }
        if let Some(streams) = &self.streams {
            writeln!(
                out,
                "krun_execution_stream_ingress_in_flight {}",
                streams.ingress.load(Ordering::Relaxed)
            )
            .unwrap();
            writeln!(
                out,
                "krun_execution_stream_execution_in_flight {}",
                streams.execution.load(Ordering::Relaxed)
            )
            .unwrap();
            writeln!(
                out,
                "krun_execution_stream_ingress_in_flight_limit {}",
                streams.ingress_limit
            )
            .unwrap();
            writeln!(
                out,
                "krun_execution_stream_execution_in_flight_limit {}",
                streams.pod_limit
            )
            .unwrap();
        }
        for (traffic, group) in [Traffic::Short, Traffic::Stream]
            .into_iter()
            .filter(|traffic| *traffic == Traffic::Short || self.streams.is_some())
            .flat_map(|traffic| [Group::Ordinary, Group::Compute].map(|group| (traffic, group)))
        {
            let i = traffic.routing_index(group);
            let class = if traffic == Traffic::Stream {
                ",traffic_class=\"stream\""
            } else {
                ""
            };
            for (outcome, counter) in [
                ("local", &self.local[i]),
                ("forwarded", &self.forwarded[i]),
                ("rejected", &self.refused[i]),
                ("execution_rejected", &self.execution_refused[i]),
                ("retry", &self.retried[i]),
            ] {
                writeln!(out, "krun_execution_routes_total{{execution_group=\"{}\",outcome=\"{outcome}\"{class}}} {}", group.name(), counter.load(Ordering::Relaxed)).unwrap();
            }
            for (listener, counts) in [
                ("ingress", &self.ingress_rejections[i]),
                ("execution", &self.execution_rejections[i]),
            ] {
                counts.metrics(
                    &mut out,
                    "krun_execution_route_admission_rejections_total",
                    &format!(
                        "execution_group=\"{}\",listener=\"{listener}\"{class}",
                        group.name()
                    ),
                );
            }
        }
        if let Some(policy) = &self.policy {
            policy.metrics(&mut out);
        }
        out
    }
}

#[derive(Clone, Copy, PartialEq, Eq)]
enum Listener {
    Ingress,
    Execution,
    Management,
    Coordinator,
    Metrics,
}

#[derive(Clone)]
struct Proxy {
    pool: Arc<Pool>,
    router: Option<Arc<Router>>,
    listener: Listener,
    bench_pod_name: Option<String>,
    bench_delay_file: Option<PathBuf>,
    retry_budget: Duration,
    config_agent: Option<Arc<config_agent::Agent>>,
}

enum Target {
    Local(Lease),
    Remote(RemoteLease),
    Management,
}

struct RequestContext {
    target: Option<Target>,
    refused: bool,
    tried_remote: Option<SocketAddr>,
    local_cold_refused: bool,
    group: Group,
    traffic: Traffic,
    stream_ingress: Option<StreamPermit>,
    started: Instant,
    config_bundle: Option<Arc<config_agent::Bundle>>,
}

async fn respond(session: &mut Session, status: u16, body: String, rejected: bool) -> Result<()> {
    let mut header = pingora_http::ResponseHeader::build(status, None)?;
    header.insert_header("Content-Length", body.len().to_string())?;
    if rejected {
        header.insert_header(REJECT_HEADER, "capacity")?;
    }
    let empty = body.is_empty();
    if !empty {
        header.insert_header("Content-Type", "text/plain; version=0.0.4")?;
    }
    session
        .write_response_header(Box::new(header), empty)
        .await?;
    if !empty {
        session.write_response_body(Some(body.into()), true).await?;
    }
    Ok(())
}

#[async_trait]
impl ProxyHttp for Proxy {
    type CTX = RequestContext;

    fn new_ctx(&self) -> Self::CTX {
        RequestContext {
            target: None,
            refused: false,
            tried_remote: None,
            local_cold_refused: false,
            group: Group::Ordinary,
            traffic: Traffic::Short,
            stream_ingress: None,
            started: Instant::now(),
            config_bundle: None,
        }
    }

    async fn request_filter(&self, session: &mut Session, _ctx: &mut Self::CTX) -> Result<bool> {
        if self.listener == Listener::Metrics {
            let (status, body) = if session.req_header().uri.path() == "/metrics" {
                let mut metrics = self.pool.metrics();
                if let Some(agent) = &self.config_agent {
                    metrics.push_str(&agent.metrics());
                }
                if let Some(router) = &self.router {
                    router.metrics(&mut metrics);
                }
                if self.pool.serving.is_some() {
                    metrics.push_str(&serving::metrics(&self.pool).await);
                }
                (200, metrics)
            } else {
                (404, String::new())
            };
            respond(session, status, body, false).await?;
            return Ok(true);
        }
        if self.listener == Listener::Coordinator {
            if session.req_header().uri.path().starts_with("/config/") {
                if let (Some(agent), Some(settings)) = (&self.config_agent, &self.pool.serving) {
                    return config_agent::coordinator(session, agent, &settings.management_token)
                        .await;
                }
                respond(session, 503, String::new(), false).await?;
                return Ok(true);
            }
            match session.req_header().uri.path() {
                "/ack" => serving::grant_ack(session, &self.pool).await?,
                "/cold/reserve" | "/cold/release" => {
                    serving::cold_load(session, &self.pool).await?
                }
                _ => respond(session, 404, String::new(), false).await?,
            }
            return Ok(true);
        }
        if self.listener == Listener::Management {
            match session.req_header().uri.path() {
                // Authenticate the stream before config bootstrap or slot readiness.
                "/config-session" => {
                    if let (Some(agent), Some(settings)) = (&self.config_agent, &self.pool.serving)
                    {
                        return config_agent::coordinator(
                            session,
                            agent,
                            &settings.management_token,
                        )
                        .await;
                    }
                    respond(session, 503, String::new(), false).await?;
                    return Ok(true);
                }
                "/ready" => {
                    serving::readiness(session, &self.pool).await?;
                    return Ok(true);
                }
                "/healthz" => {
                    let status = if self.pool.ready() { 200 } else { 503 };
                    respond(session, status, String::new(), false).await?;
                    return Ok(true);
                }
                _ => {}
            }
        }
        if let Some(path) = &self.bench_delay_file {
            let delay_ms = std::fs::read_to_string(path)
                .ok()
                .and_then(|value| value.trim().parse::<u64>().ok())
                .unwrap_or(0);
            if delay_ms > 0 {
                sleep(Duration::from_millis(delay_ms.min(5000))).await;
            }
        }
        if session.req_header().uri.path() == "/__pool/ready"
            && (self.pool.serving.is_none() || self.listener == Listener::Execution)
        {
            let status = if self.pool.ready() { 200 } else { 503 };
            respond(session, status, String::new(), false).await?;
            return Ok(true);
        }
        if self.listener == Listener::Execution {
            let router = self
                .router
                .as_ref()
                .expect("execution listener requires peers");
            let headers = &session.req_header().headers;
            let token_ok = headers
                .get(TOKEN_HEADER)
                .is_some_and(|value| bool::from(value.as_bytes().ct_eq(router.token.as_bytes())));
            let valid = token_ok
                && headers
                    .get(TARGET_HEADER)
                    .is_some_and(|value| value.as_bytes() == router.name.as_bytes())
                && headers.get(HOP_HEADER).is_some_and(|value| value == "1");
            if !valid {
                respond(session, 403, String::new(), false).await?;
                return Ok(true);
            }
        }
        Ok(false)
    }

    async fn upstream_request_filter(
        &self,
        _session: &mut Session,
        request: &mut pingora_http::RequestHeader,
        ctx: &mut Self::CTX,
    ) -> Result<()> {
        for field in [
            TOKEN_HEADER,
            TARGET_HEADER,
            HOP_HEADER,
            LOAD_HEADER,
            REJECT_HEADER,
            COLD_MISS_HEADER,
            HEADROOM_HEADER,
            config_agent::CONFIG_HEADER,
        ] {
            request.remove_header(field);
        }
        match ctx.target.as_ref().expect("upstream selected") {
            Target::Local(lease) => {
                if let Some(agent) = &self.config_agent {
                    let host = request
                        .headers
                        .get("host")
                        .and_then(|v| v.to_str().ok())
                        .unwrap_or_default()
                        .split(':')
                        .next()
                        .unwrap_or_default()
                        .to_ascii_lowercase();
                    if agent.is_static_host(&host) {
                        agent.head().map_err(|status| {
                            Error::explain(
                                ErrorType::HTTPStatus(status),
                                "configuration unavailable",
                            )
                        })?;
                    } else {
                        let bundle =
                            agent
                                .resolve(&host, request.uri.path())
                                .await
                                .map_err(|status| {
                                    Error::explain(
                                        ErrorType::HTTPStatus(status),
                                        "configuration unavailable",
                                    )
                                })?;
                        request.insert_header(config_agent::CONFIG_HEADER, &bundle.handle)?;
                        ctx.config_bundle = Some(bundle);
                    }
                }
                if let Some(permit) = &lease.load {
                    request.insert_header(LOAD_HEADER, if permit.may_load { "1" } else { "0" })?;
                }
            }
            Target::Remote(lease) => {
                request.insert_header(TOKEN_HEADER, &self.router.as_ref().unwrap().token)?;
                request.insert_header(TARGET_HEADER, lease.target())?;
                request.insert_header(HOP_HEADER, "1")?;
            }
            Target::Management => {}
        }
        Ok(())
    }

    async fn response_filter(
        &self,
        _session: &mut Session,
        response: &mut pingora_http::ResponseHeader,
        ctx: &mut Self::CTX,
    ) -> Result<()> {
        let headroom = response.remove_header(HEADROOM_HEADER);
        let accepted = response.status.is_success() || response.status.as_u16() == 101;
        match ctx.target.as_ref() {
            Some(Target::Local(lease)) => {
                response.remove_header(REJECT_HEADER);
                if response.remove_header(COLD_MISS_HEADER).is_some() {
                    if let Some(permit) = &lease.load {
                        permit.forget();
                    }
                    if self.listener == Listener::Execution {
                        response.insert_header(REJECT_HEADER, "capacity")?;
                    }
                } else if response.status.is_success()
                    && response.headers.contains_key("x-experiment-instance")
                {
                    if let Some(permit) = &lease.load {
                        permit.mark_warm();
                    }
                    if response
                        .headers
                        .get("x-experiment-cold")
                        .is_some_and(|value| value == "1")
                    {
                        lease.slot.cold.fetch_add(1, Ordering::Relaxed);
                    }
                }
                if self.pool.serving.is_none() {
                    response.insert_header("x-experiment-workerd-slot", &lease.slot.name)?;
                    if let Some(name) = &self.bench_pod_name {
                        response.insert_header("x-experiment-pod", name)?;
                    }
                }
                if self.listener == Listener::Execution && accepted {
                    let slots =
                        self.pool
                            .headroom_after_response(ctx.group, ctx.traffic, &lease.endpoint);
                    response.insert_header(HEADROOM_HEADER, slots.to_string())?;
                }
            }
            Some(Target::Remote(lease)) => {
                if response.remove_header(REJECT_HEADER).is_some() {
                    lease.rejected();
                } else if accepted {
                    lease.accepted();
                    if let Some(slots) = headroom
                        .as_ref()
                        .and_then(|value| value.to_str().ok())
                        .and_then(|value| value.parse().ok())
                    {
                        lease.observe_headroom(slots);
                    }
                }
            }
            Some(Target::Management) | None => {}
        }
        Ok(())
    }

    async fn upstream_response_filter(
        &self,
        session: &mut Session,
        response: &mut pingora_http::ResponseHeader,
        ctx: &mut Self::CTX,
    ) -> Result<()> {
        if self.listener != Listener::Ingress
            || ctx.tried_remote.is_some()
            || ctx.local_cold_refused
            || ctx.started.elapsed() >= self.retry_budget
            || response.status.as_u16() != 503
        {
            return Ok(());
        }
        let request = session.req_header();
        if !matches!(request.method.as_str(), "GET" | "HEAD")
            || request.headers.contains_key("upgrade")
            || request.headers.contains_key("transfer-encoding")
            || request
                .headers
                .get("content-length")
                .is_some_and(|value| value != "0")
        {
            return Ok(());
        }
        match ctx.target.as_ref() {
            Some(Target::Remote(lease))
                if response
                    .headers
                    .get(REJECT_HEADER)
                    .is_some_and(|value| value == "capacity") =>
            {
                lease.rejected();
                ctx.tried_remote = Some(lease.address());
            }
            Some(Target::Local(_))
                if self.pool.serving.is_some()
                    && response.headers.contains_key(COLD_MISS_HEADER) =>
            {
                ctx.local_cold_refused = true;
            }
            _ => return Ok(()),
        }
        ctx.target = None;
        self.pool.retried[ctx.traffic.routing_index(ctx.group)].fetch_add(1, Ordering::Relaxed);
        let mut error = Error::explain(ErrorType::HTTPStatus(503), "confirmed execution refusal");
        error.set_retry(true);
        Err(error)
    }

    async fn logging(&self, _session: &mut Session, error: Option<&Error>, ctx: &mut Self::CTX) {
        if error.is_some()
            && let Some(Target::Remote(lease)) = &ctx.target
        {
            lease.rejected();
        }
    }

    fn fail_to_connect(
        &self,
        _session: &mut Session,
        _peer: &HttpPeer,
        _ctx: &mut Self::CTX,
        mut error: Box<Error>,
    ) -> Box<Error> {
        error.set_retry(false);
        error
    }

    fn error_while_proxy(
        &self,
        _peer: &HttpPeer,
        _session: &mut Session,
        mut error: Box<Error>,
        ctx: &mut Self::CTX,
        _client_reused: bool,
    ) -> Box<Error> {
        if ctx.target.is_some() {
            error.set_retry(false);
        }
        error
    }

    async fn fail_to_proxy(
        &self,
        session: &mut Session,
        error: &Error,
        ctx: &mut Self::CTX,
    ) -> FailToProxy {
        let code = match error.etype() {
            ErrorType::HTTPStatus(code) => *code,
            other => match error.esource() {
                ErrorSource::Upstream => 502,
                ErrorSource::Downstream => match other {
                    ErrorType::WriteError | ErrorType::ReadError | ErrorType::ConnectionClosed => 0,
                    _ => 400,
                },
                ErrorSource::Internal | ErrorSource::Unset => 500,
            },
        };
        let rejected = ctx.refused && self.listener == Listener::Execution;
        let mut reuse = false;
        if ctx.refused {
            let empty_request = session.as_mut().is_body_empty();
            let wrote_response = respond(session, code, String::new(), rejected)
                .await
                .is_ok();
            reuse = empty_request && wrote_response;
        }
        if !ctx.refused && code > 0 {
            let _ = session.respond_error(code).await;
        }
        FailToProxy {
            error_code: code,
            can_reuse_downstream: reuse,
        }
    }

    async fn upstream_peer(
        &self,
        session: &mut Session,
        ctx: &mut Self::CTX,
    ) -> Result<Box<HttpPeer>> {
        if ctx.target.is_some() {
            return Error::e_explain(ErrorType::HTTPStatus(503), "upstream already attempted");
        }
        if (ctx.tried_remote.is_some() || ctx.local_cold_refused)
            && ctx.started.elapsed() >= self.retry_budget
        {
            return Error::e_explain(ErrorType::HTTPStatus(503), "capacity retry budget elapsed");
        }
        let host = session
            .req_header()
            .headers
            .get("host")
            .and_then(|value| value.to_str().ok())
            .and_then(|value| value.split(':').next())
            .unwrap_or_default()
            .to_ascii_lowercase();
        if self.listener == Listener::Management {
            let target = serving::management_address(&self.pool, &host).ok_or_else(|| {
                Error::explain(ErrorType::HTTPStatus(503), "no ready management endpoint")
            })?;
            ctx.target = Some(Target::Management);
            return Ok(execution_peer(target, self.pool.serving.is_some()));
        }
        let compute = self.pool.serving.as_ref().map_or_else(
            || matches!(host.as_str(), "cpu-a.test" | "cpu-b.test"),
            |settings| {
                self.pool.slots.len() > self.pool.ordinary && settings.compute_hosts.contains(&host)
            },
        );
        let group = if compute {
            Group::Compute
        } else {
            Group::Ordinary
        };
        let key: Arc<str> = if self.pool.serving.is_none() && self.pool.policy.is_some() {
            let path = session.req_header().uri.path();
            let fixture_key = path.rsplit_once('/').map_or("", |(prefix, _)| prefix);
            if host.len() > 255 || fixture_key.len() > 128 {
                return Error::e_explain(ErrorType::HTTPStatus(400), "oversized fixture key");
            }
            format!("{host}{fixture_key}").into()
        } else {
            host.as_str().into()
        };
        let ingress = self.listener == Listener::Ingress;
        if ingress
            && ctx.tried_remote.is_none()
            && !ctx.local_cold_refused
            && let Some(router) = &self.router
        {
            router.observe_request(key.clone());
        }
        let streams = self
            .pool
            .streams
            .as_ref()
            .filter(|settings| settings.matches(session.req_header()));
        let traffic = if streams.is_some() {
            Traffic::Stream
        } else {
            Traffic::Short
        };
        let i = traffic.routing_index(group);
        ctx.group = group;
        ctx.traffic = traffic;
        if ingress
            && let Some(streams) = streams
            && ctx.stream_ingress.is_none()
        {
            ctx.stream_ingress = StreamPermit::acquire(&streams.ingress, streams.ingress_limit);
            if ctx.stream_ingress.is_none() {
                self.pool.refused[i].fetch_add(1, Ordering::Relaxed);
                self.pool.ingress_rejections[i].record(Rejection::IngressStreamInFlight);
                ctx.refused = true;
                return Error::e_explain(ErrorType::HTTPStatus(503), "ingress stream limit");
            }
        }
        let local = if ctx.local_cold_refused {
            Err(Rejections::default())
        } else {
            self.pool.acquire(&host, &key, group, traffic)
        };
        let (target, address) = match local {
            Ok(lease) => {
                let address = lease.endpoint.address;
                if ingress {
                    self.pool.local[i].fetch_add(1, Ordering::Relaxed);
                }
                (Target::Local(lease), address)
            }
            Err(mut reasons) => {
                if ingress {
                    let remote = self.router.as_ref().map_or_else(
                        || Err(Rejection::NoPeers.into()),
                        |router| router.acquire(key, group, traffic, ctx.tried_remote),
                    );
                    match remote {
                        Ok(lease) => {
                            let address = lease.address();
                            self.pool.forwarded[i].fetch_add(1, Ordering::Relaxed);
                            ctx.target = Some(Target::Remote(lease));
                            return Ok(execution_peer(address, self.pool.serving.is_some()));
                        }
                        Err(remote_reasons) => reasons.extend(remote_reasons),
                    }
                    self.pool.refused[i].fetch_add(1, Ordering::Relaxed);
                    self.pool.ingress_rejections[i].record_all(reasons);
                } else {
                    self.pool.execution_refused[i].fetch_add(1, Ordering::Relaxed);
                    self.pool.execution_rejections[i].record_all(reasons);
                }
                ctx.refused = true;
                return Error::e_explain(
                    ErrorType::HTTPStatus(503),
                    "no eligible execution target",
                );
            }
        };
        ctx.target = Some(target);
        Ok(execution_peer(address, self.pool.serving.is_some()))
    }
}

fn execution_peer(address: SocketAddr, serving: bool) -> Box<HttpPeer> {
    let mut peer = HttpPeer::new(address, false, String::new());
    peer.options.connection_timeout = Some(Duration::from_millis(250));
    if !serving {
        peer.options.read_timeout = Some(Duration::from_secs(10));
        peer.options.write_timeout = Some(Duration::from_secs(10));
    }
    Box::new(peer)
}

struct Supervisor {
    pool: Arc<Pool>,
    workerd: PathBuf,
    config: PathBuf,
}

#[async_trait]
impl BackgroundService for Supervisor {
    async fn start(&self, shutdown: ShutdownWatch) {
        let mut tasks = tokio::task::JoinSet::new();
        for slot in &self.pool.slots {
            tasks.spawn(supervise(
                Arc::clone(slot),
                self.workerd.clone(),
                self.config.clone(),
                Arc::clone(&self.pool),
                shutdown.clone(),
            ));
        }
        while let Some(result) = tasks.join_next().await {
            if let Err(error) = result {
                eprintln!("slot task failed: {error}");
            }
        }
    }
}

fn spawn(
    workerd: &Path,
    config: &Path,
    slot: &str,
) -> io::Result<(tokio::process::Child, SocketAddr)> {
    let listener = TcpListener::bind("127.0.0.1:0")?;
    let address = listener.local_addr()?;
    let fd = listener.as_raw_fd();
    let mut command = Command::new(workerd);
    command
        .args(["serve"])
        .arg(config)
        .arg("--experimental")
        .arg(format!("--socket-fd=http={fd}"))
        .env_remove("WORKERD_EXPERIMENTAL_CPU_MS")
        .env_remove("WORKERD_EXPERIMENTAL_CPU_METRICS")
        .stdin(Stdio::null())
        .stdout(Stdio::null());
    // The parent retains the listener until spawn finishes. Only the child's descriptor is
    // made inheritable, so concurrent spawns cannot keep another slot's socket alive.
    unsafe {
        command.pre_exec(move || {
            let flags = libc::fcntl(fd, libc::F_GETFD);
            if flags < 0 || libc::fcntl(fd, libc::F_SETFD, flags & !libc::FD_CLOEXEC) < 0 {
                return Err(io::Error::last_os_error());
            }
            Ok(())
        });
    }
    let child = tokio::process::Command::from(command)
        .kill_on_drop(true)
        .spawn()?;
    eprintln!("start {slot} pid={} address={address}", child.id().unwrap());
    Ok((child, address))
}

async fn probe(address: SocketAddr) -> bool {
    probe_http(
        address,
        b"GET /__ready HTTP/1.1\r\nHost: ready.test\r\nConnection: close\r\n\r\n",
    )
    .await
}

async fn probe_http(address: SocketAddr, request: &[u8]) -> bool {
    let attempt = async {
        let mut stream = TcpStream::connect(address).await?;
        stream.write_all(request).await?;
        let mut status = [0; 12];
        stream.read_exact(&mut status).await?;
        Ok::<_, io::Error>(status.starts_with(b"HTTP/1.1 200"))
    };
    matches!(
        timeout(Duration::from_millis(150), attempt).await,
        Ok(Ok(true))
    )
}

async fn stop(child: &mut tokio::process::Child) {
    if let Some(pid) = child.id() {
        // The unreaped child is still owned by this task, even if it has just exited.
        unsafe { libc::kill(pid as libc::pid_t, libc::SIGTERM) };
        if timeout(Duration::from_secs(3), child.wait()).await.is_err() {
            let _ = child.kill().await;
        }
    }
}

async fn supervise(
    slot: Arc<Slot>,
    workerd: PathBuf,
    config: PathBuf,
    pool: Arc<Pool>,
    mut shutdown: ShutdownWatch,
) {
    let serving = pool.serving.is_some();
    let mut backoff = Duration::from_millis(100);
    while !*shutdown.borrow() {
        let started_child = if serving {
            serving::spawn(&workerd, &config, &slot.name)
                .map(|(child, sockets)| (child, sockets.private, Some(sockets)))
        } else {
            spawn(&workerd, &config, &slot.name).map(|(child, address)| (child, address, None))
        };
        match started_child {
            Ok((mut child, address, sockets)) => {
                let started = Instant::now();
                let ready = tokio::select! {
                    _ = shutdown.changed() => false,
                    status = child.wait() => {
                        eprintln!("{} exited during startup: {status:?}", slot.name);
                        false
                    }
                    ready = async {
                        let startup = if serving { 60 } else { 5 };
                        while started.elapsed() < Duration::from_secs(startup) {
                            let ready = match sockets {
                                Some(ref sockets) => serving::probe_ready(sockets.management).await,
                                None => probe(address).await,
                            };
                            if ready {
                                return true;
                            }
                            sleep(Duration::from_millis(20)).await;
                        }
                        false
                    } => ready,
                };
                if ready {
                    let mut endpoint = Endpoint::new(address, child.id());
                    endpoint.serving = sockets;
                    let endpoint = Arc::new(endpoint);
                    *slot.endpoint.write().unwrap() = Some(Arc::clone(&endpoint));
                    eprintln!("ready {}", slot.name);
                    tokio::select! {
                        _ = shutdown.changed() => {}
                        status = child.wait() => eprintln!("{} exited: {status:?}", slot.name),
                    }
                    *slot.endpoint.write().unwrap() = None;
                    if *shutdown.borrow() {
                        let drain = Duration::from_secs(if serving { 20 } else { 2 });
                        let _ = timeout(drain, async {
                            while endpoint
                                .in_flight
                                .iter()
                                .any(|value| value.load(Ordering::Relaxed) > 0)
                            {
                                sleep(Duration::from_millis(10)).await;
                            }
                        })
                        .await;
                    }
                    if started.elapsed() > Duration::from_secs(20) {
                        backoff = Duration::from_millis(100);
                    }
                }
                stop(&mut child).await;
                if let Some(settings) = &pool.serving {
                    settings.cold.clear(&slot.name);
                }
            }
            Err(error) => eprintln!("start {} failed: {error}", slot.name),
        }
        tokio::select! {
            _ = shutdown.changed() => break,
            _ = sleep(backoff) => {}
        }
        backoff = (backoff * 2).min(Duration::from_secs(3));
    }
}

fn count(name: &str, default: usize, min: usize, max: usize) -> usize {
    let count = env::var(name)
        .ok()
        .map_or(default, |value| value.parse().unwrap());
    assert!((min..=max).contains(&count), "invalid {name}: {count}");
    count
}

fn main() {
    let supervised = env::args_os().nth(1).is_some_and(|arg| arg == "--serving");
    let bench_upstream = env::var("POOL_BENCH_UPSTREAM").ok().map(|value| {
        let address: SocketAddr = value.parse().expect("benchmark upstream address");
        assert!(
            address.ip().is_loopback(),
            "benchmark upstream must be loopback"
        );
        address
    });
    let port = count(
        "POOL_PORT",
        if supervised { 8081 } else { 28081 },
        1,
        65_535,
    );
    let ordinary = count("POOL_ORDINARY", 2, 1, 2);
    let compute = count("POOL_COMPUTE", 2, 0, 3);
    let mut slots = Vec::new();
    for (group, count) in [(Group::Ordinary, ordinary), (Group::Compute, compute)] {
        for i in 0..count {
            slots.push(Arc::new(Slot {
                name: format!("{}-{i}", group.name()),
                group,
                endpoint: RwLock::new(
                    bench_upstream.map(|address| Arc::new(Endpoint::new(address, None))),
                ),
                requests: AtomicUsize::new(0),
                cold: AtomicUsize::new(0),
                busy: AtomicUsize::new(0),
                rejected_by_reason: Default::default(),
            }));
        }
    }
    let policy = (supervised || env::var("POOL_ADMISSION").as_deref() == Ok("1")).then(|| {
        assert!(
            bench_upstream.is_none(),
            "admission requires locally managed workerd"
        );
        Arc::new(Policy::from_env())
    });
    let pool = Arc::new(Pool {
        slots,
        ordinary,
        serving: supervised.then(|| serving::Settings {
            compute_hosts: env::var("POOL_COMPUTE_HOSTS")
                .unwrap_or_default()
                .split(',')
                .filter(|host| !host.is_empty())
                .map(str::to_ascii_lowercase)
                .collect::<HashSet<_>>(),
            management_token: std::fs::read_to_string(
                env::var_os("POOL_MANAGEMENT_TOKEN_FILE")
                    .map(PathBuf::from)
                    .unwrap_or_else(|| "/secrets/KRUN_MANAGEMENT_TOKEN".into()),
            )
            .expect("read runtime management token"),
            last_ack_grant: Default::default(),
            cold: Default::default(),
        }),
        in_flight_limits: group_limits(
            "POOL_IN_FLIGHT",
            "POOL_ORDINARY_IN_FLIGHT",
            "POOL_COMPUTE_IN_FLIGHT",
            64,
        ),
        streams: (supervised && env::var("POOL_STREAMS_ENABLED").as_deref() == Ok("1"))
            .then(Streams::from_env),
        policy: policy.clone(),
        local: Default::default(),
        forwarded: Default::default(),
        refused: Default::default(),
        execution_refused: Default::default(),
        ingress_rejections: Default::default(),
        execution_rejections: Default::default(),
        retried: Default::default(),
    });
    let retry_budget = Duration::from_millis(count("POOL_RETRY_CAPACITY_MS", 0, 0, 5000) as u64);
    let mut server = Server::new_with_opt_and_conf(
        None,
        ServerConf {
            threads: count("POOL_PROXY_THREADS", 2, 1, 64),
            max_retries: if retry_budget.is_zero() { 1 } else { 2 },
            grace_period_seconds: Some(0),
            graceful_shutdown_timeout_seconds: Some(if supervised { 25 } else { 6 }),
            ..ServerConf::default()
        },
    );
    server.bootstrap();
    if bench_upstream.is_none() {
        let mut args = env::args_os().skip(if supervised { 2 } else { 1 });
        let workerd = PathBuf::from(args.next().expect("workerd path"))
            .canonicalize()
            .unwrap();
        let config = PathBuf::from(args.next().expect("workerd config"))
            .canonicalize()
            .unwrap();
        server.add_service(background_service(
            "workerd supervisor",
            Supervisor {
                pool: Arc::clone(&pool),
                workerd,
                config,
            },
        ));
    }
    let bind_address = env::var("POOL_BIND_ADDRESS")
        .unwrap_or_else(|_| if supervised { "0.0.0.0" } else { "127.0.0.1" }.to_owned())
        .parse()
        .expect("POOL_BIND_ADDRESS must be an IP address");
    let execution_port = count(
        "POOL_EXECUTION_PORT",
        if supervised { 8082 } else { 0 },
        0,
        65_535,
    );
    let metrics_port = count(
        "POOL_METRICS_PORT",
        if supervised { 8084 } else { 0 },
        0,
        65_535,
    );
    let management_port = count(
        "POOL_MANAGEMENT_PORT",
        if supervised { 8083 } else { 0 },
        0,
        65535,
    );
    let pod_name = env::var("POOL_BENCH_POD_NAME").ok();
    let router = if execution_port > 0 && (!supervised || env::var_os("POOL_PEER_DNS").is_some()) {
        let (name, address) = if supervised {
            let ip: std::net::IpAddr = env::var("POOL_POD_IP")
                .expect("POOL_POD_IP required for serving discovery")
                .parse()
                .expect("invalid Pod IP");
            (ip.to_string(), SocketAddr::new(ip, execution_port as u16))
        } else {
            let name = pod_name
                .clone()
                .filter(|name| routing::valid_name(name))
                .expect("POOL_BENCH_POD_NAME required for peer execution");
            (name, SocketAddr::new(bind_address, execution_port as u16))
        };
        Some(Arc::new(
            Router::from_env(name, address).expect("peer discovery configuration required"),
        ))
    } else {
        assert!(
            env::var_os("POOL_PEERS").is_none(),
            "POOL_EXECUTION_PORT required with POOL_PEERS"
        );
        None
    };
    if let Some(policy) = policy {
        server.add_service(background_service(
            "workerd resource sampler",
            Sampler {
                pool: pool.clone(),
                policy,
            },
        ));
    }
    if let Some(router) = &router {
        server.add_service(background_service(
            "execution peer health",
            PeerHealth(router.clone()),
        ));
    }
    let proxy = Proxy {
        pool,
        router,
        listener: Listener::Ingress,
        bench_pod_name: pod_name,
        bench_delay_file: env::var_os("POOL_BENCH_DELAY_FILE").map(PathBuf::from),
        retry_budget,
        config_agent: if supervised {
            let path = PathBuf::from(env::args_os().nth(3).expect("workerd config"));
            config_agent::Agent::from_config(&path).expect("configuration agent settings")
        } else {
            None
        },
    };
    if let Some(agent) = &proxy.config_agent {
        server.add_service(background_service(
            "runtime configuration agent",
            config_agent::AgentService(agent.clone()),
        ));
    }
    for (listener, port) in [
        (Listener::Ingress, port),
        (
            Listener::Execution,
            if proxy.router.is_some() {
                execution_port
            } else {
                0
            },
        ),
        (Listener::Management, management_port),
        (
            Listener::Coordinator,
            count(
                "POOL_COORDINATOR_PORT",
                if supervised { 8085 } else { 0 },
                0,
                65535,
            ),
        ),
        (Listener::Metrics, metrics_port),
    ] {
        if port > 0 {
            let mut service = http_proxy_service(
                &server.configuration,
                Proxy {
                    listener,
                    ..proxy.clone()
                },
            );
            let listener_address = if listener == Listener::Coordinator {
                "127.0.0.1".parse().unwrap()
            } else {
                bind_address
            };
            service.add_tcp(&SocketAddr::new(listener_address, port as u16).to_string());
            server.add_service(service);
        }
    }
    server.run_forever();
}
