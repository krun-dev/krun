use std::collections::{BTreeMap, HashMap, HashSet};
use std::path::Path;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use async_trait::async_trait;
use futures_util::{SinkExt, StreamExt};
use pingora_core::server::ShutdownWatch;
use pingora_core::services::background::BackgroundService;
use prost::Message;
use serde::Deserialize;
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use tokio::sync::{mpsc, oneshot};
use tokio::time::{Instant, sleep, timeout};
use tokio_tungstenite::tungstenite::client::IntoClientRequest;
use tokio_tungstenite::tungstenite::protocol::WebSocketConfig;
use tokio_tungstenite::tungstenite::{self, Message as WsMessage};

pub const CONFIG_HEADER: &str = "x-krun-config-handle";
const MARKER: &str = "# krun-config-agent ";
const MAX_FRAME_ERROR_BYTES: usize = 256;

#[derive(Clone, PartialEq, Message)]
pub struct Ref {
    #[prost(string, tag = "1")]
    pub kind: String,
    #[prost(string, tag = "2")]
    pub id: String,
    #[prost(string, tag = "3")]
    pub incarnation: String,
    #[prost(uint64, tag = "4")]
    pub generation: u64,
}

impl Ref {
    fn key(&self) -> String {
        format!("{}:{}", self.kind, self.id)
    }
    fn named(kind: &str, id: impl Into<String>) -> Self {
        Self {
            kind: kind.into(),
            id: id.into(),
            ..Self::default()
        }
    }
}

#[derive(Clone, PartialEq, Message)]
pub struct Resource {
    #[prost(message, optional, tag = "1")]
    pub version: Option<Ref>,
    #[prost(bool, tag = "2")]
    pub deleted: bool,
    #[prost(bytes = "vec", tag = "3")]
    pub payload_json: Vec<u8>,
    #[prost(message, repeated, tag = "4")]
    pub dependencies: Vec<Ref>,
}

#[derive(Clone, PartialEq, Message)]
pub struct Frame {
    #[prost(string, tag = "1")]
    pub r#type: String,
    #[prost(uint64, tag = "2")]
    pub request_id: u64,
    #[prost(string, tag = "3")]
    pub epoch: String,
    #[prost(string, tag = "4")]
    pub pod_uid: String,
    #[prost(message, repeated, tag = "5")]
    pub refs: Vec<Ref>,
    #[prost(message, repeated, tag = "6")]
    pub resources: Vec<Resource>,
    #[prost(string, tag = "7")]
    pub error: String,
    #[prost(int64, tag = "8")]
    pub confirmed_unix_millis: i64,
    #[prost(bool, tag = "9")]
    pub fresh: bool,
    #[prost(uint32, tag = "10")]
    pub part: u32,
    #[prost(uint32, tag = "11")]
    pub parts: u32,
    #[prost(bytes = "vec", tag = "12")]
    pub chunk: Vec<u8>,
    #[prost(uint64, tag = "13")]
    pub nonce: u64,
    #[prost(bool, tag = "14")]
    pub applied: bool,
    #[prost(string, tag = "15")]
    pub session_proof: String,
}

impl Frame {
    fn decode_wire(data: impl prost::bytes::Buf) -> Option<Self> {
        let frame = Self::decode(data).ok()?;
        (frame.error.len() <= MAX_FRAME_ERROR_BYTES).then_some(frame)
    }
}

#[derive(Clone, Deserialize)]
pub struct Limits {
    #[serde(default = "default_verify_millis")]
    pub verify_millis: u64,
    pub frame_bytes: usize,
    pub resource_bytes: usize,
    pub pod_cache_bytes: usize,
    pub pod_cache_entries: usize,
    pub pod_cold_concurrency: usize,
}

fn default_verify_millis() -> u64 {
    5000
}

#[derive(Clone, Deserialize)]
pub struct Settings {
    pub control_url: String,
    pub pool: String,
    pub token_file: String,
    pub pod_file: String,
    pub limits: Limits,
    #[serde(default)]
    pub static_hosts: Vec<String>,
}

struct Stored {
    wire: Resource,
    value: Value,
    loaded: Instant,
}

pub struct Bundle {
    pub handle: String,
    pub body: Vec<u8>,
    host: String,
    route_index: usize,
    expires: Option<i64>,
    dependencies: Vec<String>,
    used: Mutex<Instant>,
}

struct State {
    verify_millis: u64,
    epoch: String,
    authorized: bool,
    connected: bool,
    checked: Option<Instant>,
    authority_stamp: i64,
    applied: bool,
    resources: HashMap<String, Stored>,
    roots: HashMap<String, Instant>,
    bundles: HashMap<String, Arc<Bundle>>,
    current_bundles: HashMap<(String, usize), String>,
    resource_bundles: HashMap<String, HashSet<String>>,
    bytes: usize,
}

impl State {
    fn fresh(&self) -> bool {
        self.connected
            && self
                .checked
                .is_some_and(|at| at.elapsed() < Duration::from_millis(self.verify_millis))
    }

    fn confirm(&mut self, stamp: i64, fresh: bool) {
        if !fresh {
            self.connected = false;
            return;
        }
        self.connected = true;
        // Ordinary transport heartbeats cannot renew an old authority proof.
        if stamp > self.authority_stamp {
            self.authority_stamp = stamp;
            let age = wall_millis().saturating_sub(stamp).max(0) as u64;
            self.checked = Instant::now().checked_sub(Duration::from_millis(age));
        }
    }
}

struct Resolve {
    host: String,
    reply: oneshot::Sender<std::result::Result<(), u16>>,
}

struct Pending {
    hosts: Vec<String>,
    replies: Vec<oneshot::Sender<std::result::Result<(), u16>>>,
    deadline: Instant,
}

pub struct Agent {
    settings: Settings,
    token: String,
    pod: String,
    session_proof: Mutex<String>,
    state: Mutex<State>,
    commands: mpsc::Sender<Resolve>,
    receiver: Mutex<Option<mpsc::Receiver<Resolve>>>,
    next: AtomicU64,
    cold_reads: AtomicU64,
    cold_rejected: AtomicU64,
}

impl Agent {
    pub fn is_static_host(&self, host: &str) -> bool {
        self.settings.static_hosts.iter().any(|h| h == host)
    }
    pub fn from_config(path: &Path) -> std::io::Result<Option<Arc<Self>>> {
        let text = std::fs::read_to_string(path)?;
        let Some(raw) = text.lines().find_map(|line| line.strip_prefix(MARKER)) else {
            return Ok(None);
        };
        let settings: Settings = serde_json::from_str(raw).map_err(std::io::Error::other)?;
        let token = std::fs::read_to_string(&settings.token_file)?
            .trim()
            .to_owned();
        let pod = std::fs::read_to_string(&settings.pod_file)?
            .trim()
            .to_owned();
        if token.is_empty()
            || pod.is_empty()
            || settings.limits.frame_bytes < 4096
            || !(100..=60000).contains(&settings.limits.verify_millis)
            || settings.limits.resource_bytes > settings.limits.pod_cache_bytes
        {
            return Err(std::io::Error::other(
                "invalid configuration agent identity or limits",
            ));
        }
        Ok(Some(Self::new(settings, token, pod)))
    }

    fn new(settings: Settings, token: String, pod: String) -> Arc<Self> {
        let (commands, receiver) = mpsc::channel(settings.limits.pod_cold_concurrency);
        let verify_millis = settings.limits.verify_millis;
        Arc::new(Self {
            settings,
            token,
            pod,
            session_proof: Mutex::new(String::new()),
            state: Mutex::new(State {
                verify_millis,
                epoch: String::new(),
                authorized: false,
                connected: false,
                checked: None,
                authority_stamp: 0,
                applied: false,
                resources: HashMap::new(),
                roots: HashMap::new(),
                bundles: HashMap::new(),
                current_bundles: HashMap::new(),
                resource_bundles: HashMap::new(),
                bytes: 0,
            }),
            commands,
            receiver: Mutex::new(Some(receiver)),
            next: AtomicU64::new(1),
            cold_reads: AtomicU64::new(0),
            cold_rejected: AtomicU64::new(0),
        })
    }

