FROM rust:1.95-bookworm AS supervisor
RUN apt-get update && apt-get install -y --no-install-recommends cmake && rm -rf /var/lib/apt/lists/*
WORKDIR /build/supervisor
COPY supervisor/Cargo.toml supervisor/Cargo.lock ./
COPY supervisor/src ./src
RUN cargo build --release --locked

FROM node:24-bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --ignore-scripts --omit=dev \
    && case "$(node -p 'process.arch')" in \
         x64) workerd_arch=linux-64 ;; \
         arm64) workerd_arch=linux-arm64 ;; \
         *) echo "Unsupported workerd architecture" >&2; exit 1 ;; \
       esac \
    && cp "node_modules/@cloudflare/workerd-${workerd_arch}/bin/workerd" node_modules/workerd/bin/workerd
COPY entry.js assets.js storage.js cache.js service.js ./
COPY storage ./storage
COPY --from=supervisor /build/supervisor/target/release/krun-execution-pool-experiment /usr/local/bin/krun-runtime-supervisor
USER 1000:1000
# Run the native binary directly so it receives container shutdown signals.
CMD ["/app/node_modules/.bin/workerd","serve","/config/runtime.capnp","-I","/","--experimental"]
