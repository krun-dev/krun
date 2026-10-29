FROM node:24-bookworm-slim AS dependencies
WORKDIR /app
COPY package.json package-lock.json ./
# npm publishes its CLI with bundled transitive dependencies. The pinned CLI
# currently bundles older undici/ip-address builds even though their declared
# ranges accept the patched releases installed at the image root. Remove only
# those bundled copies so Node resolves the exact patched top-level versions.
RUN npm ci --ignore-scripts --omit=dev --audit=false \
 && rm -rf node_modules/npm/node_modules/undici node_modules/npm/node_modules/ip-address \
 && node -e "const{createRequire}=require('module');const npm=createRequire(require.resolve('npm/package.json'));if(npm('undici/package.json').version!=='6.29.1')process.exit(1);const socks=createRequire('/app/node_modules/npm/node_modules/socks/package.json');if(socks('ip-address/package.json').version!=='10.7.5')process.exit(1)"
RUN node node_modules/bun/install.js
RUN test "$(node_modules/.bin/npm --version)" = "11.21.1" \
 && test "$(node_modules/.bin/pnpm --version)" = "12.16.1" \
 && test "$(node node_modules/yarn/bin/yarn.js --version)" = "1.22.22" \
 && test "$(node node_modules/@yarnpkg/cli-dist/bin/yarn.js --version)" = "4.18.1" \
 && test "$(node_modules/.bin/bun --version)" = "1.4.4" \
 && test "$(HOME=/tmp WRANGLER_SEND_METRICS=false WRANGLER_LOG_PATH=/tmp/wrangler.log node_modules/.bin/wrangler --version | tail -n 1)" = "4.155.0"
COPY wrangler-project.mjs ./
COPY test/manager-smoke.mjs ./test/manager-smoke.mjs
RUN node test/manager-smoke.mjs && rm test/manager-smoke.mjs && rmdir test

FROM node:24-bookworm-slim AS runtime
WORKDIR /app
COPY --from=dependencies /app/node_modules ./node_modules
COPY build-worker.mjs proxy.mjs ./
COPY wrangler-project.mjs ./
# The base image ships another global npm CLI with its own bundled dependency
# tree. The builder always invokes the pinned /app CLI, so remove the duplicate
# instead of carrying an unpatched, unreachable copy in the final image/SBOM.
RUN rm -rf /usr/local/lib/node_modules/npm /usr/local/bin/npm /usr/local/bin/npx \
 && test ! -e /usr/local/lib/node_modules/npm \
 && test ! -e /usr/local/bin/npm \
 && test ! -e /usr/local/bin/npx \
 && test "$(node_modules/.bin/npm --version)" = "11.21.1" \
 && test "$(HOME=/tmp WRANGLER_SEND_METRICS=false WRANGLER_LOG_PATH=/tmp/wrangler.log node_modules/.bin/wrangler --version | tail -n 1)" = "4.155.0"
USER 1000:1000
CMD ["node","/app/build-worker.mjs"]