    pub fn head(&self) -> std::result::Result<Value, u16> {
        let state = self.state.lock().unwrap();
        if !state.authorized {
            return Err(503);
        }
        Ok(
            json!({"config_epoch":state.epoch,"config_revision":0,"lazy_routes":true,"protocol":2,
            "pool":self.settings.pool,"config_fresh":state.fresh(),"config_sync_healthy":state.connected,
            "verify_millis":state.verify_millis,"config_current":state.applied && state.fresh(),"confirmed_unix_millis":state.authority_stamp}),
        )
    }

    pub fn get_bundle(&self, handle: &str) -> std::result::Result<Arc<Bundle>, u16> {
        let state = self.state.lock().unwrap();
        if !state.authorized {
            return Err(503);
        }
        state.bundles.get(handle).cloned().ok_or(503)
    }

    pub async fn resolve(&self, host: &str, path: &str) -> std::result::Result<Arc<Bundle>, u16> {
        if host.is_empty()
            || host.len() > 253
            || host
                .bytes()
                .any(|b| !b.is_ascii_alphanumeric() && b != b'.' && b != b'-')
        {
            return Err(404);
        }
        if let Some(bundle) = self.build_bundle(host, path)? {
            return Ok(bundle);
        }
        let (reply, receive) = oneshot::channel();
        if self
            .commands
            .try_send(Resolve {
                host: host.into(),
                reply,
            })
            .is_err()
        {
            self.cold_rejected.fetch_add(1, Ordering::Relaxed);
            return Err(503);
        }
        match timeout(Duration::from_secs(3), receive).await {
            Ok(Ok(Ok(()))) => self.build_bundle(host, path)?.ok_or(503),
            Ok(Ok(Err(status))) => Err(status),
            _ => Err(503),
        }
    }

    fn build_bundle(
        &self,
        host: &str,
        path: &str,
    ) -> std::result::Result<Option<Arc<Bundle>>, u16> {
        let mut state = self.state.lock().unwrap();
        if !state.authorized {
            return Err(503);
        }
        if let Some(used) = state.roots.get_mut(host) {
            *used = Instant::now();
        }
        let root_key = format!("route:{}/{}", self.settings.pool, host);
        let Some(root) = state.resources.get(&root_key) else {
            return Ok(None);
        };
        if root.wire.deleted {
            return if state.fresh() && root.loaded.elapsed() >= Duration::from_secs(1) {
                Ok(None)
            } else {
                Err(404)
            };
        }
        let bindings = root.value.as_array().ok_or(503u16)?;
        let route_index = select_route(bindings, path)?;
        let route_key = (host.to_owned(), route_index);
        if let Some(handle) = state.current_bundles.get(&route_key) {
            if let Some(bundle) = state.bundles.get(handle) {
                if bundle
                    .expires
                    .is_some_and(|expires| expires <= wall_millis())
                {
                    return Err(410);
                }
                *bundle.used.lock().unwrap() = Instant::now();
                return Ok(Some(bundle.clone()));
            }
        }
        let binding = bindings.get(route_index).ok_or(404u16)?;
        let mut route = binding.clone();
        let route_object = route.as_object_mut().ok_or(503u16)?;
        let worker = text(binding, "worker_id");
        let environment = text(binding, "environment");
        let mut needed = vec![root_key];
        let scope_key = format!("scope:{worker}");
        let Some(scope) = state.resources.get(&scope_key) else {
            return Ok(None);
        };
        if scope.wire.deleted || scope.value.get("allowed") != Some(&Value::Bool(true)) {
            return Err(404);
        }
        needed.push(scope_key);
        let fixed = binding.get("fixed") == Some(&Value::Bool(true));
        let deployment_key = format!("deployment:{worker}/{environment}");
        let Some(deployment) = state.resources.get(&deployment_key) else {
            return Ok(None);
        };
        let generation = deployment.wire.version.as_ref().ok_or(503u16)?.generation;
        if deployment.wire.deleted {
            return Err(404);
        }
        if deployment.value.get("enabled") != Some(&Value::Bool(true)) {
            return Err(410);
        }
        if !fixed {
            for field in [
                "version",
                "candidate_version",
                "candidate_weight",
                "retained_versions",
            ] {
                if let Some(value) = deployment.value.get(field) {
                    route_object.insert(field.into(), value.clone());
                }
            }
            route_object.insert(
                "enabled".into(),
                Value::Bool(
                    binding.get("enabled") == Some(&Value::Bool(true))
                        && deployment.value.get("enabled") == Some(&Value::Bool(true)),
                ),
            );
        }
        needed.push(deployment_key);
        let mut expires_at = None;
        if let Some(expires) = route.get("expires_at").and_then(Value::as_str) {
            let at = chrono::DateTime::parse_from_rfc3339(expires).map_err(|_| 503u16)?;
            expires_at = Some(at.timestamp_millis());
            if at.timestamp_millis() <= wall_millis() {
                return Err(410);
            }
        }
        if route.get("enabled") != Some(&Value::Bool(true)) {
            return Err(410);
        }
        let primary = text(&route, "version");
        if primary.is_empty() {
            return Err(503);
        }
        let mut ids = vec![primary.to_owned()];
        let candidate = text(&route, "candidate_version");
        if !candidate.is_empty() {
            ids.push(candidate.into());
        }
        if let Some(retained) = route.get("retained_versions").and_then(Value::as_array) {
            ids.extend(
                retained
                    .iter()
                    .map(|v| text(v, "version_id").to_owned())
                    .filter(|id| !id.is_empty()),
            );
        }
        for id in ids {
            needed.push(format!("version:{id}"));
        }
        let mut seen = HashSet::new();
        let mut offset = 0;
        while offset < needed.len() {
            let key = needed[offset].clone();
            offset += 1;
            if !seen.insert(key.clone()) {
                continue;
            }
            let Some(resource) = state.resources.get(&key) else {
                return Ok(None);
            };
            if resource.wire.deleted {
                return Err(503);
            }
            if resource
                .wire
                .version
                .as_ref()
                .is_some_and(|r| r.kind != "route" && r.kind != "deployment")
            {
                for dep in &resource.wire.dependencies {
                    needed.push(dep.key());
                }
            }
            if seen.len() > self.settings.limits.pod_cache_entries {
                return Err(503);
            }
        }
        let dependencies: Vec<_> = seen.into_iter().collect();
        let mut identity = BTreeMap::new();
        for key in &dependencies {
            let resource = &state.resources[key];
            let version = resource.wire.version.as_ref().ok_or(503u16)?;
            identity.insert(
                key.clone(),
                format!("{}/{}", version.incarnation, version.generation),
            );
        }
        let identity = serde_json::to_vec(&(state.epoch.as_str(), host, route_index, identity))
            .map_err(|_| 503u16)?;
        let handle = format!("{:x}", Sha256::digest(identity));
        if let Some(bundle) = state.bundles.get(&handle).cloned() {
            *bundle.used.lock().unwrap() = Instant::now();
            state.current_bundles.insert(route_key, handle);
            return Ok(Some(bundle));
        }
        // Clone/serialize descriptors only when a resource version changed.
        // The ordinary request path above is a local handle lookup.
        let mut versions = serde_json::Map::new();
        for key in &dependencies {
            let resource = &state.resources[key];
            let version = resource.wire.version.as_ref().ok_or(503u16)?;
            if version.kind == "version" {
                versions.insert(version.id.clone(), resource.value.clone());
            }
        }
        let body = serde_json::to_vec(
            &json!({"handle":handle,"config_epoch":state.epoch,"route":route,"versions":versions,"target_generation":generation,
            "config_fresh":state.fresh(),"config_sync_healthy":state.connected}),
        )
        .map_err(|_| 503u16)?;
        if state.bytes + body.len() > self.settings.limits.pod_cache_bytes {
            self.trim_bundles(&mut state);
            if state.bytes + body.len() > self.settings.limits.pod_cache_bytes {
                return Err(503);
            }
        }
        let bundle = Arc::new(Bundle {
            handle: handle.clone(),
            body,
            host: host.into(),
            route_index,
            expires: expires_at,
            dependencies,
            used: Mutex::new(Instant::now()),
        });
        state.bytes += bundle.body.len();
        for dependency in &bundle.dependencies {
            state
                .resource_bundles
                .entry(dependency.clone())
                .or_default()
                .insert(handle.clone());
        }
        state.current_bundles.insert(route_key, handle.clone());
        state.bundles.insert(handle, bundle.clone());
        Ok(Some(bundle))
    }

