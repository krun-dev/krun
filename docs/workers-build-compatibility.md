# Cloudflare Workers build compatibility

This document defines the build compatibility boundary for krun. The platform
accepts projects that are already configured for Cloudflare Workers, builds
them in an isolated job, and converts the result into krun's
`workerd-bundle-v2` artifact. Wrangler is an implementation detail of the
builder; it is not a public krun CLI or deployment API.

## Scope

The JavaScript build profile supports:

- module Workers written in JavaScript, JSX, TypeScript or TSX;
- projects with a checked-in npm, pnpm, Yarn or Bun lockfile;
- `wrangler.jsonc`, `wrangler.json` and `wrangler.toml`;
- Wrangler custom builds and projects already configured with the Cloudflare
  Vite plugin or a Cloudflare framework adapter;
- Workers static assets;
- Wrangler module `rules`, including `ESModule`, `CommonJS`, `Text`, `Data`
  and `CompiledWasm`;
- `find_additional_modules`, `base_dir`, `preserve_file_names` and
  `no_bundle`;
- compatibility dates, compatibility flags and Node.js compatibility handled
  by the pinned workerd runtime.

The first implementation deliberately does not support:

- Python Workers or `pywrangler`;
- Rust compilation and `worker-build`;
- Wrangler's automatic conversion of an ordinary framework repository into a
  Workers project;
- legacy Service Worker syntax;
- Cloudflare deployment, login, resource provisioning or resource-management
  commands.

A repository without a Worker entry point or a Worker configuration generated
by its checked-in build tooling fails with an explicit unsupported-project
error. The builder never installs an adapter into, or writes configuration
back to, the user's repository.

## Build flow

1. The control plane pins a repository commit and creates an immutable source
   archive containing the selected project directory. Archive extraction
   rejects links, special files, path traversal, duplicate paths and configured
   size limits.
2. A short-lived builder job downloads that archive. The job runs as a
   non-root user with a read-only root filesystem, no service-account token,
   bounded CPU, memory, temporary storage and execution time.
3. The builder selects exactly one package manager from the checked-in
   lockfile, performs a frozen install against the configured package registry,
   and records the package-manager and Wrangler versions in its log.
4. A pinned Wrangler version runs in dry-run mode. Wrangler may execute the
   project's declared custom build, but receives no Cloudflare credentials and
   cannot deploy anything.
5. A trusted packer reads the generated entry point, extra modules and assets,
   rejects links and unsafe or oversized output, hashes every file and emits a
   deterministic `workerd-bundle-v2` archive.
6. The control plane independently validates the manifest and archive before
   accepting the artifact. A real workerd smoke test covers module loading and
   request execution.

## Isolation boundary

Installing dependencies and running a project build executes repository code.
Production project builds therefore use the Kubernetes builder mode. The job
has no control-plane, database, object-store or Kubernetes credentials; it only
receives narrowly scoped source-download and artifact-upload URLs. Network
policy permits the configured package registry and the build transfer endpoint
and denies cluster services and arbitrary public destinations.

`builder_mode: local` is a trusted-development convenience. It must not run
custom builds from untrusted repositories. Deployments that offer builds to
untrusted tenants should set a sandboxed Kubernetes `runtimeClassName` backed
by gVisor, Kata Containers or an equivalent isolation layer.

## Versioning

Wrangler and package-manager versions are pinned in the builder image. They are
upgraded deliberately, not fetched from the project at runtime. Each upgrade
must pass fixtures for:

- a plain TypeScript Worker with npm dependencies;
- named exports such as a Durable Object class;
- Text, Data and Wasm imports;
- a variable dynamic import collected by `find_additional_modules`;
- `no_bundle` with a nested module tree;
- a Cloudflare Vite plugin build with generated Worker configuration and
  static assets;
- path traversal, link, collision, output-size and timeout rejection;
- loading the resulting artifact in the pinned workerd runtime.
