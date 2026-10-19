FROM node:24-bookworm-slim
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --ignore-scripts --omit=dev
COPY entry.js ./
USER 1000:1000
CMD ["/app/node_modules/@cloudflare/workerd-linux-64/bin/workerd","serve","/config/runtime.capnp","-I","/","--experimental"]
