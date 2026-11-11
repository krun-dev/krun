import { readFileSync } from "node:fs";

export const fixtureConfigEpoch = "a".repeat(32);

// Node tests execute the shipped runtime with a small WorkerEntrypoint stand-in.
// Match import syntax rather than depending on a particular formatter's spacing.
export function loadRuntimeSource() {
  const runtimeURL = new URL("../entry.js", import.meta.url);
  const source = readFileSync(runtimeURL, "utf8");
  const entrypointImport =
    /^import\s*{\s*WorkerEntrypoint\s*}\s*from\s*(["'])cloudflare:workers\1;?/m;

  if (!entrypointImport.test(source)) {
    throw new Error("Runtime WorkerEntrypoint import was not found");
  }

  const patched = source.replace(
    entrypointImport,
    `class WorkerEntrypoint {
      constructor(env, ctx) {
        this.env = env;
        this.ctx = ctx;
      }
    }`,
  );

  return patched.replace(/\bfrom\s+(["'])(\.\/[^"']+)\1/g, (_, quote, specifier) => {
    const moduleURL = new URL(specifier, runtimeURL);
    return "from " + JSON.stringify(moduleURL.href);
  });
}
