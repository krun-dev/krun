FROM node:24-bookworm-slim
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --ignore-scripts --omit=dev
COPY build-worker.mjs proxy.mjs ./
USER 1000:1000
CMD ["node","/app/build-worker.mjs"]
