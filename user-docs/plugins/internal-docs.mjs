// Mounts optional internal-only documentation into Starlight and adds it to the
// sidebar.
//
// Public builds set no source, so no internal page exists and the sidebar is
// unchanged. Internal builds point `KRUN_INTERNAL_DOCS_DIR` at a directory
// shaped like:
//
//   docs/         Markdown pages, published under /docs/internal/*
//   public/       Optional assets, served from /docs/internal-docs/*
//   sidebar.json  Optional sidebar items for this section
//
// The mounted targets live under paths ignored by Git, so internal content
// never enters the open-source repository. The copy runs before Astro reads the
// content collections, which means `npm run build` and `npm run docs:dev` both
// pick it up without an extra build step.

import {
  cpSync,
  existsSync,
  mkdirSync,
  readFileSync,
  readdirSync,
  rmSync,
  statSync,
  writeFileSync,
} from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const docsRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const repositoryRoot = path.resolve(docsRoot, "..");
const markdownFile = /\.mdx?$/;

const mountPoints = [
  { source: "docs", target: "src/content/docs/internal" },
  { source: "public", target: "public/internal-docs" },
];

// Marks a mount produced by this plugin. Only directories carrying the marker
// are removed, so a hand-written `internal/` page never gets deleted.
const markerFile = ".krun-internal-mount";

const docsMountPoint = path.join(docsRoot, mountPoints[0].target);
const markerPath = path.join(docsMountPoint, markerFile);

function countPages(directory) {
  let total = 0;

  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    const child = path.join(directory, entry.name);

    if (entry.isDirectory()) {
      total += countPages(child);
    } else if (entry.isFile() && markdownFile.test(entry.name)) {
      total += 1;
    }
  }

  return total;
}

function isDirectory(target) {
  try {
    return statSync(target).isDirectory();
  } catch {
    return false;
  }
}

/** Removes only mounts this plugin created, leaving hand-written pages alone. */
function clearMountPoints() {
  if (existsSync(markerPath)) {
    rmSync(docsMountPoint, { recursive: true, force: true });
  }

  rmSync(path.join(docsRoot, mountPoints[1].target), { recursive: true, force: true });
}

/**
 * Reads an optional `sidebar.json` from the internal source to let the internal
 * repository name its own groups. Without it, Starlight autogenerates the tree
 * and directory names appear verbatim in the sidebar.
 */
function readSidebar(sourceRoot) {
  const sidebarPath = path.join(sourceRoot, "sidebar.json");

  if (!existsSync(sidebarPath)) {
    return [{ autogenerate: { directory: "internal" } }];
  }

  const items = JSON.parse(readFileSync(sidebarPath, "utf8"));

  if (!Array.isArray(items) || items.length === 0) {
    throw new Error(`${sidebarPath} must contain a non-empty array of sidebar items`);
  }

  return items;
}

/** Copies the internal tree into the Starlight sources, replacing earlier mounts. */
function mountInternalDocs(source) {
  // Relative paths are resolved from the repository root, which is where
  // `npm run build` and `npm run docs:dev` are invoked.
  const sourceRoot = path.resolve(repositoryRoot, source);
  const docsSource = path.join(sourceRoot, "docs");

  if (!isDirectory(docsSource)) {
    throw new Error(`${sourceRoot} has no docs/ directory to mount`);
  }

  const pages = countPages(docsSource);

  if (pages === 0) {
    throw new Error(`${docsSource} contains no .md or .mdx pages`);
  }

  clearMountPoints();

  for (const mountPoint of mountPoints) {
    const from = path.join(sourceRoot, mountPoint.source);

    if (!isDirectory(from)) {
      continue;
    }

    const to = path.join(docsRoot, mountPoint.target);

    mkdirSync(path.dirname(to), { recursive: true });
    cpSync(from, to, { recursive: true });
  }

  writeFileSync(markerPath, `${path.basename(sourceRoot)}\n`);

  return { pages, source: sourceRoot, items: readSidebar(sourceRoot) };
}

/** True when a previous mount left pages behind and no source is configured now. */
function isStaleMount() {
  return existsSync(markerPath) && isDirectory(docsMountPoint);
}

export default function internalDocs() {
  return {
    name: "krun-internal-docs",
    hooks: {
      "config:setup"({ config, updateConfig, logger }) {
        const source = process.env.KRUN_INTERNAL_DOCS_DIR ?? "";

        if (!source.trim()) {
          if (isStaleMount()) {
            logger.warn(
              "internal docs: mounted pages found without KRUN_INTERNAL_DOCS_DIR, removing them",
            );
            clearMountPoints();
          }

          return;
        }

        const mounted = mountInternalDocs(source);

        updateConfig({
          sidebar: [
            ...(config.sidebar ?? []),
            {
              label: "内部文档",
              collapsed: false,
              badge: { text: "内部", variant: "danger" },
              items: mounted.items,
            },
          ],
        });

        logger.info(`internal docs: ${mounted.pages} page(s) mounted from ${mounted.source}`);
      },
    },
  };
}
