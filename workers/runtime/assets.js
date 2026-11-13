// Static asset policy is shared by the public router and env.ASSETS.fetch().
// This module only decides paths and response headers; entry.js owns I/O/caches.
export function normalizePath(input) {
  try {
    input = decodeURIComponent(input);
  } catch {
    return null;
  }

  if (/[\\\x00-\x1f\x7f]/.test(input)) {
    return null;
  }

  const parts = [];

  for (const part of input.split("/")) {
    if (part === "" || part === ".") {
      continue;
    }

    if (part === "..") {
      parts.pop();
    } else {
      parts.push(part);
    }
  }

  const result = "/" + parts.join("/");

  return result !== "/" && input.endsWith("/") ? result + "/" : result;
}

export function workerFirst(path, patterns = []) {
  const matches = (pattern) =>
    pattern.endsWith("*") ? path.startsWith(pattern.slice(0, -1)) : path === pattern;

  if (patterns.some((p) => p.startsWith("!") && matches(p.slice(1)))) {
    return false;
  }

  return patterns.some((p) => !p.startsWith("!") && matches(p));
}

function pattern(source, absolute = false) {
  if (
    (!source.startsWith("/") || source.startsWith("//")) &&
    !(absolute && source.startsWith("https://"))
  ) {
    return null;
  }

  if (/[?#]/.test(source) || (source.match(/\*/g) || []).length > 1) {
    return null;
  }

  const names = [];
  let expression = "";
  let offset = 0;
  const hostnameEnd = source.startsWith("https://") ? source.indexOf("/", 8) : -1;

  for (const match of source.matchAll(/\*|:([A-Za-z]\w*)/g)) {
    const name = match[1] || "splat";

    if (names.includes(name)) {
      return null;
    }

    expression += source.slice(offset, match.index).replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
    expression +=
      match[0] === "*"
        ? "(.*)"
        : hostnameEnd !== -1 && match.index < hostnameEnd
          ? "([^./]+)"
          : "([^/]+)";
    names.push(name);
    offset = match.index + match[0].length;
  }

  expression += source.slice(offset).replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

  return {
    regex: new RegExp("^" + expression + "$"),
    names,
    absolute: source.startsWith("https://"),
  };
}

function captures(rule, url, path) {
  const matched = rule.regex.exec(rule.absolute ? "https://" + url.hostname + path : path);

  return (
    matched &&
    Object.fromEntries(
      rule.names.map((name, i) => [
        name,
        matched[i + 1].split("/").map(encodeURIComponent).join("/"),
      ]),
    )
  );
}

function substitute(value, params) {
  return value.replace(/:([A-Za-z]\w*)/g, (all, name) =>
    Object.hasOwn(params, name) ? params[name] : all,
  );
}

export function compileRules(config) {
  const redirects = [];
  const headers = [];
  let block;

  for (const raw of (config._headers || "").split(/\r?\n/)) {
    const line = raw.trim();

    if (!line || line.startsWith("#")) {
      continue;
    }

    if (!/^\s/.test(raw)) {
      const match = pattern(line, true);
      block = match ? { ...match, set: [], unset: [] } : null;

      if (block) {
        headers.push(block);
      }

      continue;
    }

    if (!block) {
      continue;
    }

    const remove = /^!\s+([!#$%&'*+.^_`|~\w-]+)$/.exec(line);

    if (remove) {
      block.unset.push(remove[1]);
      continue;
    }

    const set = /^([!#$%&'*+.^_`|~\w-]+):\s*(.*)$/.exec(line);

    if (set && !/[\x00-\x08\x0a-\x1f\x7f]/.test(set[2])) {
      try {
        new Headers([[set[1], set[2]]]);
        block.set.push([set[1], set[2]]);
      } catch {}
    }
  }

  for (const raw of (config._redirects || "").split(/\r?\n/)) {
    const line = raw.trim();

    if (!line || line.startsWith("#")) {
      continue;
    }

    const fields = line.split(/\s+/);

    if (fields.length < 2 || fields.length > 3) {
      continue;
    }

    const [source, destination] = fields;
    const status = fields[2] === undefined ? 302 : Number(fields[2]);
    const match = pattern(source);

    if (!match || ![200, 301, 302, 303, 307, 308].includes(status)) {
      continue;
    }

    if (
      !(destination.startsWith("/") && !destination.startsWith("//")) &&
      !(status !== 200 && /^https?:\/\//.test(destination))
    ) {
      continue;
    }

    redirects.push({ ...match, destination, status });
  }

  redirects.sort((a, b) => Number(a.names.length > 0) - Number(b.names.length > 0));

  return { redirects, headers };
}

export function applyHeaders(response, request, rules) {
  const url = new URL(request.url);
  const path = normalizePath(url.pathname);
  const values = new Headers();
  const unset = new Set();

  for (const rule of rules.headers) {
    const params = captures(rule, url, path);

    if (!params) {
      continue;
    }

    for (const [name, value] of rule.set) {
      values.append(name, substitute(value, params));
    }

    for (const name of rule.unset) {
      unset.add(name.toLowerCase());
    }
  }

  for (const [name, value] of values) {
    response.headers.set(name, value);
  }

  for (const name of unset) {
    response.headers.delete(name);
  }

  return response;
}

export function lookupAsset(files, path, handling, skipRedirects = false) {
  if (path === "/_headers" || path === "/_redirects") {
    return {};
  }

  if (handling === "none") {
    return files.has(path) ? { file: path } : {};
  }

  const force = handling === "force-trailing-slash";
  const drop = handling === "drop-trailing-slash";
  const actions = [];
  const serve = (file) => actions.push({ file });
  const redirect = (file, destination) => actions.push({ file, redirect: destination || "/" });
  const canonical = (stem) => (stem && !drop ? stem + "/" : stem || "/");

  if (path.endsWith("/index")) {
    serve(path);
    const parent = path.slice(0, -6);
    redirect(path + ".html", canonical(parent));
    redirect(parent + ".html", force ? canonical(parent) : parent);
  } else if (path.endsWith("/index.html")) {
    const parent = path.slice(0, -11);
    redirect(path, canonical(parent));

    if (force || drop) {
      serve(path);
    }

    redirect(parent + ".html", force ? canonical(parent) : parent);
  } else if (path.endsWith("/")) {
    const parent = path.slice(0, -1);

    if (!drop || path === "/") {
      serve(path + "index.html");
    }

    if (force) {
      serve(parent + ".html");
    } else {
      redirect(parent + ".html", parent);

      if (drop) {
        redirect(path + "index.html", parent);
      }
    }
  } else if (path.endsWith(".html")) {
    const stem = path.slice(0, -5);
    redirect(path, force ? canonical(stem) : stem);

    if (force) {
      serve(path);
    }

    redirect(stem + "/index.html", canonical(stem));
  }

  serve(path);

  if (force) {
    redirect(path + ".html", path + "/");
  } else {
    serve(path + ".html");
  }

  if (drop) {
    serve(path + "/index.html");
  } else {
    redirect(path + "/index.html", path + "/");
  }

  for (const action of actions) {
    if (!files.has(action.file)) {
      continue;
    }

    if (!action.redirect) {
      return { file: action.file };
    }

    if (skipRedirects || files.has(action.redirect)) {
      continue;
    }

    // Canonicalization must not hide another existing file or form a loop.
    const target = lookupAsset(files, action.redirect, handling, true);

    if (target.file === action.file) {
      return { redirect: action.redirect };
    }
  }

  return {};
}

export function assetDecision(request, config, files, rules, binding = false) {
  const url = new URL(request.url);
  const path = normalizePath(url.pathname);

  if (path === null) {
    return { status: 400 };
  }

  if (!binding && workerFirst(path, config.run_worker_first)) {
    return {};
  }

  if (request.method !== "GET" && request.method !== "HEAD") {
    return binding ? { status: 405 } : {};
  }

  let target = path;
  let rewrite = false;

  for (const rule of rules.redirects) {
    const params = captures(rule, url, path);

    if (!params) {
      continue;
    }

    let destination = substitute(rule.destination, params);

    // A capture must never turn a relative destination into a network-path URL.
    if (destination.startsWith("//") || /[\\\r\n]/.test(destination)) {
      continue;
    }

    if (rule.status !== 200) {
      if (!destination.includes("?")) {
        const hash = destination.indexOf("#");
        destination =
          hash < 0
            ? destination + url.search
            : destination.slice(0, hash) + url.search + destination.slice(hash);
      }

      try {
        const targetURL = new URL(destination, url);

        if (!["http:", "https:"].includes(targetURL.protocol)) {
          continue;
        }

        destination = destination.startsWith("/")
          ? targetURL.pathname + targetURL.search + targetURL.hash
          : targetURL.href;
      } catch {
        continue;
      }

      return { redirect: destination, status: rule.status };
    }

    target = normalizePath(new URL(destination, url).pathname);
    rewrite = true;
    break;
  }

  const base =
    config.base_path && config.base_path !== "/" ? config.base_path.replace(/\/$/, "") : "";

  if (base && target !== base && !target.startsWith(base + "/")) {
    return {};
  }

  const relative = base ? target.slice(base.length) || "/" : target;
  let hit = lookupAsset(files, relative, config.html_handling || "auto-trailing-slash");

  if (rewrite && hit.redirect) {
    // Internal rewrites resolve the canonical target without redirecting the browser.
    hit = lookupAsset(files, hit.redirect, config.html_handling || "auto-trailing-slash");
  }

  if (hit.redirect) {
    return {
      redirect: (base + hit.redirect).split("/").map(encodeURIComponent).join("/") + url.search,
      status: 307,
    };
  }

  if (hit.file) {
    if (base && relative === "/" && hit.file === "/index.html" && !rewrite) {
      if (config.html_handling === "drop-trailing-slash" && target === base + "/") {
        return {
          redirect: base.split("/").map(encodeURIComponent).join("/") + url.search,
          status: 307,
        };
      }

      if (
        config.html_handling !== "none" &&
        config.html_handling !== "drop-trailing-slash" &&
        target === base
      ) {
        return {
          redirect: (base + "/").split("/").map(encodeURIComponent).join("/") + url.search,
          status: 307,
        };
      }
    }

    return hit;
  }

  if (config.not_found_handling === "404-page") {
    let directory = relative.slice(0, relative.lastIndexOf("/") + 1);

    for (;;) {
      if (files.has(directory + "404.html")) {
        return { file: directory + "404.html", status: 404, fallback: true };
      }

      if (directory === "/") {
        break;
      }

      directory = directory.slice(0, -1);
      directory = directory.slice(0, directory.lastIndexOf("/") + 1);
    }
  }

  if (
    config.not_found_handling === "single-page-application" &&
    (binding || request.headers.get("sec-fetch-mode") === "navigate") &&
    files.has("/index.html")
  ) {
    return { file: "/index.html", fallback: true };
  }

  return {};
}

export function immutableAssetName(path) {
  return /[-.][0-9a-f]{8,}\.[a-z0-9]+$/i.test(path);
}

export function assetResponse(request, path, value, status = 200) {
  const headers = new Headers({
    etag: '"' + value.digest + '"',
    "x-content-type-options": "nosniff",
    "content-type": value.content_type,
    "cache-control": immutableAssetName(path)
      ? "public, max-age=31536000, immutable"
      : "public, max-age=300",
  });

  if (status === 200 && request.headers.get("if-none-match") === headers.get("etag")) {
    return new Response(null, { status: 304, headers });
  }

  return new Response(request.method === "HEAD" ? null : value.bytes, { status, headers });
}
