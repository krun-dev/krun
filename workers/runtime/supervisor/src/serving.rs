use std::collections::{BTreeMap, HashSet};
use std::fmt::Write as _;
use std::io;
use std::net::{SocketAddr, TcpListener};
use std::os::fd::AsRawFd;
use std::os::unix::fs::OpenOptionsExt;
use std::os::unix::process::CommandExt;
use std::path::Path;
use std::process::{Command, Stdio};
use std::sync::Mutex;
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::Duration;

use pingora_core::Result;
use pingora_http::ResponseHeader;
use pingora_proxy::Session;
use subtle::ConstantTimeEq;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::TcpStream;
use tokio::task::JoinSet;
use tokio::time::{Instant, timeout};

use crate::{Pool, admission::ColdPermit};

const SLOT_MARKER: &str = "krun-unassigned-slot";
const MANAGEMENT_TIMEOUT: Duration = Duration::from_millis(700);
const MAX_RESPONSE: u64 = 8 * 1024 * 1024;

pub struct Settings {
    pub compute_hosts: HashSet<String>,
    pub management_token: String,
    pub last_ack_grant: Mutex<Option<(Cursor, Instant)>>,
    pub cold: ColdLoads,
}

#[derive(Default)]
pub struct ColdLoads {
    next: AtomicU64,
    permits: Mutex<BTreeMap<u64, (String, Instant, ColdPermit)>>,
}

impl ColdLoads {
    pub fn clear(&self, slot: &str) {
        self.permits
            .lock()
            .unwrap()
            .retain(|_, (owner, _, _)| owner != slot);
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Cursor {
    epoch: String,
    revision: u64,
}

#[derive(Clone, Copy)]
pub struct ChildSockets {
    pub private: SocketAddr,
    pub management: SocketAddr,
    pub metrics: SocketAddr,
}

pub fn spawn(
    workerd: &Path,
    config: &Path,
    slot: &str,
) -> io::Result<(tokio::process::Child, ChildSockets)> {
    let text = std::fs::read_to_string(config)?;
    if !text.contains(SLOT_MARKER) {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "serving config has no slot marker",
        ));
    }
    let child_config =
        std::env::temp_dir().join(format!("krun-{}-{slot}.capnp", std::process::id()));
    let mut file = std::fs::OpenOptions::new()
        .write(true)
        .create(true)
        .truncate(true)
        .mode(0o600)
        .custom_flags(libc::O_NOFOLLOW)
        .open(&child_config)?;
    std::io::Write::write_all(&mut file, text.replace(SLOT_MARKER, slot).as_bytes())?;

    let listeners = [
        TcpListener::bind("127.0.0.1:0")?,
        TcpListener::bind("127.0.0.1:0")?,
        TcpListener::bind("127.0.0.1:0")?,
    ];
    let sockets = ChildSockets {
        private: listeners[0].local_addr()?,
        management: listeners[1].local_addr()?,
        metrics: listeners[2].local_addr()?,
    };
    let fds = listeners.each_ref().map(AsRawFd::as_raw_fd);
    let mut command = Command::new(workerd);
    command
        .arg("serve")
        .arg(child_config)
        .args(["-I", "/", "--experimental"])
        .stdin(Stdio::null());
    for (name, fd) in ["private", "management", "metrics"].into_iter().zip(fds) {
        command.arg(format!("--socket-fd={name}={fd}"));
    }
    // Only the sockets for this child are made inheritable after fork.
    unsafe {
        command.pre_exec(move || {
            for fd in fds {
                let flags = libc::fcntl(fd, libc::F_GETFD);
                if flags < 0 || libc::fcntl(fd, libc::F_SETFD, flags & !libc::FD_CLOEXEC) < 0 {
                    return Err(io::Error::last_os_error());
                }
            }
            Ok(())
        });
    }
    let child = tokio::process::Command::from(command)
        .kill_on_drop(true)
        .spawn()?;
    eprintln!("start {slot} pid={}", child.id().unwrap());
    Ok((child, sockets))
}

