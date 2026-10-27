const status = document.querySelector("#api-status");
const environment = document.querySelector("#environment");
const release = document.querySelector("#release");
const route = document.querySelector("#current-route");
const form = document.querySelector("#echo-form");
const result = document.querySelector("#echo-result");

route.textContent = window.location.pathname;

async function loadHealth() {
  try {
    const response = await fetch("/api/health", { headers: { accept: "application/json" } });
    if (!response.ok) throw new Error(`HTTP ${response.status}`);
    const data = await response.json();
    status.textContent = data.ok ? "API 已连通" : "API 返回异常";
    status.dataset.state = data.ok ? "ok" : "error";
    environment.textContent = data.environment;
    release.textContent = data.release_id;
  } catch (error) {
    status.textContent = `API 请求失败：${error.message}`;
    status.dataset.state = "error";
  }
}

form.addEventListener("submit", async (event) => {
  event.preventDefault();
  const message = new FormData(form).get("message")?.toString() ?? "";
  result.textContent = "发送中…";
  try {
    const response = await fetch("/api/echo", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ message }),
    });
    const data = await response.json();
    if (!response.ok) throw new Error(data.error ?? `HTTP ${response.status}`);
    result.textContent = data.echo;
  } catch (error) {
    result.textContent = `请求失败：${error.message}`;
  }
});

await loadHealth();