    fn trim_bundles(&self, state: &mut State) {
        if !state.fresh() {
            return;
        }
        let remove: Vec<_> = state
            .bundles
            .iter()
            .filter(|(_, b)| {
                Arc::strong_count(b) == 1
                    && (b.used.lock().unwrap().elapsed() > Duration::from_secs(300)
                        || state
                            .current_bundles
                            .get(&(b.host.clone(), b.route_index))
                            .is_none_or(|handle| handle != &b.handle))
            })
            .map(|(key, _)| key.clone())
            .collect();
        for key in remove {
            if let Some(b) = state.bundles.remove(&key) {
                state.bytes = state.bytes.saturating_sub(b.body.len());
                let route_key = (b.host.clone(), b.route_index);
                if state.current_bundles.get(&route_key) == Some(&key) {
                    state.current_bundles.remove(&route_key);
                }
                for dependency in &b.dependencies {
                    if let Some(handles) = state.resource_bundles.get_mut(dependency) {
                        handles.remove(&key);
                        if handles.is_empty() {
                            state.resource_bundles.remove(dependency);
                        }
                    }
                }
            }
        }
    }

    // Retire only while authority is reachable. Offline hot configurations and
    // bundles pinned by in-flight proxy requests survive an extended outage.
    fn retire(&self) -> Vec<Ref> {
        let mut state = self.state.lock().unwrap();
        if !state.fresh() {
            return Vec::new();
        }
        self.trim_bundles(&mut state);
        let retired: Vec<String> = state
            .roots
            .iter()
            .filter(|(host, used)| {
                let negative = state
                    .resources
                    .get(&format!("route:{}/{}", self.settings.pool, host))
                    .is_some_and(|r| {
                        r.wire.deleted && r.loaded.elapsed() >= Duration::from_secs(1)
                    });
                let pinned = state
                    .bundles
                    .values()
                    .any(|b| b.host == **host && Arc::strong_count(b) > 1);
                !pinned && (negative || used.elapsed() >= Duration::from_secs(300))
            })
            .map(|(host, _)| host.clone())
            .collect();
        for host in &retired {
            state.roots.remove(host);
        }
        let mut reachable = HashSet::new();
        let mut pending: Vec<_> = state
            .roots
            .keys()
            .map(|host| format!("route:{}/{}", self.settings.pool, host))
            .collect();
        while let Some(key) = pending.pop() {
            if !reachable.insert(key.clone()) {
                continue;
            }
            if let Some(resource) = state.resources.get(&key) {
                pending.extend(resource.wire.dependencies.iter().map(Ref::key));
            }
        }
        let garbage: Vec<_> = state
            .resources
            .keys()
            .filter(|key| !reachable.contains(*key))
            .cloned()
            .collect();
        for key in garbage {
            if let Some(resource) = state.resources.remove(&key) {
                state.bytes = state.bytes.saturating_sub(resource.wire.encoded_len());
            }
        }
        retired
            .into_iter()
            .map(|host| Ref::named("route", format!("{}/{}", self.settings.pool, host)))
            .collect()
    }

    fn install(&self, frame: &Frame) -> std::result::Result<Vec<Ref>, String> {
        // Bound decoded collection work before taking the serving lock. A snapshot
        // may contain more than the 256 requested roots because it includes their graph.
        if frame.encoded_len() > self.settings.limits.resource_bytes
            || frame.resources.len() > self.settings.limits.pod_cache_entries
            || frame.refs.len() > self.settings.limits.pod_cache_entries
        {
            return Err("configuration frame exceeds Pod budget".into());
        }
        let mut state = self.state.lock().unwrap();
        if frame.epoch != state.epoch || !state.authorized {
            return Err("configuration epoch mismatch".into());
        }
        self.trim_bundles(&mut state);
        let mut staged = HashMap::new();
        let mut resource_keys = HashSet::new();
        let mut bytes = state.bytes;
        for resource in &frame.resources {
            let version = resource
                .version
                .as_ref()
                .ok_or("resource version missing")?;
            if version.generation == 0
                || version.incarnation.is_empty()
                || version.id.len() > 512
                || resource.payload_json.len() > self.settings.limits.resource_bytes
            {
                return Err("invalid resource".into());
            }
            let key = version.key();
            if !resource_keys.insert(key.clone()) {
                return Err("duplicate configuration resource".into());
            }
            if let Some(old) = state.resources.get(&key) {
                let old_version = old.wire.version.as_ref().unwrap();
                if old_version.incarnation == version.incarnation
                    && old_version.generation > version.generation
                {
                    continue;
                }
                bytes = bytes.saturating_sub(old.wire.encoded_len());
            }
            bytes += resource.encoded_len();
            let value = if resource.deleted {
                Value::Null
            } else {
                serde_json::from_slice(&resource.payload_json)
                    .map_err(|_| "invalid resource payload")?
            };
            if version.kind == "route"
                && !resource.deleted
                && !value
                    .as_array()
                    .is_some_and(|bindings| bindings.iter().all(Value::is_object))
            {
                return Err("invalid route bindings".into());
            }
            staged.insert(
                key,
                Stored {
                    wire: resource.clone(),
                    value,
                    loaded: Instant::now(),
                },
            );
        }
        let cold_host = staged.iter().any(|(key, value)| {
            value
                .wire
                .version
                .as_ref()
                .is_some_and(|r| r.kind == "route")
                && !state.resources.contains_key(key)
        });
        let entry_limit = if cold_host {
            self.settings.limits.pod_cache_entries * 3 / 4
        } else {
            self.settings.limits.pod_cache_entries
        };
        let byte_limit = if cold_host {
            self.settings.limits.pod_cache_bytes * 3 / 4
        } else {
            self.settings.limits.pod_cache_bytes
        };
        if bytes > byte_limit
            || state.resources.len()
                + staged
                    .iter()
                    .filter(|(key, _)| !state.resources.contains_key(*key))
                    .count()
                > entry_limit
        {
            return Err("configuration working set exceeds Pod budget".into());
        }
        for (_, value) in &staged {
            for dependency in &value.wire.dependencies {
                let key = dependency.key();
                if !state.resources.contains_key(&key) && !staged.contains_key(&key) {
                    return Err("configuration dependency missing".into());
                }
            }
        }
        for reference in &frame.refs {
            let key = reference.key();
            let actual = staged
                .get(&key)
                .or_else(|| state.resources.get(&key))
                .map(|v| &v.wire);
            if actual.and_then(|r| r.version.as_ref()) != Some(reference) {
                return Err("configuration version proof missing".into());
            }
        }
        state.applied = false;
        state.bytes = bytes;
        let mut acks = Vec::new();
        for (key, value) in staged {
            let changed = state.resources.get(&key).is_none_or(|old| {
                old.wire.version != value.wire.version || old.wire.deleted != value.wire.deleted
            });
            if changed {
                let handles = state
                    .resource_bundles
                    .get(&key)
                    .map(|handles| handles.iter().cloned().collect::<Vec<_>>())
                    .unwrap_or_default();
                for handle in handles {
                    if let Some(bundle) = state.bundles.get(&handle) {
                        let route_key = (bundle.host.clone(), bundle.route_index);
                        if state.current_bundles.get(&route_key) == Some(&handle) {
                            state.current_bundles.remove(&route_key);
                        }
                    }
                }
            }
            acks.push(value.wire.version.clone().unwrap());
            state.resources.insert(key, value);
        }
        if !frame.refs.is_empty() {
            acks = frame.refs.clone();
        }
        Ok(acks)
    }

