import assert from "node:assert/strict";
import { build } from "esbuild";
import { execFileSync, spawnSync } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const argument = (name) =>
  process.argv.find((value) => value.startsWith(name + "="))?.slice(name.length + 1);
const repos = path.resolve(argument("--repos") ?? ".data/r2-project-compat/repos");
const grant = argument("--grant-file");
const projects = [
  {
    name: "r2-image-worker",
    commit: "80cc728a049af10bfd9347a11088ee1d6dc4bbbd",
    entry: "src/index.ts",
    binding: "BUCKET",
    compatibility_date: "2025-08-09",
    variables: { USER: "fixture-user", PASS: "fixture-password" },
  },
  {
    name: "r2-file-manager",
    commit: "282ad563ccc48ae4297fa3506ceae77bfa329a81",
    entry: "worker.ts",
    binding: "MY_BUCKET",
    compatibility_date: "2026-09-28",
    variables: {},
  },
  {
    name: "bytego",
    commit: "ff21c5b6de9a2b3e5f648139753a73d08ddf6f2a",
    entry: "src/index.ts",
    binding: "BUCKET",
    compatibility_date: "2023-12-01",
    variables: {
      AUTH_KEY: "fixture-auth",
      PUBLIC_DOMAIN: "https://cdn.fixture.test",
      DELETE_MODE: "hard",
    },
  },
  {
    name: "r2-share-worker",
    commit: "70b2b79be23a18fe76964ff483a0cc5a6e6669be",
    entry: "src/worker.ts",
    binding: "MY_BUCKET",
    compatibility_date: "2026-03-30",
    variables: { SECRET_KEY: "fixture-share-secret" },
  },
  {
    name: "r2-explorer",
    commit: "2c09ecbea00be66992ddb8c63946aed1a9be59e6",
    entry: "packages/worker/src/index.ts",
    binding: "BUCKET",
    compatibility_date: "2026-09-28",
    variables: {},
  },
];
const upstreams = {
  "r2-image-worker": "yusukebe/r2-image-worker",
  "r2-file-manager": "msaifuddin/r2-file-manager",
  bytego: "zopiya/bytego",
  "r2-share-worker": "itoschum/r2-share-worker",
  "r2-explorer": "G4brym/R2-Explorer",
};
if (process.argv.includes("--prepare")) {
  mkdirSync(repos, { recursive: true });
  for (const project of projects) {
    const cwd = path.join(repos, project.name);
    if (!existsSync(cwd)) {
      mkdirSync(cwd);
      execFileSync("git", ["init", "--quiet"], { cwd });
      execFileSync(
        "git",
        ["remote", "add", "origin", "https://github.com/" + upstreams[project.name] + ".git"],
        { cwd },
      );
      execFileSync("git", ["fetch", "--depth", "1", "origin", project.commit], {
        cwd,
        stdio: "inherit",
      });
      execFileSync("git", ["checkout", "--detach", "FETCH_HEAD"], { cwd, stdio: "inherit" });
    }
    assert.equal(
      execFileSync("git", ["rev-parse", "HEAD"], { cwd, encoding: "utf8" }).trim(),
      project.commit,
    );
    if (["r2-image-worker", "bytego", "r2-explorer"].includes(project.name)) {
      const packageDirectory =
        project.name === "r2-explorer" ? path.join(cwd, "packages/worker") : cwd;
      execFileSync(
        "npm",
        [
          "install",
          "--ignore-scripts",
          "--omit=dev",
          "--workspaces=false",
          "--legacy-peer-deps",
          "--package-lock=false",
          "--no-audit",
          "--no-fund",
        ],
        { cwd: packageDirectory, stdio: "inherit", timeout: 120000 },
      );
    }
    console.log("Prepared " + project.name + " " + project.commit);
  }
  process.exit(0);
}
assert.ok(grant, "provide a private --grant-file issued by the configured Go integration test");
const directory = mkdtempSync(path.join(tmpdir(), "krun-r2-projects-"));
const failures = [];
try {
  for (const project of projects) {
    try {
      const cwd = path.join(repos, project.name);
      const commit = execFileSync("git", ["rev-parse", "HEAD"], { cwd, encoding: "utf8" }).trim();
      assert.equal(commit, project.commit, "upstream revision must match the audited fixture");
      execFileSync("git", ["diff", "--exit-code"], { cwd, stdio: "pipe" });
      const source = path.join(directory, project.name + ".js");
      const options = {
        bundle: true,
        format: "esm",
        platform: "neutral",
        target: "es2022",
        mainFields: ["module", "main"],
        conditions: ["workerd", "worker", "browser"],
        outfile: source,
        loader: { ".html": "text" },
        external: ["cloudflare:*"],
        nodePaths: [path.join(repos, "r2-image-worker/node_modules")],
      };
      if (project.name === "r2-explorer") {
        options.stdin = {
          contents:
            'import {R2Explorer} from "./packages/worker/src/index.ts"; export default R2Explorer({readonly:false});',
          resolveDir: cwd,
          sourcefile: "fixture-entry.ts",
          loader: "ts",
        };
      } else {
        options.entryPoints = [path.join(cwd, project.entry)];
      }
      await build(options);
      const config = path.join(directory, project.name + ".json");
      writeFileSync(
        config,
        JSON.stringify({
          ...project,
          source,
          multipart_cleanup_file: path.join(path.dirname(path.resolve(grant)), "multipart.jsonl"),
        }),
      );
      console.log("\nUPSTREAM " + project.name + " " + commit);
      const result = spawnSync(
        process.execPath,
        [
          path.join(here, "r2-workerd-smoke.mjs"),
          "--project-file=" + config,
          "--grant-file=" + path.resolve(grant),
        ],
        { encoding: "utf8", timeout: 90000 },
      );
      process.stdout.write(result.stdout ?? "");
      if (result.status !== 0) {
        process.stderr.write(result.stderr ?? "");
        throw new Error("upstream scenarios failed");
      }
    } catch (error) {
      failures.push(project.name);
      console.log("PROJECT FAILED " + project.name + ": " + error.message);
    }
  }
} finally {
  rmSync(directory, { recursive: true, force: true });
}
assert.equal(failures.length, 0, "Failed projects: " + failures.join(", "));