struct LocalResponse {
    headers: BTreeMap<String, String>,
    body: Vec<u8>,
}

async fn request(address: SocketAddr, path: &str) -> Option<LocalResponse> {
    timeout(MANAGEMENT_TIMEOUT, async {
        let mut stream = TcpStream::connect(address).await.ok()?;
        let request =
            format!("GET {path} HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n");
        stream.write_all(request.as_bytes()).await.ok()?;
        let mut data = Vec::new();
        stream
            .take(MAX_RESPONSE + 1)
            .read_to_end(&mut data)
            .await
            .ok()?;
        if data.len() as u64 > MAX_RESPONSE {
            return None;
        }
        let mut parsed = [httparse::EMPTY_HEADER; 32];
        let mut response = httparse::Response::new(&mut parsed);
        let httparse::Status::Complete(offset) = response.parse(&data).ok()? else {
            return None;
        };
        if response.code != Some(200) {
            return None;
        }
        let headers = response
            .headers
            .iter()
            .filter_map(|header| {
                Some((
                    header.name.to_ascii_lowercase(),
                    std::str::from_utf8(header.value).ok()?.to_owned(),
                ))
            })
            .collect::<BTreeMap<_, _>>();
        let body = if headers
            .get("transfer-encoding")
            .is_some_and(|value| value.eq_ignore_ascii_case("chunked"))
        {
            chunk_body(&data[offset..])?
        } else {
            data[offset..].to_vec()
        };
        Some(LocalResponse { headers, body })
    })
    .await
    .ok()?
}

fn chunk_body(mut input: &[u8]) -> Option<Vec<u8>> {
    let mut body = Vec::new();
    loop {
        let split = input.windows(2).position(|pair| pair == b"\r\n")?;
        let line = std::str::from_utf8(&input[..split]).ok()?;
        let size = usize::from_str_radix(line.split(';').next()?, 16).ok()?;
        if size == 0 {
            return Some(body);
        }
        input = input.get(split + 2..)?;
        body.extend_from_slice(input.get(..size)?);
        if input.get(size..size.checked_add(2)?)? != b"\r\n" {
            return None;
        }
        input = input.get(size + 2..)?;
    }
}

fn cursor(response: &LocalResponse) -> Option<Cursor> {
    let epoch = response.headers.get("x-krun-config-epoch")?;
    if epoch.len() != 32 || !epoch.bytes().all(|byte| byte.is_ascii_hexdigit()) {
        return None;
    }
    Some(Cursor {
        epoch: epoch.to_owned(),
        revision: response
            .headers
            .get("x-krun-config-revision")?
            .parse()
            .ok()?,
    })
}

pub async fn probe_ready(address: SocketAddr) -> bool {
    request(address, "/ready")
        .await
        .and_then(|response| cursor(&response))
        .is_some()
}

fn sockets(pool: &Pool) -> Vec<(String, String, ChildSockets)> {
    pool.slots
        .iter()
        .filter_map(|slot| {
            let endpoint = slot.endpoint.read().unwrap();
            let sockets = endpoint.as_ref()?.serving?;
            Some((slot.name.clone(), slot.group.name().to_owned(), sockets))
        })
        .collect()
}

async fn converged(pool: &Pool) -> Option<(Cursor, Vec<u8>)> {
    let sockets = sockets(pool);
    if sockets.len() != pool.slots.len() {
        return None;
    }
    let mut probes = JoinSet::new();
    for (_, _, socket) in sockets {
        probes.spawn(request(socket.management, "/ready"));
    }
    let first = probes.join_next().await?.ok()??;
    let mut oldest = (cursor(&first)?, first.body);
    while let Some(probe) = probes.join_next().await {
        let response = probe.ok()??;
        let current = cursor(&response)?;
        if current.epoch != oldest.0.epoch {
            return None;
        }
        if current.revision < oldest.0.revision {
            oldest = (current, response.body);
        }
    }
    Some(oldest)
}

