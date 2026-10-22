const $ = (id) => document.getElementById(id);
const sensitive = new Set([
  "authorization",
  "cookie",
  "proxy-authorization",
  "x-api-key",
  "x-csrf-token",
]);
let current = null;

function cell(dl, label, value, code = false) {
  const dt = document.createElement("dt");
  dt.textContent = label;
  const dd = document.createElement("dd");
  if (code) {
    const el = document.createElement("code");
    el.textContent = value;
    dd.append(el);
  } else {
    dd.textContent = value;
  }
  dl.append(dt, dd);
}

function revealOn() {
  return $("reveal").checked;
}

function render(data) {
  current = data;
  const request = $("request-meta");
  request.replaceChildren();
  cell(request, "Method", data.method);
  cell(request, "URL", data.url);
  cell(request, "Received", data.received_at);
  if (data.cf) cell(request, "CF", JSON.stringify(data.cf));

  const env = $("env-meta");
  env.replaceChildren();
  for (const [key, value] of Object.entries(data.environment || {})) {
    cell(env, key, value === null ? "—" : String(value), true);
  }

  const names = Object.keys(data.headers || {}).sort((a, b) => a.localeCompare(b));
  const body = $("headers");
  body.replaceChildren();
  for (const name of names) {
    const tr = document.createElement("tr");
    const nameCell = document.createElement("td");
    const nameCode = document.createElement("code");
    nameCode.textContent = name;
    nameCell.append(nameCode);
    const valueCell = document.createElement("td");
    const valueCode = document.createElement("code");
    const hidden = sensitive.has(name.toLowerCase()) && !revealOn();
    valueCode.textContent = hidden ? "••••••（勾选“显示敏感 header”）" : data.headers[name];
    valueCode.classList.toggle("muted", hidden);
    valueCell.append(valueCode);
    tr.append(nameCell, valueCell);
    body.append(tr);
  }
  $("header-count").textContent = names.length + " headers";
  $("release").textContent = data.environment?.KRUN_RELEASE_ID
    ? "release " + data.environment.KRUN_RELEASE_ID.slice(0, 16)
    : "release —";
}

function status(text, error = false) {
  const el = $("status");
  el.textContent = text;
  el.classList.toggle("error", error);
}

async function load(extraHeaders = {}) {
  status("请求 /headers …");
  try {
    const response = await fetch("/headers", {
      headers: extraHeaders,
      cache: "no-store",
    });
    if (!response.ok) throw new Error("HTTP " + response.status);
    render(await response.json());
    status("已更新 · " + new Date().toLocaleTimeString());
  } catch (error) {
    status("请求失败：" + error.message, true);
  }
}

$("refresh").addEventListener("click", () => load());
$("reveal").addEventListener("change", () => {
  if (current) render(current);
});
$("copy").addEventListener("click", async () => {
  if (!current) return;
  try {
    await navigator.clipboard.writeText(JSON.stringify(current, null, 2));
    status("已复制 JSON");
  } catch {
    status("复制失败，请手动选择表格内容", true);
  }
});
$("probe").addEventListener("submit", (event) => {
  event.preventDefault();
  const name = $("probe-name").value.trim();
  const value = $("probe-value").value;
  if (!name) {
    status("header 名称不能为空", true);
    return;
  }
  try {
    new Headers({ [name]: value });
  } catch {
    status("header 名称或值不合法", true);
    return;
  }
  load({ [name]: value });
});

load();
