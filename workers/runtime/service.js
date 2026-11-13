const servicePrefix = "x-krun-service-";
const brokerURL = "http://platform/api/internal/workers/service-invoke/";
const requestURL = servicePrefix + "url";
const appAuthorization = servicePrefix + "app-authorization";
const maxURLLength = 2048;

function businessHeaders(incoming) {
  const headers = new Headers(incoming);
  for (const name of [...headers.keys()]) {
    if (
      name.startsWith(servicePrefix) ||
      [
        "authorization",
        "host",
        "x-runtime-pool",
        "x-krun-version",
        "x-krun-pod",
        "x-krun-scope",
        "x-forwarded-host",
        "x-internal",
        "cloudflare-workers-version-overrides",
        "krun-workers-verify-authorization",
      ].includes(name)
    ) {
      headers.delete(name);
    }
  }
  return headers;
}

function streamingRequest(url, original, headers) {
  return new Request(url, {
    method: original.method,
    headers,
    body: original.body,
    duplex: "half",
    signal: original.signal,
    redirect: "manual",
  });
}

export function serviceCall(env, props, request) {
  if (env.VALIDATOR_ONLY === "true") {
    return new Response("Service bindings are unavailable during validation", { status: 403 });
  }
  if (request.url.length > maxURLLength) {
    return new Response("Service request URL is too long", { status: 414 });
  }
  const headers = businessHeaders(request.headers);
  const authorization = request.headers.get("authorization");
  if (authorization !== null) {
    headers.set(appAuthorization, authorization);
  }
  headers.set(requestURL, request.url);
  headers.set("authorization", "Bearer " + env.RUNTIME_TOKEN);
  headers.set("x-runtime-pool", env.RUNTIME_POOL || "default");

  const target =
    brokerURL + encodeURIComponent(props.callerVersion) + "/" + encodeURIComponent(props.binding);
  return env.PLATFORM.fetch(streamingRequest(target, request, headers));
}

export function serviceRequest(request) {
  const rawURL = request.headers.get(requestURL);
  if (!rawURL || rawURL.length > maxURLLength) {
    throw new Error("Invalid service request URL");
  }
  const url = new URL(rawURL);
  if (!["http:", "https:"].includes(url.protocol) || url.username || url.password) {
    throw new Error("Invalid service request URL");
  }
  const headers = businessHeaders(request.headers);
  const authorization = request.headers.get(appAuthorization);
  if (authorization !== null) {
    headers.set("authorization", authorization);
  }
  return streamingRequest(url, request, headers);
}