pub async fn readiness(session: &mut Session, pool: &Pool) -> Result<()> {
    let sampled = pool.policy.as_ref().is_some_and(|policy| {
        !matches!(policy.check_cpu(), Err(crate::Rejection::PodCpuUnavailable))
            && pool.slots.iter().all(|slot| {
                slot.endpoint
                    .read()
                    .unwrap()
                    .as_ref()
                    .is_some_and(|endpoint| {
                        !matches!(
                            endpoint.cpu.read().unwrap().check(policy.sample_interval),
                            Err(crate::Rejection::WorkerdCpuUnavailable)
                        )
                    })
            })
    });
    let applied = if sampled { converged(pool).await } else { None };
    let (status, body) = match applied {
        Some((_, body)) => (200, body),
        None => (
            503,
            br#"{"error":"runtime configuration not converged"}"#.to_vec(),
        ),
    };
    let mut header = ResponseHeader::build(status, None)?;
    header.insert_header("Content-Type", "application/json")?;
    header.insert_header("Content-Length", body.len().to_string())?;
    session
        .write_response_header(Box::new(header), false)
        .await?;
    session.write_response_body(Some(body.into()), true).await
}

fn authorized(session: &Session, settings: &Settings) -> bool {
    session
        .req_header()
        .headers
        .get("authorization")
        .and_then(|value| value.to_str().ok())
        .and_then(|value| value.strip_prefix("Bearer "))
        .is_some_and(|token| {
            bool::from(token.as_bytes().ct_eq(settings.management_token.as_bytes()))
        })
}

pub async fn cold_load(session: &mut Session, pool: &Pool) -> Result<()> {
    let settings = pool.serving.as_ref().expect("serving coordinator");
    let headers = &session.req_header().headers;
    let slot = headers
        .get("x-krun-workerd-slot")
        .and_then(|value| value.to_str().ok());
    let slot = slot.and_then(|name| pool.slots.iter().find(|slot| slot.name == name));
    let (status, body) = if let Some(slot) = slot.filter(|_| authorized(session, settings)) {
        let releasing = headers
            .get("x-krun-cold-permit")
            .and_then(|value| value.to_str().ok())
            .and_then(|value| value.parse::<u64>().ok());
        let reserve = session.req_header().uri.path() == "/cold/reserve";
        let tracked = headers
            .get("x-krun-workerd-tracked")
            .and_then(|value| value.to_str().ok())
            .and_then(|value| value.parse::<usize>().ok());
        let mut permits = settings.cold.permits.lock().unwrap();
        permits.retain(|_, (_, at, _)| at.elapsed() < Duration::from_secs(120));
        if let Some(tracked) = tracked.filter(|_| reserve) {
            let policy = pool.policy.as_ref().expect("serving admission");
            let permit = if tracked > policy.max_loaded {
                Err(crate::Rejection::WorkerdLoaded)
            } else {
                policy.cold_permit()
            };
            match permit {
                Ok(permit) => {
                    let id = settings.cold.next.fetch_add(1, Ordering::Relaxed);
                    permits.insert(id, (slot.name.clone(), Instant::now(), permit));
                    slot.cold.fetch_add(1, Ordering::Relaxed);
                    (200, id.to_string())
                }
                Err(reason) => {
                    slot.rejected_by_reason.record(reason);
                    (503, reason.name().to_owned())
                }
            }
        } else if let Some(id) = releasing.filter(|_| !reserve) {
            if permits
                .get(&id)
                .is_some_and(|(owner, _, _)| owner == &slot.name)
            {
                permits.remove(&id);
            }
            (200, String::new())
        } else {
            (400, String::new())
        }
    } else {
        (403, String::new())
    };
    crate::respond(session, status, body, false).await
}

