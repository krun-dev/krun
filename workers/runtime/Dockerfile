FROM node:24-bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY package.json package-lock.json ./
ARG KRUN_WORKERD_URL=https://github.com/krun-dev/workerd/releases/download/v1.20261116.1-krun.1/workerd-linux-64.gz
RUN if [ "$(node -p 'process.arch')" != x64 ]; then echo 'CPU metrics runtime requires Linux AMD64' >&2; exit 1; fi \
    && npm ci --ignore-scripts --omit=dev \
    && curl -fsSL --retry 3 "$KRUN_WORKERD_URL" -o /tmp/krun-workerd.gz \
    && gzip -dc /tmp/krun-workerd.gz > node_modules/workerd/bin/workerd \
    && echo '49125ba6eddbfc3ccbdd4a69f67c211bb257610bf0ec2a5cd554c8b65928e578  node_modules/workerd/bin/workerd' | sha256sum -c - \
    && chmod 755 node_modules/workerd/bin/workerd \
    && rm /tmp/krun-workerd.gz
COPY entry.js assets.js storage.js cache.js service.js ./
COPY storage ./storage
ENV WORKERD_EXPERIMENTAL_CPU_METRICS=1
USER 1000:1000
# Run the native binary directly so it receives container shutdown signals.
CMD ["/app/node_modules/.bin/workerd","serve","/config/runtime.capnp","-I","/","--experimental"]
