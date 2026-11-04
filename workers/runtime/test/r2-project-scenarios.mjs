import assert from "node:assert/strict";
import { createHmac } from "node:crypto";
import { appendFileSync } from "node:fs";

const b64 = (value) => Buffer.from(value).toString("base64");
const fixture = "upstream R2 compatibility: 中文\n";

async function status(response, expected) {
  assert.equal(response.status, expected, await response.clone().text());
  return response;
}

export async function runProject(project, request) {
  const failures = [];
  let passed = 0;
  async function check(name, run) {
    try {
      await run();
      passed++;
      console.log("PASS " + name);
    } catch (error) {
      failures.push(name);
      console.log("FAIL " + name + ": " + error.message);
    }
  }

  if (project.name === "r2-image-worker") {
    let key;
    await check("Basic Auth rejects anonymous upload", async () => {
      await status(await request("/upload", { method: "PUT", body: "no auth" }), 401);
    });
    await check("FormData File upload", async () => {
      const form = new FormData();
      form.set("image", new Blob([fixture], { type: "image/png" }), "fixture.png");
      const response = await status(
        await request("/upload", {
          method: "PUT",
          body: form,
          headers: { authorization: "Basic " + b64("fixture-user:fixture-password") },
        }),
        200,
      );
      key = await response.text();
      assert.match(key, /^[a-f0-9]{64}\.png$/);
    });
    await check("Image download and cached second read", async () => {
      assert.ok(key, "upload did not produce a key");
      for (let attempt = 0; attempt < 2; attempt++) {
        const response = await status(await request("/" + key), 200);
        assert.equal(response.headers.get("content-type"), "image/png");
        assert.equal(await response.text(), fixture);
        if (attempt === 1) {
          assert.match(
            response.headers.get("age"),
            /^\d+$/,
            "second response did not come from Cache API",
          );
        }
      }
    });
  }

  if (project.name === "r2-file-manager") {
    await check("PUT request stream", async () => {
      await status(await request("/direct.txt", { method: "PUT", body: fixture }), 200);
    });
    await check("GET stream and httpEtag", async () => {
      const response = await status(await request("/direct.txt"), 200);
      assert.match(response.headers.get("etag"), /^".+"$/);
      assert.equal(await response.text(), fixture);
    });
    await check("FormData File.stream upload and index", async () => {
      const form = new FormData();
      form.set("file", new Blob([fixture]), "form.txt");
      await status(await request("/", { method: "POST", body: form }), 200);
      const response = await status(await request("/"), 200);
      assert.match(await response.text(), /form\.txt/);
      assert.equal(await (await status(await request("/form.txt"), 200)).text(), fixture);
    });
    await check("Delete and missing object", async () => {
      await status(await request("/delete/direct.txt"), 200);
      await status(await request("/direct.txt"), 404);
    });
  }

  if (project.name === "bytego") {
    let key;
    await check("HTML home", async () => {
      assert.match(await (await status(await request("/"), 200)).text(), /ByteGo/);
    });
    await check("Authenticated File.stream upload and Unicode metadata", async () => {
      const form = new FormData();
      form.set("file", new Blob([fixture], { type: "text/plain" }), "中文.txt");
      form.set("customPath", "projects/bytego.txt");
      const response = await status(
        await request("/upload", {
          method: "POST",
          body: form,
          headers: { authorization: "Bearer fixture-auth" },
        }),
        200,
      );
      const body = await response.json();
      key = body.key;
      assert.equal(key, "projects/bytego.txt");
      assert.equal(body.filename, "中文.txt");
      assert.equal(body.size, Buffer.byteLength(fixture));
      assert.equal(body.url, "https://cdn.fixture.test/projects/bytego.txt");
    });
    await check("Authenticated delete", async () => {
      assert.ok(key, "upload did not produce a key");
      await status(
        await request("/object", {
          method: "DELETE",
          body: JSON.stringify({ key }),
          headers: { authorization: "Bearer fixture-auth", "content-type": "application/json" },
        }),
        200,
      );
    });
  }

  if (project.name === "r2-share-worker") {
    const expiry = String(Date.now() + 60000);
    const token = createHmac("sha256", project.variables.SECRET_KEY)
      .update("share:" + expiry)
      .digest("hex");
    const query = "?token=" + token + "&exp=" + expiry;
    await check("Invalid sharing token", async () => {
      await status(await request("/?token=bad&exp=" + expiry), 403);
    });
    await check("Directory listing follows multiple pages", async () => {
      const response = await status(await request("/" + query), 200);
      const html = await response.text();
      assert.match(html, /page-000\.txt/);
      assert.match(html, /page-064\.txt/);
    });
    await check("Attachment download", async () => {
      const response = await status(await request("/share.txt" + query), 200);
      assert.equal(await response.text(), fixture);
      assert.match(response.headers.get("content-disposition"), /attachment/);
    });
    await check("Media HEAD and Range download", async () => {
      const response = await status(
        await request("/media.mp4" + query, {
          headers: { range: "bytes=2-7" },
        }),
        206,
      );
      assert.equal(response.headers.get("content-range"), "bytes 2-7/16");
      assert.equal(await response.text(), "234567");
    });
  }

  if (project.name === "r2-explorer") {
    const base = "/api/buckets/BUCKET";
    const original = "explorer/中文.txt";
    const renamed = "explorer/renamed.txt";
    const encoded = (key) => encodeURIComponent(b64(key));
    await check("Raw stream upload with HTTP/custom metadata", async () => {
      const query = new URLSearchParams({
        key: b64(original),
        httpMetadata: b64(JSON.stringify({ contentType: "text/plain" })),
        customMetadata: b64(JSON.stringify({ originalName: "中文.txt" })),
      });
      await status(
        await request(base + "/upload?" + query, {
          method: "POST",
          body: fixture,
          headers: { "content-type": "application/octet-stream" },
        }),
        200,
      );
    });
    await check("Flat list uses empty delimiter", async () => {
      const response = await status(await request(base), 200);
      const body = await response.json();
      assert.ok(body.objects.some((object) => object.key === original));
    });
    await check("Directory list and delimiter", async () => {
      const response = await status(await request(base + "?delimiter=/"), 200);
      assert.ok((await response.json()).delimitedPrefixes.includes("explorer/"));
    });
    await check("Download bytes and HTTP metadata", async () => {
      const response = await status(await request(base + "/" + encoded(original)), 200);
      assert.equal(response.headers.get("content-type"), "text/plain");
      assert.equal(await response.text(), fixture);
    });
    await check("Rename via get body -> put -> delete", async () => {
      await status(
        await request(base + "/move", {
          method: "POST",
          headers: { "content-type": "application/json" },
          body: JSON.stringify({ oldKey: b64(original), newKey: b64(renamed) }),
        }),
        200,
      );
      await status(await request(base + "/" + encoded(original)), 404);
      assert.equal(
        await (await status(await request(base + "/" + encoded(renamed)), 200)).text(),
        fixture,
      );
    });
    await check("Delete renamed object", async () => {
      await status(
        await request(base + "/delete", {
          method: "POST",
          headers: { "content-type": "application/json" },
          body: JSON.stringify({ key: b64(renamed) }),
        }),
        200,
      );
    });
    await check("Multipart create, cross-request resume, parts and completion", async () => {
      const key = "explorer/multipart.bin";
      const created = await (
        await status(
          await request(base + "/multipart/create?key=" + encoded(key), {
            method: "POST",
          }),
          200,
        )
      ).json();
      assert.equal(created.key, key);
      assert.ok(created.uploadId);
      appendFileSync(
        project.multipart_cleanup_file,
        JSON.stringify({ key, token: created.uploadId }) + "\n",
        { mode: 0o600 },
      );
      const parts = [];
      for (const [partNumber, bytes] of [
        [1, Buffer.alloc(5 * 1024 * 1024, 65)],
        [2, Buffer.from("tail")],
      ]) {
        const query = new URLSearchParams({
          key: b64(key),
          uploadId: created.uploadId,
          partNumber: String(partNumber),
        });
        parts.push(
          await (
            await status(
              await request(base + "/multipart/upload?" + query, {
                method: "POST",
                body: bytes,
                headers: { "content-type": "application/octet-stream" },
              }),
              200,
            )
          ).json(),
        );
      }
      const completed = await (
        await status(
          await request(base + "/multipart/complete", {
            method: "POST",
            headers: { "content-type": "application/json" },
            body: JSON.stringify({ key: b64(key), uploadId: created.uploadId, parts }),
          }),
          200,
        )
      ).json();
      assert.equal(completed.success, true);
      assert.equal(completed.str.size, 5 * 1024 * 1024 + 4);
      const bytes = Buffer.from(
        await (await status(await request(base + "/" + encoded(key)), 200)).arrayBuffer(),
      );
      assert.deepEqual(bytes.subarray(0, -4), Buffer.alloc(5 * 1024 * 1024, 65));
      assert.equal(bytes.subarray(-4).toString(), "tail");
      await status(
        await request(base + "/delete", {
          method: "POST",
          headers: { "content-type": "application/json" },
          body: JSON.stringify({ key: b64(key) }),
        }),
        200,
      );
    });
  }
  assert.equal(failures.length, 0, "Failed upstream scenarios: " + failures.join(", "));
  console.log("RESULT " + project.name + ": " + passed + " selected checks passed");
}