    fn fence(&self, epoch: Option<&str>) {
        let mut state = self.state.lock().unwrap();
        state.authorized = false;
        state.applied = false;
        state.connected = false;
        state.checked = None;
        if let Some(epoch) = epoch {
            state.epoch = epoch.into();
            state.resources.clear();
            state.bundles.clear();
            state.current_bundles.clear();
            state.resource_bundles.clear();
            state.roots.clear();
            state.bytes = 0;
            state.authority_stamp = 0;
        }
    }

    pub fn metrics(&self) -> String {
        let state = self.state.lock().unwrap();
        format!(
            "krun_config_agent_resources {}\nkrun_config_agent_cache_bytes {}\nkrun_config_agent_connected {}\nkrun_config_agent_fresh {}\nkrun_config_agent_cold_reads_total {}\nkrun_config_agent_cold_rejected_total {}\n",
            state.resources.len(),
            state.bytes,
            u8::from(state.connected),
            u8::from(state.fresh()),
            self.cold_reads.load(Ordering::Relaxed),
            self.cold_rejected.load(Ordering::Relaxed)
        )
    }

    async fn run(&self) {
        let mut commands = self
            .receiver
            .lock()
            .unwrap()
            .take()
            .expect("agent runs once");
        let mut backoff = Duration::from_millis(250);
        loop {
            let url = self
                .settings
                .control_url
                .replacen("https://", "wss://", 1)
                .replacen("http://", "ws://", 1);
            let Ok(mut request) = url.into_client_request() else {
                return;
            };
            request.headers_mut().insert(
                "authorization",
                format!("Bearer {}", self.token).parse().unwrap(),
            );
            request
                .headers_mut()
                .insert("x-runtime-pool", self.settings.pool.parse().unwrap());
            let websocket_config = WebSocketConfig::default()
                .max_message_size(Some(self.settings.limits.frame_bytes))
                .max_frame_size(Some(self.settings.limits.frame_bytes));
            let connected = timeout(
                Duration::from_secs(3),
                tokio_tungstenite::connect_async_with_config(
                    request,
                    Some(websocket_config),
                    false,
                ),
            )
            .await;
            let mut socket = match connected {
                Ok(Ok((socket, _))) => socket,
                other => {
                    if let Ok(Err(tungstenite::Error::Http(response))) = &other {
                        if response.status() == 401 || response.status() == 403 {
                            self.fence(None);
                        }
                    }
                    self.state.lock().unwrap().connected = false;
                    while let Ok(command) = commands.try_recv() {
                        let _ = command.reply.send(Err(503));
                    }
                    sleep(
                        backoff
                            + Duration::from_millis(
                                (wall_millis() as u64
                                    + self.pod.bytes().map(u64::from).sum::<u64>())
                                    % 250,
                            ),
                    )
                    .await;
                    backoff = (backoff * 2).min(Duration::from_secs(5));
                    continue;
                }
            };
            let mut random = [0u8; 32];
            if getrandom::fill(&mut random).is_err() {
                continue;
            }
            let proof: String = random.iter().map(|byte| format!("{byte:02x}")).collect();
            *self.session_proof.lock().unwrap() = proof.clone();
            let epoch = self.state.lock().unwrap().epoch.clone();
            let hello = Frame {
                r#type: "hello".into(),
                pod_uid: self.pod.clone(),
                session_proof: proof,
                epoch,
                ..Frame::default()
            };
            if socket
                .send(WsMessage::Binary(hello.encode_to_vec().into()))
                .await
                .is_err()
            {
                continue;
            }
            let mut pending: HashMap<u64, Pending> = HashMap::new();
            let mut by_host: HashMap<String, u64> = HashMap::new();
            let mut chunks = Vec::new();
            let mut chunk_index = 0;
            let mut chunk_parts = 0;
            let mut chunk_nonce = 0;
            let mut last_nonce = 0;
            let mut welcomed = false;
            let welcome_started = Instant::now();
            let mut chunk_started: Option<Instant> = None;
            let mut tick = tokio::time::interval(Duration::from_millis(250));
            'connection: loop {
                tokio::select! {
                command = commands.recv() => {
                    let Some(command) = command else {
                        return;
                    };
                    let mut batch = vec![command];
                    while batch.len() < self.settings.limits.pod_cold_concurrency {
                        match commands.try_recv() {
                            Ok(command) => batch.push(command),
                            Err(_) => break,
                        }
                    }
                    let id = self.next.fetch_add(1, Ordering::Relaxed);
                    let mut hosts = Vec::new();
                    let mut replies = Vec::new();
                    for command in batch {
                        if let Some(existing) = by_host.get(&command.host) {
                            let existing = pending.get_mut(existing).unwrap();
                            if existing.replies.len() >= 64 {
                                self.cold_rejected.fetch_add(1, Ordering::Relaxed);
                                let _ = command.reply.send(Err(503));
                            } else {
                                existing.replies.push(command.reply);
                            }
                            continue;
                        }
                        if hosts.contains(&command.host) {
                            replies.push(command.reply);
                            continue;
                        }
                        if by_host.len() + hosts.len() >= self.settings.limits.pod_cold_concurrency {
                            let _ = command.reply.send(Err(503));
                            self.cold_rejected.fetch_add(1, Ordering::Relaxed);
                            continue;
                        }
                        let mut state = self.state.lock().unwrap();
                        if !state.roots.contains_key(&command.host)
                            && state.roots.len() >= self.settings.limits.pod_cache_entries / 4
                        {
                            let _ = command.reply.send(Err(503));
                            continue;
                        }
                        state.roots.insert(command.host.clone(), Instant::now());
                        hosts.push(command.host);
                        replies.push(command.reply);
                    }
                    if hosts.is_empty() {
                        continue;
                    }
                    let epoch = self.state.lock().unwrap().epoch.clone();
                    let frame = Frame {
                        r#type: "resolve".into(),
                        request_id: id,
                        epoch,
                        refs: hosts
                            .iter()
                            .map(|host| Ref::named("route", format!("{}/{}", self.settings.pool, host)))
                            .collect(),
                        ..Frame::default()
                    };
                    for host in &hosts {
                        by_host.insert(host.clone(), id);
                    }
                    pending.insert(
                        id,
                        Pending {
                            hosts,
                            replies,
                            deadline: Instant::now() + Duration::from_secs(3),
                        },
                    );
                    self.cold_reads.fetch_add(1, Ordering::Relaxed);
                    if socket
                        .send(WsMessage::Binary(frame.encode_to_vec().into()))
                        .await
                        .is_err()
                    {
                        break;
                    }
                }
                incoming = socket.next() => {
                    let data = match incoming {
                        Some(Ok(WsMessage::Binary(data))) => data,
                        Some(Ok(WsMessage::Ping(data))) => {
                            if socket.send(WsMessage::Pong(data)).await.is_err() {
                                break;
                            }
                            continue;
                        }
                        Some(Ok(WsMessage::Pong(_))) => continue,
                        _ => break,
                    };
                    let Some(mut frame) = Frame::decode_wire(data) else {
                        break;
                    };
                    if chunk_index > 0 && frame.r#type != "chunk" {
                        break;
                    }
                    if frame.r#type == "chunk" {
                        if chunk_index == 0 {
                            chunk_started = Some(Instant::now());
                        }
                        if frame.parts == 0
                            || frame.parts as usize
                                > self.settings.limits.resource_bytes / (self.settings.limits.frame_bytes - 256) + 2
                            || frame.part != chunk_index
                            || chunk_index > 0 && (frame.parts != chunk_parts || frame.nonce != chunk_nonce)
                        {
                            break;
                        }
                        chunk_parts = frame.parts;
                        chunk_nonce = frame.nonce;
                        chunk_index += 1;
                        chunks.extend_from_slice(&frame.chunk);
                        if chunks.len() > self.settings.limits.resource_bytes {
                            break;
                        }
                        if chunk_index < chunk_parts {
                            continue;
                        }
                        let Some(decoded) = Frame::decode_wire(chunks.as_slice()) else {
                            break;
                        };
                        frame = decoded;
                        chunks.clear();
                        chunk_index = 0;
                        chunk_started = None;
                    }
                    if frame.nonce <= last_nonce {
                        break;
                    }
                    last_nonce = frame.nonce;
                    match frame.r#type.as_str() {
                        "welcome" => {
                            welcomed = true;
                            let roots = {
                                let mut state = self.state.lock().unwrap();
                                if !state.epoch.is_empty() && state.epoch != frame.epoch {
                                    drop(state);
                                    self.fence(Some(&frame.epoch));
                                    break;
                                }
                                state.epoch = frame.epoch.clone();
                                state.authorized = true;
                                state.applied = false;
                                state.confirm(frame.confirmed_unix_millis, true);
                                state.roots.keys().cloned().collect::<Vec<_>>()
                            };
                            backoff = Duration::from_millis(250);
                            let versions = {
                                let state = self.state.lock().unwrap();
                                state
                                    .resources
                                    .values()
                                    .filter_map(|r| r.wire.version.clone())
                                    .collect::<Vec<_>>()
                            };
                            for batch in versions.chunks(128) {
                                let resume = Frame {
                                    r#type: "resume".into(),
                                    epoch: frame.epoch.clone(),
                                    refs: batch.to_vec(),
                                    ..Frame::default()
                                };
                                if socket
                                    .send(WsMessage::Binary(resume.encode_to_vec().into()))
                                    .await
                                    .is_err()
                                {
                                    break 'connection;
                                }
                            }
                            for batch in roots.chunks(32) {
                                let subscribe = Frame {
                                    r#type: "subscribe".into(),
                                    epoch: frame.epoch.clone(),
                                    refs: batch
                                        .iter()
                                        .map(|host| Ref::named("route", format!("{}/{}", self.settings.pool, host)))
                                        .collect(),
                                    ..Frame::default()
                                };
                                if socket
                                    .send(WsMessage::Binary(subscribe.encode_to_vec().into()))
                                    .await
                                    .is_err()
                                {
                                    break 'connection;
                                }
                            }
                            let ready = Frame {
                                r#type: "ready".into(),
                                epoch: frame.epoch.clone(),
                                ..Frame::default()
                            };
                            if socket
                                .send(WsMessage::Binary(ready.encode_to_vec().into()))
                                .await
                                .is_err()
                            {
                                break;
                            }
                        }
                        "reset" => {
                            self.fence(Some(&frame.epoch));
                            break;
                        }
                        "status" => {
                            let mut state = self.state.lock().unwrap();
                            if state.epoch != frame.epoch {
                                drop(state);
                                self.fence(Some(&frame.epoch));
                                break;
                            }
                            state.confirm(frame.confirmed_unix_millis, frame.fresh);
                            state.applied = frame.applied;
                        }
                        "snapshot" => {
                            let acks = match self.install(&frame) {
                                Ok(acks) => acks,
                                Err(error) if error == "configuration working set exceeds Pod budget" && frame.request_id != 0 => {
                                    let request = pending.remove(&frame.request_id);
                                    if let Some(request) = request {
                                        let roots = {
                                            let mut state = self.state.lock().unwrap();
                                            let mut roots = Vec::new();
                                            for host in &request.hosts {
                                                by_host.remove(host);
                                                let id = format!("{}/{}",self.settings.pool,host);
                                                if !state.resources.contains_key(&format!("route:{id}")) {
                                                    state.roots.remove(host);
                                                    roots.push(Ref::named("route",id));
                                                }
                                            }
                                            roots
                                        };
                                        for reply in request.replies {let _=reply.send(Err(503));}
                                        self.cold_rejected.fetch_add(1,Ordering::Relaxed);
                                        if !roots.is_empty() {
                                            let unsubscribe = Frame {r#type:"unsubscribe".into(),epoch:frame.epoch.clone(),refs:roots,..Frame::default()};
                                            if socket.send(WsMessage::Binary(unsubscribe.encode_to_vec().into())).await.is_err() {break;}
                                        }
                                    }
                                    continue;
                                }
                                Err(_) => {
                                    let nack = Frame {
                                        r#type: "nack".into(),
                                        epoch: frame.epoch.clone(),
                                        nonce: frame.nonce,
                                        ..Frame::default()
                                    };
                                    let _ = socket
                                        .send(WsMessage::Binary(nack.encode_to_vec().into()))
                                        .await;
                                    break;
                                }
                            };
                            for refs in acks.chunks(128) {
                                let ack = Frame {
                                    r#type: "ack".into(),
                                    epoch: frame.epoch.clone(),
                                    nonce: frame.nonce,
                                    refs: refs.to_vec(),
                                    ..Frame::default()
                                };
                                if socket
                                    .send(WsMessage::Binary(ack.encode_to_vec().into()))
                                    .await
                                    .is_err()
                                {
                                    break 'connection;
                                }
                            }
                            if let Some(request) = pending.remove(&frame.request_id) {
                                for host in &request.hosts {
                                    by_host.remove(host);
                                }
                                for reply in request.replies {
                                    let _ = reply.send(Ok(()));
                                }
                            }
                        }
                        "error" => {
                            if let Some(request) = pending.remove(&frame.request_id) {
                                for host in &request.hosts {
                                    by_host.remove(host);
                                }
                                let epoch = self.state.lock().unwrap().epoch.clone();
                                let refs = {
                                    let mut state = self.state.lock().unwrap();
                                    request
                                        .hosts
                                        .iter()
                                        .filter_map(|host| {
                                            if state
                                                .resources
                                                .contains_key(&format!("route:{}/{}", self.settings.pool, host))
                                            {
                                                None
                                            } else {
                                                state.roots.remove(host);
                                                Some(Ref::named(
                                                    "route",
                                                    format!("{}/{}", self.settings.pool, host),
                                                ))
                                            }
                                        })
                                        .collect::<Vec<_>>()
                                };
                                if !refs.is_empty() {
                                    let frame = Frame {
                                        r#type: "unsubscribe".into(),
                                        epoch,
                                        refs,
                                        ..Frame::default()
                                    };
                                    if socket
                                        .send(WsMessage::Binary(frame.encode_to_vec().into()))
                                        .await
                                        .is_err()
                                    {
                                        break 'connection;
                                    }
                                }
                                for reply in request.replies {
                                    let _ = reply.send(Err(503));
                                }
                            }
                        }
                        _ => break,
                    }
                }
                _ = tick.tick() => {
                    if !welcomed && welcome_started.elapsed() > Duration::from_secs(3) { break; }
                    if chunk_started.is_some_and(|started| started.elapsed() > Duration::from_secs(3)) {
                        break;
                    }
                    let retired = self.retire();
                    for batch in retired.chunks(128) {
                        let epoch = self.state.lock().unwrap().epoch.clone();
                        let unsubscribe = Frame {
                            r#type: "unsubscribe".into(),
                            epoch,
                            refs: batch.to_vec(),
                            ..Frame::default()
                        };
                        if socket
                            .send(WsMessage::Binary(unsubscribe.encode_to_vec().into()))
                            .await
                            .is_err()
                        {
                            break 'connection;
                        }
                    }
                    let expired: Vec<_> = pending
                        .iter()
                        .filter(|(_, v)| Instant::now() >= v.deadline)
                        .map(|(id, _)| *id)
                        .collect();
                    for id in expired {
                        if let Some(request) = pending.remove(&id) {
                            for host in &request.hosts {
                                by_host.remove(host);
                            }
                            let epoch = self.state.lock().unwrap().epoch.clone();
                            let refs = {
                                let mut state = self.state.lock().unwrap();
                                request
                                    .hosts
                                    .iter()
                                    .filter_map(|host| {
                                        if state
                                            .resources
                                            .contains_key(&format!("route:{}/{}", self.settings.pool, host))
                                        {
                                            None
                                        } else {
                                            state.roots.remove(host);
                                            Some(Ref::named(
                                                "route",
                                                format!("{}/{}", self.settings.pool, host),
                                            ))
                                        }
                                    })
                                    .collect::<Vec<_>>()
                            };
                            if !refs.is_empty() {
                                let frame = Frame {
                                    r#type: "unsubscribe".into(),
                                    epoch,
                                    refs,
                                    ..Frame::default()
                                };
                                if socket
                                    .send(WsMessage::Binary(frame.encode_to_vec().into()))
                                    .await
                                    .is_err()
                                {
                                    break 'connection;
                                }
                            }
                            for reply in request.replies {
                                let _ = reply.send(Err(503));
                            }
                        }
                    }
                }
                        }
            }
            self.state.lock().unwrap().connected = false;
            for (_, request) in pending {
                for reply in request.replies {
                    let _ = reply.send(Err(503));
                }
            }
            sleep(
                backoff
                    + Duration::from_millis(
                        (wall_millis() as u64 + self.pod.bytes().map(u64::from).sum::<u64>()) % 250,
                    ),
            )
            .await;
            backoff = (backoff * 2).min(Duration::from_secs(5));
        }
    }
}

