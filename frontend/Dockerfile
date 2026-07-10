# ─── Stage 1: build ──────────────────────────────────────────────────────────
FROM node:22-alpine AS builder

WORKDIR /app

# Install dependencies first to leverage layer caching.
COPY package*.json ./
RUN npm ci

# Copy the rest of the frontend source and build. Vite outputs to /app/dist,
# including the placeholder public/config.json (overridden at deploy time by the
# Helm-rendered ConfigMap mount).
COPY ./ ./
RUN npm run build

# ─── Stage 2: serve ──────────────────────────────────────────────────────────
FROM nginx:alpine AS final

# Remove the default nginx welcome page and config.
RUN rm -rf /usr/share/nginx/html/* /etc/nginx/conf.d/default.conf

# Copy built assets.
COPY --from=builder /app/dist /usr/share/nginx/html

# Baked placeholder nginx config so the image runs standalone (no /api proxy —
# that is added by the Helm-rendered nginx.conf mounted over this in-cluster).
# Listens on 8080 with all temp paths under /tmp so the container can run
# non-root with a read-only root filesystem.
COPY nginx.default.conf /etc/nginx/nginx.conf

EXPOSE 8080

CMD ["nginx", "-g", "daemon off;"]
