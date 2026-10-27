#!/usr/bin/env node

import path from "node:path";
import { resolveConfig } from "vite";

const projectRoot = path.resolve(import.meta.dirname, "..");
const config = await resolveConfig(
  {
    configFile: path.join(projectRoot, "vite.config.ts"),
    logLevel: "silent",
    mode: "production",
  },
  "build",
  "production",
);

process.stdout.write(
  JSON.stringify({
    root: config.root,
    base: config.base,
    outDir: config.build.outDir,
    proxyKeys: Object.keys(config.server.proxy ?? {}).sort(),
  }),
);