fn text<'a>(value: &'a Value, key: &str) -> &'a str {
    value.get(key).and_then(Value::as_str).unwrap_or_default()
}
fn wall_millis() -> i64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_millis()
        .min(i64::MAX as u128) as i64
}

// Match the existing managed-route contract: decode unreserved bytes only,
// reject encoded slashes, controls, double escaping and dot path segments.
fn managed_path(raw: &str) -> std::result::Result<String, u16> {
    if raw.contains("//") {
        return Err(400);
    }
    let bytes = raw.as_bytes();
    let mut out = Vec::with_capacity(bytes.len());
    let mut i = 0;
    while i < bytes.len() {
        if bytes[i] == b'%' {
            if i + 2 >= bytes.len() {
                return Err(400);
            }
            let a = (bytes[i + 1] as char).to_digit(16).ok_or(400u16)?;
            let b = (bytes[i + 2] as char).to_digit(16).ok_or(400u16)?;
            let value = (a * 16 + b) as u8;
            if matches!(value, 0..=31 | 127 | b'/' | b'\\' | b'%') {
                return Err(400);
            }
            if value.is_ascii_alphanumeric() || b"._~-".contains(&value) {
                out.push(value);
            } else {
                out.extend_from_slice(format!("%{value:02X}").as_bytes());
            }
            i += 3;
        } else {
            out.push(bytes[i]);
            i += 1;
        }
    }
    let path = String::from_utf8(out).map_err(|_| 400u16)?;
    if path.split('/').any(|part| part == "." || part == "..") {
        return Err(400);
    }
    Ok(path)
}