pub async fn grant_ack(session: &mut Session, pool: &Pool) -> Result<()> {
    let settings = pool.serving.as_ref().expect("serving coordinator");
    if !authorized(session, settings) {
        return crate::respond(session, 403, String::new(), false).await;
    }
    let headers = &session.req_header().headers;
    let requested = headers
        .get("x-krun-config-epoch")
        .and_then(|value| value.to_str().ok())
        .zip(
            headers
                .get("x-krun-config-revision")
                .and_then(|value| value.to_str().ok())
                .and_then(|value| value.parse().ok()),
        )
        .map(|(epoch, revision)| Cursor {
            epoch: epoch.to_owned(),
            revision,
        });
    let granted = if let Some((actual, _)) = converged(pool).await
        && requested.as_ref() == Some(&actual)
    {
        let mut last = settings.last_ack_grant.lock().unwrap();
        if last
            .as_ref()
            .is_some_and(|(cursor, at)| cursor == &actual && at.elapsed() < Duration::from_secs(3))
        {
            false
        } else {
            *last = Some((actual, Instant::now()));
            true
        }
    } else {
        false
    };
    crate::respond(
        session,
        if granted { 200 } else { 409 },
        String::new(),
        false,
    )
    .await
}

pub fn management_address(pool: &Pool, host: &str) -> Option<SocketAddr> {
    let slots = &pool.slots[..pool.ordinary];
    let hash = host.bytes().fold(0x811c9dc5u32, |hash, byte| {
        (hash ^ u32::from(byte)).wrapping_mul(0x01000193)
    });
    (0..slots.len()).find_map(|offset| {
        let slot = &slots[(hash as usize + offset) % slots.len()];
        Some(slot.endpoint.read().unwrap().as_ref()?.serving?.management)
    })
}

pub async fn metrics(pool: &Pool) -> String {
    let mut scrapes = JoinSet::new();
    for (slot, group, sockets) in sockets(pool) {
        scrapes.spawn(async move { (slot, group, request(sockets.metrics, "/metrics").await) });
    }
    let mut samples = String::new();
    let mut config = BTreeMap::<String, f64>::new();
    let mut complete = 0;
    while let Some(scrape) = scrapes.join_next().await {
        let Ok((slot, group, Some(response))) = scrape else {
            continue;
        };
        let Ok(body) = std::str::from_utf8(&response.body) else {
            continue;
        };
        complete += 1;
        for line in body.lines() {
            if line.starts_with('#') || line.trim().is_empty() {
                continue;
            }
            let Some((series, value)) = line.rsplit_once(' ') else {
                continue;
            };
            if series.starts_with("krun_runtime_config_") {
                if let Ok(value) = value.parse::<f64>() {
                    config
                        .entry(series.to_owned())
                        .and_modify(|current| {
                            *current = if series == "krun_runtime_config_fresh" {
                                current.min(value)
                            } else {
                                current.max(value)
                            };
                        })
                        .or_insert(value);
                }
                continue;
            }
            let labels = format!("workerd_slot=\"{slot}\",execution_group=\"{group}\"");
            if let Some(series) = series.strip_suffix('}') {
                writeln!(samples, "{series},{labels}}} {value}").unwrap();
            } else {
                writeln!(samples, "{series}{{{labels}}} {value}").unwrap();
            }
        }
    }
    if complete != pool.slots.len() || config.get("krun_runtime_config_fresh") != Some(&1.0) {
        for name in ["fresh", "ack_current", "converged"] {
            config.insert(format!("krun_runtime_config_{name}"), 0.0);
        }
    }
    if config
        .get("krun_runtime_config_revision_lag")
        .copied()
        .unwrap_or_default()
        > 0.0
    {
        config.insert("krun_runtime_config_ack_current".into(), 0.0);
        config.insert("krun_runtime_config_converged".into(), 0.0);
    }
    for (name, value) in config {
        writeln!(samples, "{name} {value}").unwrap();
    }
    samples
}
