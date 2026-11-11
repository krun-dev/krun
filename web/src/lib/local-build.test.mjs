import assert from "node:assert/strict";
import test from "node:test";
import { localProjectFromFiles, localProjectFromDrop, submitLocalBuild } from "./local-build.ts";

function projectFile(relativePath) {
  const file = new File([relativePath], relativePath.split("/").at(-1));
  return Object.assign(file, { webkitRelativePath: relativePath });
}

test("folder picker keeps paths relative to the selected folder and skips generated directories", () => {
  const project = localProjectFromFiles([
    projectFile("site/index.html"),
    projectFile("site/assets/main.js"),
    projectFile("site/.git/config"),
    projectFile("site/node_modules/module/index.js"),
    projectFile("site/sub/.wrangler/state.json"),
  ]);

  assert.equal(project.name, "site");
  assert.deepEqual(
    Array.from(project.form, ([name, file]) => [name, file.name]),
    [
      ["file:index.html", "index.html"],
      ["file:assets/main.js", "main.js"],
    ],
  );
});

test("directory drop reads every batch and skips ignored subtrees before opening them", async () => {
  const entry = (path) => ({
    name: path.split("/").at(-1),
    isFile: true,
    file: (receive) => receive(projectFile(path)),
  });
  const batches = [
    [entry("site/index.html")],
    [
      {
        name: "assets",
        isDirectory: true,
        createReader: () => {
          const nested = [[entry("site/assets/main.js")], []];
          return { readEntries: (receive) => receive(nested.shift()) };
        },
      },
      {
        name: "node_modules",
        isDirectory: true,
        createReader: () => assert.fail("ignored directories must not be opened"),
      },
    ],
    [],
  ];
  const project = await localProjectFromDrop({
    items: [
      {
        kind: "file",
        webkitGetAsEntry: () => ({
          name: "site",
          isDirectory: true,
          createReader: () => ({ readEntries: (receive) => receive(batches.shift()) }),
        }),
      },
    ],
    files: [],
  });

  assert.equal(project.name, "site");
  assert.deepEqual(Array.from(project.form.keys()), ["file:index.html", "file:assets/main.js"]);
});

test("a mixed drop does not upload only the ZIP when the directory is missing from FileList", async () => {
  await assert.rejects(() =>
    localProjectFromDrop({
      items: [
        { kind: "file", webkitGetAsEntry: () => ({ isDirectory: true }) },
        { kind: "file", webkitGetAsEntry: () => ({ isFile: true }) },
      ],
      files: [new File(["zip contents"], "project.zip")],
    }),
  );
});

test("ZIP uploads use browser-generated multipart boundaries and same-origin CSRF", async (context) => {
  const project = localProjectFromFiles([new File(["zip contents"], "project.ZIP")]);
  const target = "/api/v1/organizations/org-a/workers/site/environments/preview/local-builds";
  context.mock.method(globalThis, "fetch", async (path, options) => {
    assert.equal(options.method, "POST");
    assert.equal(options.credentials, "same-origin");
    if (path === "/api/auth/csrf") {
      return Response.json({ csrf_token: "test-csrf" });
    }

    assert.equal(path, target + "?project_root=apps%2Ffront+end");
    const request = new Request("https://console.example.com" + path, options);
    assert.equal(request.headers.get("X-CSRF-Token"), "test-csrf");
    assert.equal(request.headers.get("Idempotency-Key"), "upload-attempt");
    assert.match(request.headers.get("Content-Type"), /^multipart\/form-data; boundary=/);
    const body = await request.formData();
    assert.deepEqual(Array.from(body.keys()), ["archive"]);
    assert.equal(body.get("archive").name, "project.ZIP");
    assert.equal(await body.get("archive").text(), "zip contents");
    return Response.json(
      { build: { id: "build-1", state: "queued" }, kind: "static" },
      { status: 202 },
    );
  });

  const result = await submitLocalBuild(target, project, "apps/front end", "upload-attempt");
  assert.deepEqual(result, { build: { id: "build-1", state: "queued" }, kind: "static" });
});