fn select_route(bindings: &[Value], raw: &str) -> std::result::Result<usize, u16> {
    if bindings.len() == 1 && text(&bindings[0], "match").is_empty() {
        return Ok(0);
    }
    let path = managed_path(raw)?;
    let mut best = None;
    for (i, route) in bindings.iter().enumerate() {
        let candidate = text(route, "path");
        match text(route, "match") {
            "exact" if candidate == path => return Ok(i),
            "prefix"
                if candidate == "/"
                    || candidate == path
                    || path
                        .strip_prefix(candidate)
                        .is_some_and(|rest| rest.starts_with('/')) =>
            {
                if best
                    .is_none_or(|old: usize| text(&bindings[old], "path").len() < candidate.len())
                {
                    best = Some(i);
                }
            }
            _ => {}
        }
    }
    best.ok_or(404)
}

pub struct AgentService(pub Arc<Agent>);

pub async fn coordinator(
    session: &mut pingora_proxy::Session,
    agent: &Arc<Agent>,
    token: &str,
) -> pingora_core::Result<bool> {
    use subtle::ConstantTimeEq;
    let authorized = session
        .req_header()
        .headers
        .get("authorization")
        .is_some_and(|v| bool::from(v.as_bytes().ct_eq(format!("Bearer {token}").as_bytes())));
    let path = session.req_header().uri.path().to_owned();
    let result = if !authorized {
        Err(403)
    } else if path == "/config/session" || path == "/config-session" {
        let proof = agent.session_proof.lock().unwrap();
        let matches = proof.len() == 64
            && session
                .req_header()
                .headers
                .get("x-krun-session-proof")
                .is_some_and(|value| bool::from(value.as_bytes().ct_eq(proof.as_bytes())));
        if !matches {
            Err(403)
        } else {
            serde_json::to_vec(&json!({"pod_uid": agent.pod, "pool": agent.settings.pool}))
                .map_err(|_| 503u16)
        }
    } else if path == "/config/head" {
        agent
            .head()
            .and_then(|value| serde_json::to_vec(&value).map_err(|_| 503u16))
    } else if let Some(handle) = path.strip_prefix("/config/bundle/") {
        agent.get_bundle(handle).map(|bundle| bundle.body.clone())
    } else if path == "/config/resolve" {
        let host = session
            .req_header()
            .headers
            .get("x-krun-host")
            .and_then(|v| v.to_str().ok())
            .unwrap_or_default()
            .to_owned();
        let route_path = session
            .req_header()
            .headers
            .get("x-krun-path")
            .and_then(|v| v.to_str().ok())
            .unwrap_or("/")
            .to_owned();
        if route_path.len() > 8192 {
            Err(400)
        } else {
            agent
                .resolve(&host, &route_path)
                .await
                .map(|bundle| bundle.body.clone())
        }
    } else {
        Err(404)
    };
    let (status, body) = match result {
        Ok(body) => (200, body),
        Err(status) => (
            status,
            serde_json::to_vec(&json!({"error":"configuration unavailable"})).unwrap(),
        ),
    };
    let mut response = pingora_http::ResponseHeader::build(status, None)?;
    response.insert_header("content-type", "application/json")?;
    response.insert_header("cache-control", "no-store")?;
    response.insert_header("content-length", body.len().to_string())?;
    session
        .write_response_header(Box::new(response), false)
        .await?;
    session.write_response_body(Some(body.into()), true).await?;
    Ok(true)
}

