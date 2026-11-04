const encoder = new TextEncoder();
export const encode = (value) =>
  encodeURIComponent(value).replace(
    /[!'()*]/g,
    (c) => "%" + c.charCodeAt(0).toString(16).toUpperCase(),
  );
const hex = (value) =>
  [...new Uint8Array(value)].map((v) => v.toString(16).padStart(2, "0")).join("");
export const digest = async (value) =>
  hex(await crypto.subtle.digest("SHA-256", encoder.encode(value)));
export async function hmac(key, value) {
  const imported = await crypto.subtle.importKey(
    "raw",
    typeof key === "string" ? encoder.encode(key) : key,
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"],
  );
  return crypto.subtle.sign("HMAC", imported, encoder.encode(value));
}

// The caller constructs the URL and allowed headers from an authorized typed
// operation. This function is internal to the trusted parent Worker.
export async function signRequest(grant, method, key, query = {}, headers = {}, now = new Date()) {
  const base = new URL(grant.endpoint);
  if (
    !["https:", "http:"].includes(base.protocol) ||
    base.username ||
    base.password ||
    base.search ||
    base.hash ||
    base.pathname !== "/"
  ) {
    throw new Error("Invalid storage target");
  }
  if (grant.addressing_style === "virtual") {
    base.hostname = grant.bucket + "." + base.hostname;
  }
  const path =
    (grant.addressing_style === "path" ? "/" + encode(grant.bucket) : "") +
    "/" +
    key.split("/").map(encode).join("/");
  base.pathname = path;
  const stamp = now.toISOString().replace(/[:-]|\.\d{3}/g, "");
  const day = stamp.slice(0, 8);
  const scope = day + "/" + grant.region + "/s3/aws4_request";
  const parameters = {
    ...query,
    "X-Amz-Algorithm": "AWS4-HMAC-SHA256",
    "X-Amz-Credential": grant.access_key_id + "/" + scope,
    "X-Amz-Date": stamp,
    "X-Amz-Expires": "60",
  };
  if (grant.session_token) {
    parameters["X-Amz-Security-Token"] = grant.session_token;
  }
  const signed = new Headers(headers);
  signed.set("host", base.host);
  const entries = [...signed].sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0));
  const names = entries.map(([name]) => name).join(";");
  parameters["X-Amz-SignedHeaders"] = names;
  const canonicalQuery = Object.entries(parameters)
    .map(([name, value]) => [encode(name), encode(String(value))])
    .sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0))
    .map(([name, value]) => name + "=" + value)
    .join("&");
  const canonical = [
    method,
    path,
    canonicalQuery,
    entries.map(([name, value]) => name + ":" + value.trim().replace(/\s+/g, " ") + "\n").join(""),
    names,
    "UNSIGNED-PAYLOAD",
  ].join("\n");
  let signingKey = "AWS4" + grant.secret_access_key;
  for (const part of [day, grant.region, "s3", "aws4_request"]) {
    signingKey = await hmac(signingKey, part);
  }
  const signature = hex(
    await hmac(
      signingKey,
      "AWS4-HMAC-SHA256\n" + stamp + "\n" + scope + "\n" + (await digest(canonical)),
    ),
  );
  base.search = canonicalQuery + "&X-Amz-Signature=" + signature;
  signed.delete("host");
  return { url: base.href, method, headers: Object.fromEntries(signed) };
}
