import assert from "node:assert/strict";
import { gzipSync } from "node:zlib";
import { cacheRequest, createCacheStorage } from "../cache.js";

function storage(namespace) {
  return createCacheStorage({
    fetch(url, options) {
      return cacheRequest(namespace, new Request(url, { ...options, duplex: "half" }));
    },
  });
}
const first = storage("org-a/service/production");
const second = storage("org-b/service/production");
const url = "https://fixture.test/image?a=1";
const originalNow = Date.now;
let now = originalNow();
Date.now = () => now;
try {
  const response = new Response("0123456789", {
    headers: {
      "cache-control": "max-age=60",
      "content-length": "10",
      etag: '"v1"',
      "last-modified": "Wed, 04 Nov 2026 00:00:00 GMT",
      "cache-tag": "hidden",
    },
  });
  await first.default.put(url, response);
  assert.equal(response.bodyUsed, true);
  assert.equal(await (await first.default.match(url)).text(), "0123456789");
  assert.equal(await (await first.default.match(url)).text(), "0123456789");
  assert.equal((await first.default.match(url)).headers.has("cache-tag"), false);
  assert.equal(await second.default.match(url), undefined);
  assert.equal(await (await first.open("default")).match(url), undefined);
  assert.equal(await first.default.match(url + "&b=2"), undefined);
  assert.equal(await first.default.match(new Request(url, { method: "HEAD" })), undefined);
  assert.ok(
    await first.default.match(new Request(url, { method: "POST" }), { ignoreMethod: true }),
  );

  const range = await first.default.match(new Request(url, { headers: { range: "bytes=2-5" } }));
  assert.equal(range.status, 206);
  assert.equal(range.headers.get("content-range"), "bytes 2-5/10");
  assert.equal(await range.text(), "2345");
  const suffix = await first.default.match(new Request(url, { headers: { range: "bytes=-3" } }));
  assert.equal(await suffix.text(), "789");
  assert.equal(
    (await first.default.match(new Request(url, { headers: { range: "bytes=20-" } }))).status,
    416,
  );
  assert.equal(
    (await first.default.match(new Request(url, { headers: { "if-none-match": 'W/"v1"' } })))
      .status,
    304,
  );
  assert.equal(
    (
      await first.default.match(
        new Request(url, { headers: { "if-modified-since": "Thu, 05 Nov 2026 00:00:00 GMT" } }),
      )
    ).status,
    304,
  );
  now += 61000;
  assert.equal(await first.default.match(url), undefined);

  const named = await first.open("中文");
  await named.put(url, new Response("named"));
  assert.equal(await (await named.match(url)).text(), "named");
  assert.equal(await first.default.match(url), undefined);
  assert.equal(await named.delete(url), true);
  assert.equal(await named.delete(url), false);

  for (const [name, headers] of Object.entries({
    private: { "cache-control": "private, max-age=60" },
    noStore: { "cache-control": "no-store" },
    noCache: { "cache-control": "no-cache, max-age=60" },
    cookie: { "set-cookie": "secret=fixture" },
    aged: { "cache-control": "max-age=60", age: "61" },
    expired: { expires: new Date(now - 1000).toUTCString() },
  })) {
    await first.default.put(url + name, new Response("private", { headers }));
    assert.equal(await first.default.match(url + name), undefined, name);
  }
  await first.default.put(
    url,
    new Response("public", {
      headers: { "set-cookie": "remove-me", "cache-control": "private=Set-Cookie, max-age=60" },
    }),
  );
  assert.equal((await first.default.match(url)).headers.has("set-cookie"), false);

  const vary = url + "vary";
  for (const language of ["en", "zh"]) {
    await first.default.put(
      new Request(vary, { headers: { "accept-language": language } }),
      new Response(language, {
        headers: { vary: "Accept-Language", "cache-control": "max-age=60" },
      }),
    );
  }
  assert.equal(await first.default.match(vary), undefined);
  assert.equal(
    await (
      await first.default.match(new Request(vary, { headers: { "accept-language": "zh" } }))
    ).text(),
    "zh",
  );
  assert.equal(await first.default.delete(vary), true);
  assert.equal(
    await first.default.match(new Request(vary, { headers: { "accept-language": "zh" } })),
    undefined,
  );

  const compressed = gzipSync("gzip bytes must survive");
  await first.default.put(
    url + "gzip",
    new Response(compressed, { headers: { "content-encoding": "gzip" } }),
  );
  assert.deepEqual(
    Buffer.from(await (await first.default.match(url + "gzip")).arrayBuffer()),
    compressed,
  );
  for (const status of [204, 400, 504]) {
    await first.default.put(
      url + status,
      new Response(status === 204 ? null : "cached-error", {
        status,
        headers: { "cache-control": "max-age=60" },
      }),
    );
    assert.equal((await first.default.match(url + status)).status, status);
  }
  await assert.rejects(
    first.default.put(new Request(url, { method: "POST" }), new Response("x")),
    TypeError,
  );
  await assert.rejects(first.default.put(url, new Response("x", { status: 206 })), TypeError);
  await assert.rejects(
    first.default.put(url, new Response("x", { headers: { vary: "*" } })),
    TypeError,
  );
  await assert.rejects(first.default.match(url, { ignoreSearch: true }), TypeError);

  await first.default.put(url + "oversize", new Response(new Uint8Array(4 * 1024 * 1024 + 1)));
  assert.equal(await first.default.match(url + "oversize"), undefined);
  for (let number = 0; number < 5; number++) {
    await first.default.put(url + "lru" + number, new Response(new Uint8Array(4 * 1024 * 1024)));
  }
  assert.equal(await first.default.match(url + "lru0"), undefined);
  assert.equal((await first.default.match(url + "lru4")).status, 200);
  for (let number = 0; number < 5; number++) {
    await first.default.delete(url + "lru" + number);
  }
} finally {
  Date.now = originalNow;
}
console.log(
  "Cache isolation, TTL, Vary, conditional/Range reads, raw gzip, no-store and bounded eviction passed",
);
