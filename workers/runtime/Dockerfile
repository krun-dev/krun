FROM node:24-bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY package.json package-lock.json ./
ARG KRUN_WORKERD_URL=https://github.com/krun-dev/workerd/releases/download/v1.20261116.1-krun.2/workerd-linux-64.gz
RUN npm ci --ignore-scripts --omit=dev \
    && case "$(node -p 'process.arch')" in \
         x64) curl -fsSL --retry 3 "$KRUN_WORKERD_URL" -o /tmp/krun-workerd.gz \
              && gzip -dc /tmp/krun-workerd.gz > node_modules/workerd/bin/workerd \
              && echo '8029143fd3ebc7410b893b148d04af0bb95730486c0ca0b706b4ec9177eff4d6  node_modules/workerd/bin/workerd' | sha256sum -c - \
              && chmod 755 node_modules/workerd/bin/workerd \
              && rm /tmp/krun-workerd.gz ;; \
         arm64) cp node_modules/@cloudflare/workerd-linux-arm64/bin/workerd node_modules/workerd/bin/workerd ;; \
         *) echo "Unsupported workerd architecture" >&2; exit 1 ;; \
       esac
COPY entry.js assets.js storage.js cache.js service.js ./
COPY storage ./storage
ENV WORKERD_EXPERIMENTAL_CPU_METRICS=1
ENV WORKERD_EXPERIMENTAL_MEMORY_METRICS=1
USER 1000:1000
# Run the native binary directly so it receives container shutdown signals.
CMD ["/app/node_modules/.bin/workerd","serve","/config/runtime.capnp","-I","/","--experimental"]