#[async_trait]
impl BackgroundService for AgentService {
    async fn start(&self, mut shutdown: ShutdownWatch) {
        tokio::select! {_=self.0.run()=>{},_=shutdown.changed()=>{}}
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn fixture() -> Arc<Agent> {
        let agent = Agent::new(
            Settings {
                control_url: "http://127.0.0.1/unused".into(),
                pool: "test".into(),
                token_file: String::new(),
                pod_file: String::new(),
                static_hosts: vec![],
                limits: Limits {
                    verify_millis: 5000,
                    frame_bytes: 262144,
                    resource_bytes: 4194304,
                    pod_cache_bytes: 8388608,
                    pod_cache_entries: 128,
                    pod_cold_concurrency: 4,
                },
            },
            "token".into(),
            "pod".into(),
        );
        let mut state = agent.state.lock().unwrap();
        state.epoch = "epoch".into();
        state.authorized = true;
        state.confirm(wall_millis(), true);
        state.roots.insert("site.test".into(), Instant::now());
        drop(state);
        agent
    }
    fn resource(kind: &str, id: &str, generation: u64, value: Value, deps: Vec<Ref>) -> Resource {
        Resource {
            version: Some(Ref {
                kind: kind.into(),
                id: id.into(),
                incarnation: "inc".into(),
                generation,
            }),
            deleted: false,
            payload_json: serde_json::to_vec(&value).unwrap(),
            dependencies: deps,
        }
    }
    fn graph(fixed: bool) -> Vec<Resource> {
        vec![
            resource(
                "route",
                "test/site.test",
                1,
                json!([{"worker_id":"worker","environment":"production","version":"one","enabled":true,"fixed":fixed}]),
                vec![
                    Ref::named("scope", "worker"),
                    Ref::named("deployment", "worker/production"),
                    Ref::named("version", "one"),
                ],
            ),
            resource("scope", "worker", 1, json!({"allowed":true}), vec![]),
            resource(
                "deployment",
                "worker/production",
                1,
                json!({"version":"one","enabled":true}),
                vec![Ref::named("version", "one")],
            ),
            resource(
                "version",
                "one",
                1,
                json!({"id":"one","worker_id":"worker"}),
                vec![],
            ),
        ]
    }
    fn install(agent: &Agent, resources: Vec<Resource>) {
        agent
            .install(&Frame {
                r#type: "snapshot".into(),
                epoch: "epoch".into(),
                resources,
                ..Frame::default()
            })
            .unwrap();
    }
    #[test]
    fn wire_frame_limits_error_text_by_bytes() {
        let mut frame = Frame {
            r#type: "error".into(),
            error: "é".repeat(MAX_FRAME_ERROR_BYTES / 2),
            ..Frame::default()
        };
        let encoded = frame.encode_to_vec();
        assert_eq!(
            Frame::decode_wire(encoded.as_slice()).unwrap().error,
            frame.error
        );
        frame.error.push('a');
        let encoded = frame.encode_to_vec();
        assert!(Frame::decode_wire(encoded.as_slice()).is_none());
    }

    #[test]
    fn malformed_bindings_are_rejected_without_poisoning_or_replacing_hot_routes() {
        let agent = fixture();
        install(&agent, graph(false));
        let hot = agent.build_bundle("site.test", "/").unwrap().unwrap();
        for invalid in [Value::Null, json!(true), json!(42), json!("bad"), json!([])] {
            for payload in [invalid.clone(), json!([invalid])] {
                let broken = resource("route", "test/site.test", 2, payload, vec![]);
                // An empty binding array is a valid route removal.
                if broken.payload_json == b"[]" {
                    continue;
                }
                assert_eq!(
                    agent
                        .install(&Frame {
                            epoch: "epoch".into(),
                            resources: vec![broken],
                            ..Frame::default()
                        })
                        .unwrap_err(),
                    "invalid route bindings"
                );
                assert!(agent.head().is_ok());
                assert!(Arc::ptr_eq(
                    &hot,
                    &agent.build_bundle("site.test", "/").unwrap().unwrap()
                ));
            }
        }
        // Defense in depth for malformed data already present in the cache.
        agent.state.lock().unwrap().current_bundles.clear();
        agent
            .state
            .lock()
            .unwrap()
            .resources
            .get_mut("route:test/site.test")
            .unwrap()
            .value = json!([true]);
        assert!(matches!(agent.build_bundle("site.test", "/"), Err(503)));
        assert!(agent.head().is_ok());
        install(&agent, graph(false));
        assert!(agent.build_bundle("site.test", "/").unwrap().is_some());
    }

    #[test]
    fn large_snapshot_uses_indexed_dependency_and_version_checks() {
        let mut settings = fixture().settings.clone();
        settings.limits.pod_cache_entries = 65536;
        settings.limits.pod_cache_bytes = 16777216;
        let agent = Agent::new(settings, "token".into(), "pod".into());
        {
            let mut state = agent.state.lock().unwrap();
            state.epoch = "epoch".into();
            state.authorized = true;
        }
        let resources: Vec<_> = (0..8192)
            .map(|i| {
                resource(
                    "version",
                    &format!("v{i}"),
                    1,
                    json!({}),
                    (8184..8192)
                        .map(|j| Ref::named("version", format!("v{j}")))
                        .collect(),
                )
            })
            .collect();
        let refs: Vec<_> = resources
            .iter()
            .rev()
            .filter_map(|r| r.version.clone())
            .collect();
        let frame = Frame {
            epoch: "epoch".into(),
            resources,
            refs: refs.clone(),
            ..Frame::default()
        };
        assert!(frame.encoded_len() < agent.settings.limits.resource_bytes);
        assert_eq!(agent.install(&frame).unwrap(), refs);
        assert_eq!(agent.state.lock().unwrap().resources.len(), 8192);
    }

    #[test]
    fn duplicate_and_oversize_frames_leave_the_working_set_unchanged() {
        let agent = fixture();
        install(&agent, graph(false));
        let duplicate = graph(false)[1].clone();
        let frames = [
            Frame {
                resources: vec![duplicate.clone(), duplicate],
                ..Frame::default()
            },
            Frame {
                refs: vec![Ref::named("version", "one"); 129],
                ..Frame::default()
            },
            Frame {
                resources: vec![graph(false)[1].clone(); 129],
                ..Frame::default()
            },
        ];
        for mut frame in frames {
            frame.epoch = "epoch".into();
            assert!(agent.install(&frame).is_err());
            assert_eq!(agent.state.lock().unwrap().resources.len(), 4);
            assert!(agent.build_bundle("site.test", "/").unwrap().is_some());
        }
    }

    #[test]
    fn fixed_version_survives_default_switch_but_observes_disable() {
        let agent = fixture();
        install(&agent, graph(true));
        install(
            &agent,
            vec![
                resource(
                    "deployment",
                    "worker/production",
                    2,
                    json!({"version":"two","enabled":true}),
                    vec![Ref::named("version", "two")],
                ),
                resource(
                    "version",
                    "two",
                    1,
                    json!({"id":"two","worker_id":"worker"}),
                    vec![],
                ),
            ],
        );
        let bundle = agent.build_bundle("site.test", "/").unwrap().unwrap();
        let body: Value = serde_json::from_slice(&bundle.body).unwrap();
        assert_eq!(body["route"]["version"], "one");
        install(
            &agent,
            vec![resource(
                "deployment",
                "worker/production",
                3,
                json!({"version":"two","enabled":false}),
                vec![],
            )],
        );
        assert!(matches!(agent.build_bundle("site.test", "/"), Err(410)));
    }
    #[test]
    fn fixed_route_does_not_require_the_current_default_descriptor() {
        let agent = fixture();
        install(&agent, graph(true));
        let deployment = resource(
            "deployment",
            "worker/production",
            2,
            json!({"version":"missing-default","enabled":true}),
            vec![Ref::named("version", "missing-default")],
        );
        let mut missing = resource("version", "missing-default", 1, Value::Null, vec![]);
        missing.deleted = true;
        install(&agent, vec![deployment, missing]);
        let bundle = agent.build_bundle("site.test", "/").unwrap().unwrap();
        let body: Value = serde_json::from_slice(&bundle.body).unwrap();
        assert_eq!(body["route"]["version"], "one");
        assert!(body["versions"].get("missing-default").is_none());
    }

    #[test]
    fn offline_hot_bundle_survives_and_retirement_pauses() {
        let agent = fixture();
        install(&agent, graph(false));
        let before = agent.build_bundle("site.test", "/").unwrap().unwrap();
        {
            let mut state = agent.state.lock().unwrap();
            state.connected = false;
            *state.roots.get_mut("site.test").unwrap() = Instant::now() - Duration::from_secs(600);
        }
        assert!(agent.retire().is_empty());
        let after = agent.build_bundle("site.test", "/").unwrap().unwrap();
        assert_eq!(before.handle, after.handle);
        agent.fence(None);
        assert!(matches!(agent.build_bundle("site.test", "/"), Err(503)));
    }
    #[test]
    fn heartbeat_does_not_renew_old_primary_proof() {
        let agent = fixture();
        let mut state = agent.state.lock().unwrap();
        state.checked = Some(Instant::now() - Duration::from_secs(30));
        let stamp = state.authority_stamp;
        state.confirm(stamp, true);
        assert!(!state.fresh());
    }
    #[test]
    fn snapshot_rejects_missing_dependencies_without_partial_install() {
        let agent = fixture();
        install(&agent, graph(false));
        let broken = resource(
            "deployment",
            "worker/production",
            2,
            json!({"version":"missing","enabled":true}),
            vec![Ref::named("version", "missing")],
        );
        assert!(
            agent
                .install(&Frame {
                    epoch: "epoch".into(),
                    resources: vec![broken],
                    ..Frame::default()
                })
                .is_err()
        );
        assert_eq!(
            agent.state.lock().unwrap().resources["deployment:worker/production"]
                .wire
                .version
                .as_ref()
                .unwrap()
                .generation,
            1
        );
    }
    #[test]
    fn late_generation_cannot_replace_current_configuration() {
        let agent = fixture();
        install(&agent, graph(false));
        install(
            &agent,
            vec![resource(
                "deployment",
                "worker/production",
                2,
                json!({"version":"one","enabled":false}),
                vec![],
            )],
        );
        install(&agent, graph(false));
        assert!(matches!(agent.build_bundle("site.test", "/"), Err(410)));
    }
    #[test]
    fn retirement_collects_unused_graph_and_preserves_inflight_bundle() {
        let agent = fixture();
        install(&agent, graph(false));
        let bundle = agent.build_bundle("site.test", "/").unwrap().unwrap();
        agent.state.lock().unwrap().roots.insert(
            "site.test".into(),
            Instant::now() - Duration::from_secs(600),
        );
        assert!(agent.retire().is_empty());
        drop(bundle);
        let refs = agent.retire();
        assert_eq!(refs.len(), 1);
        assert!(agent.state.lock().unwrap().resources.is_empty());
    }
    #[test]
    fn managed_routes_keep_path_boundary_and_encoding_rules() {
        let rules = vec![
            json!({"match":"prefix","path":"/api"}),
            json!({"match":"exact","path":"/api/users"}),
        ];
        assert_eq!(select_route(&rules, "/api/users").unwrap(), 1);
        assert_eq!(select_route(&rules, "/api/list").unwrap(), 0);
        assert_eq!(select_route(&rules, "/apis"), Err(404));
        for path in ["/api%2Fusers", "/api/%252e", "/api/../users", "/api//users"] {
            assert_eq!(select_route(&rules, path), Err(400));
        }
    }
    #[test]
    fn hot_handles_are_reused_and_only_changed_dependencies_are_fenced() {
        let agent = fixture();
        install(&agent, graph(false));
        install(
            &agent,
            vec![
                resource(
                    "route",
                    "test/other.test",
                    1,
                    json!([{"worker_id":"other","environment":"production","version":"two","enabled":true}]),
                    vec![
                        Ref::named("scope", "other"),
                        Ref::named("deployment", "other/production"),
                    ],
                ),
                resource("scope", "other", 1, json!({"allowed":true}), vec![]),
                resource(
                    "deployment",
                    "other/production",
                    1,
                    json!({"version":"two","enabled":true}),
                    vec![Ref::named("version", "two")],
                ),
                resource(
                    "version",
                    "two",
                    1,
                    json!({"id":"two","worker_id":"other"}),
                    vec![],
                ),
            ],
        );
        let original = agent.build_bundle("site.test", "/").unwrap().unwrap();
        let other = agent.build_bundle("other.test", "/").unwrap().unwrap();
        for _ in 0..10000 {
            assert!(Arc::ptr_eq(
                &original,
                &agent.build_bundle("site.test", "/").unwrap().unwrap()
            ));
        }
        install(
            &agent,
            vec![resource(
                "deployment",
                "worker/production",
                2,
                json!({"version":"one","enabled":false}),
                vec![],
            )],
        );
        {
            let state = agent.state.lock().unwrap();
            assert!(!state.current_bundles.contains_key(&("site.test".into(), 0)));
            assert!(
                state
                    .current_bundles
                    .contains_key(&("other.test".into(), 0))
            );
        }
        assert!(matches!(agent.build_bundle("site.test", "/"), Err(410)));
        assert!(Arc::ptr_eq(
            &other,
            &agent.build_bundle("other.test", "/").unwrap().unwrap()
        ));
        // Re-sending the same descriptor must not invalidate a hot handle.
        install(
            &agent,
            vec![resource(
                "scope",
                "other",
                1,
                json!({"allowed":true}),
                vec![],
            )],
        );
        assert!(
            agent
                .state
                .lock()
                .unwrap()
                .current_bundles
                .contains_key(&("other.test".into(), 0))
        );
    }

    #[test]
    fn cold_admission_preserves_headroom_and_existing_hot_graph() {
        let agent = fixture();
        install(&agent, graph(false));
        let bundle = agent.build_bundle("site.test", "/").unwrap().unwrap();
        // Four initial resources fit at 75% of a six-entry budget.
        let mut settings = agent.settings.clone();
        settings.limits.pod_cache_entries = 6;
        let bounded = Agent::new(settings, "token".into(), "pod".into());
        {
            let mut state = bounded.state.lock().unwrap();
            state.epoch = "epoch".into();
            state.authorized = true;
            state.confirm(wall_millis(), true);
        }
        install(&bounded, graph(false));
        let next = resource("route", "test/new.test", 1, json!([]), vec![]);
        assert!(
            bounded
                .install(&Frame {
                    epoch: "epoch".into(),
                    resources: vec![next],
                    ..Frame::default()
                })
                .is_err()
        );
        assert!(bounded.build_bundle("site.test", "/").unwrap().is_some());
        assert!(
            !bounded
                .state
                .lock()
                .unwrap()
                .resources
                .contains_key("route:test/new.test")
        );
        assert!(!bundle.body.is_empty());
    }

    #[tokio::test]
    async fn binary_stream_cold_load_chunking_and_offline_hot_service() {
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let address = listener.local_addr().unwrap();
        let mut settings = fixture().settings.clone();
        settings.control_url = format!("http://{address}/watch");
        let agent = Agent::new(settings, "token".into(), "pod".into());
        let server = tokio::spawn(async move {
            let (tcp, _) = listener.accept().await.unwrap();
            let mut socket = tokio_tungstenite::accept_async(tcp).await.unwrap();
            let hello = socket.next().await.unwrap().unwrap();
            let WsMessage::Binary(hello) = hello else {
                panic!("hello must be binary")
            };
            assert_eq!(Frame::decode(hello).unwrap().r#type, "hello");
            let welcome = Frame {
                r#type: "welcome".into(),
                epoch: "epoch".into(),
                nonce: 1,
                confirmed_unix_millis: wall_millis(),
                fresh: true,
                ..Frame::default()
            };
            socket
                .send(WsMessage::Binary(welcome.encode_to_vec().into()))
                .await
                .unwrap();
            loop {
                let WsMessage::Binary(data) = socket.next().await.unwrap().unwrap() else {
                    continue;
                };
                let request = Frame::decode(data).unwrap();
                if request.r#type != "resolve" {
                    continue;
                }
                assert_eq!(request.refs[0].id, "test/site.test");
                let resources = graph(false);
                let snapshot = Frame {
                    r#type: "snapshot".into(),
                    epoch: "epoch".into(),
                    request_id: request.request_id,
                    nonce: 2,
                    refs: resources.iter().filter_map(|r| r.version.clone()).collect(),
                    resources,
                    ..Frame::default()
                };
                let raw = snapshot.encode_to_vec();
                let parts = raw.len().div_ceil(128);
                for (i, chunk) in raw.chunks(128).enumerate() {
                    let frame = Frame {
                        r#type: "chunk".into(),
                        part: i as u32,
                        parts: parts as u32,
                        nonce: 2,
                        chunk: chunk.to_vec(),
                        ..Frame::default()
                    };
                    socket
                        .send(WsMessage::Binary(frame.encode_to_vec().into()))
                        .await
                        .unwrap();
                }
                // Allow the consumer to ACK before intentionally dropping transport.
                let _ = timeout(Duration::from_secs(1), socket.next()).await;
                break;
            }
        });
        let runner = {
            let agent = agent.clone();
            tokio::spawn(async move { agent.run().await })
        };
        for _ in 0..100 {
            if agent.head().is_ok() {
                break;
            }
            sleep(Duration::from_millis(10)).await;
        }
        let bundle = agent.resolve("site.test", "/").await.unwrap();
        let value: Value = serde_json::from_slice(&bundle.body).unwrap();
        assert_eq!(value["route"]["version"], "one");
        server.await.unwrap();
        sleep(Duration::from_millis(30)).await;
        assert!(!agent.state.lock().unwrap().connected);
        assert_eq!(
            agent.resolve("site.test", "/").await.unwrap().handle,
            bundle.handle
        );
        runner.abort();
    }
}
