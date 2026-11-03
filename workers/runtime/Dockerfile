FROM node:24-bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --ignore-scripts --omit=dev
COPY entry.js assets.js storage.js ./
USER 1000:1000
# The workerd launcher selects the optional native package for the image
# architecture. Do not hard-code workerd-linux-64: kind and production may run
# arm64 nodes.
CMD ["/app/node_modules/.bin/workerd","serve","/config/runtime.capnp","-I","/","--experimental"]
