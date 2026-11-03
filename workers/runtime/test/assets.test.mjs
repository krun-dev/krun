import assert from "node:assert/strict";
import {
  assetDecision,
  compileRules,
  applyHeaders,
  assetResponse,
  normalizePath,
} from "../assets.js";
const files = new Map(
  [
    "index.html",
    "file.html",
    "folder/index.html",
    "404.html",
    "folder/404.html",
    "raw.txt",
    "_headers",
    "_redirects",
  ].map((p) => ["/" + p, p]),
);
const request = (path, init = {}) => new Request("https://site.test" + path, init);
function decide(path, config = {}, init = {}, binding = false) {
  return assetDecision(request(path, init), config, files, compileRules(config), binding);
}
for (const [mode, canonicalFile, canonicalFolder] of [
  ["auto-trailing-slash", "/file", "/folder/"],
  ["force-trailing-slash", "/file/", "/folder/"],
  ["drop-trailing-slash", "/file", "/folder"],
]) {
  for (const [stem, canonical, file] of [
    ["file", canonicalFile, "/file.html"],
    ["folder", canonicalFolder, "/folder/index.html"],
  ]) {
    for (const suffix of ["", ".html", "/", "/index", "/index.html"]) {
      const path = "/" + stem + suffix;
      const hit = decide(path + "?a=1", { html_handling: mode });
      if (path === canonical) {
        assert.equal(hit.file, file, mode + path);
      } else {
        assert.equal(hit.redirect, canonical + "?a=1", mode + path);
      }
    }
  }
}
assert.deepEqual(decide("/missing.html"), {}); // Don't redirect nonexistent pages.
assert.equal(decide("/file.html", { html_handling: "none" }).file, "/file.html");
assert.deepEqual(decide("/file", { html_handling: "none" }), {});
assert.equal(
  decide("/folder/deeper/missing", { not_found_handling: "404-page" }).file,
  "/folder/404.html",
);
assert.equal(decide("/elsewhere", { not_found_handling: "404-page" }).status, 404);
assert.equal(decide("/blog/file", { base_path: "/blog" }).file, "/file.html");
assert.deepEqual(decide("/file", { base_path: "/blog" }), {});
assert.deepEqual(decide("/blogger/file", { base_path: "/blog" }), {});
assert.equal(decide("/blog", { base_path: "/blog" }).redirect, "/blog/");
assert.equal(decide("/blog/file.html?q=1", { base_path: "/blog" }).redirect, "/blog/file?q=1");
assert.equal(decide("/blog/file", { base_path: "/blog" }, {}, true).file, "/file.html");
const config = {
  base_path: "/blog",
  _redirects:
    "/blog/old/* /blog/:splat 301\n/blog/:id/view /blog/file?from=:id 302\n/blog/rewrite /blog/file 200\n/blog/file /blog/raw.txt 200\n/blog/external https://other.test/no 200\n/blog/escape/* /:splat 302",
  _headers:
    "/*\n X-Shared: common\n Cache-Control: max-age=60\nhttps://:host.test/blog/:page\n X-Shared: :host-:page\n ! X-Content-Type-Options\n/blog/rewrite\n X-Rewrite: source",
};
assert.equal(decide("/blog/old/file?q=1", config).redirect, "/blog/file?q=1");
assert.equal(decide("/blog/id/view", config).redirect, "/blog/file?from=id");
assert.equal(decide("/blog/rewrite", config).file, "/file.html"); // No chained rewrite.
assert.deepEqual(decide("/blog/external", config), {});
assert(!decide("/blog/escape/%2Fevil.test", config).redirect?.startsWith("//"));
assert.deepEqual(decide("/blog/old/file", { ...config, run_worker_first: ["/blog/old/*"] }), {});
assert.equal(
  decide("/blog/old/file", { ...config, run_worker_first: ["/*"] }, {}, true).status,
  301,
);
for (const p of ["/_headers", "/_redirects"]) {
  assert.equal(decide(p).file, undefined);
}
const value = {
  digest: "abc",
  content_type: "text/plain",
  bytes: new TextEncoder().encode("hello"),
};
let response = applyHeaders(
  assetResponse(request("/blog/file"), "/file.html", value),
  request("/blog/file"),
  compileRules(config),
);
assert.equal(response.headers.get("x-shared"), "common, site-file");
assert.equal(response.headers.get("cache-control"), "max-age=60");
const unicodeHeaders = applyHeaders(
  new Response("ok"),
  request("/blog/%E6%B1%89"),
  compileRules({ _headers: "/blog/:name\n X-Name: :name" }),
);
assert.equal(unicodeHeaders.headers.get("x-name"), "%E6%B1%89");
assert.equal(response.headers.get("x-content-type-options"), null);
response = applyHeaders(
  assetResponse(
    request("/blog/file", { headers: { "if-none-match": '"abc"' } }),
    "/file.html",
    value,
  ),
  request("/blog/file"),
  compileRules(config),
);
assert.equal(response.status, 304);
assert.equal(response.headers.get("x-shared"), "common, site-file");
assert.equal(
  await assetResponse(request("/raw.txt", { method: "HEAD" }), "/raw.txt", value).text(),
  "",
);
assert.equal(decide("/file", {}, { method: "POST" }, true).status, 405);
assert.equal(normalizePath("/bad%ZZ"), null);
assert.equal(normalizePath("/foo/"), "/foo/");
const collisionFiles = new Map(["/same.html", "/same/index.html"].map((p) => [p, p]));
for (const [mode, path, file] of [
  ["auto-trailing-slash", "/same/index.html", "/same/index.html"],
  ["force-trailing-slash", "/same.html", "/same.html"],
  ["drop-trailing-slash", "/same/index.html", "/same/index.html"],
]) {
  const hit = assetDecision(
    request(path),
    { html_handling: mode },
    collisionFiles,
    compileRules({}),
  );
  if (mode === "auto-trailing-slash") {
    assert.equal(hit.redirect, "/same/");
  } else {
    assert.equal(hit.file, file);
  }
}
assert.equal(
  decide("/file", { _redirects: "/* /dynamic 302\n/file /static 301" }).redirect,
  "/static",
);
assert.equal(decide("/blog/file", { base_path: "/blog/" }).file, "/file.html");
assert.equal(
  decide("/blog", { base_path: "/blog", html_handling: "drop-trailing-slash" }).file,
  "/index.html",
);
assert.equal(
  decide("/blog/", { base_path: "/blog", html_handling: "drop-trailing-slash" }).redirect,
  "/blog",
);
console.log(
  "Static asset policies: HTML modes, mounts, headers, redirects, rewrites, fallbacks and bindings passed",
);
