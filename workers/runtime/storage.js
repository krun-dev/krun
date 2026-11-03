// Only the trusted entry worker signs configuration reads. User Workers receive
// neither these credentials nor the entry worker's outbound network capability.
const encoder = new TextEncoder();

const hex = (bytes) =>
  [...new Uint8Array(bytes)].map((b) => b.toString(16).padStart(2, "0")).join("");

const sha256 = async (text) => hex(await crypto.subtle.digest("SHA-256", encoder.encode(text)));

const hmac = async (key, text) => {
  const imported = await crypto.subtle.importKey(
    "raw",
    typeof key === "string" ? encoder.encode(key) : key,
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"],
  );

  return crypto.subtle.sign("HMAC", imported, encoder.encode(text));
};

const encode = (value) =>
  encodeURIComponent(value).replace(
    /[!'()*]/g,
    (c) => "%" + c.charCodeAt(0).toString(16).toUpperCase(),
  );

export async function configurationRequest(env, input, timeout, now = new Date()) {
  const url = new URL(input);
  const base = new URL(env.CONFIG_BASE);

  if (
    url.origin !== base.origin ||
    !url.pathname.startsWith(base.pathname.replace(/\/$/, "") + "/") ||
    url.username ||
    url.password ||
    url.search ||
    url.hash
  ) {
    throw new Error("configuration URL outside storage prefix");
  }

  const headers = new Headers();

  if (env.STORAGE_ACCESS_KEY || env.STORAGE_SECRET_KEY) {
    if (!env.STORAGE_ACCESS_KEY || !env.STORAGE_SECRET_KEY || !env.STORAGE_REGION) {
      throw new Error("incomplete configuration storage credentials");
    }

    const stamp = now.toISOString().replace(/[:-]|\.\d{3}/g, "");
    const day = stamp.slice(0, 8);
    const region = env.STORAGE_REGION;
    const payload = await sha256("");
    const path = url.pathname
      .split("/")
      .map((part) => encode(decodeURIComponent(part)))
      .join("/");
    const canonical = [
      "GET",
      path,
      "",
      `host:${url.host}\nx-amz-content-sha256:${payload}\nx-amz-date:${stamp}\n`,
      "host;x-amz-content-sha256;x-amz-date",
      payload,
    ].join("\n");
    const scope = day + "/" + region + "/s3/aws4_request";
    let key = "AWS4" + env.STORAGE_SECRET_KEY;

    for (const value of [day, region, "s3", "aws4_request"]) {
      key = await hmac(key, value);
    }

    const signature = hex(
      await hmac(
        key,
        "AWS4-HMAC-SHA256\n" + stamp + "\n" + scope + "\n" + (await sha256(canonical)),
      ),
    );
    headers.set("x-amz-date", stamp);
    headers.set("x-amz-content-sha256", payload);
    headers.set(
      "authorization",
      `AWS4-HMAC-SHA256 Credential=${env.STORAGE_ACCESS_KEY}/${scope}, SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature=${signature}`,
    );
  }

  return new Request(url, { headers, redirect: "manual", signal: AbortSignal.timeout(timeout) });
}

export async function fetchConfiguration(env, url, timeout) {
  return fetch(await configurationRequest(env, url, timeout));
}
