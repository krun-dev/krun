export function json(body, init = {}) {
  const headers = new Headers(init.headers);
  headers.set("content-type", "application/json; charset=utf-8");
  headers.set("cache-control", "no-store");
  return new Response(JSON.stringify(body), { ...init, headers });
}

export function methodNotAllowed(allow) {
  return json(
    { error: "method_not_allowed", allow },
    { status: 405, headers: { allow } },
  );
}
