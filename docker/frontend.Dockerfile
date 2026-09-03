# Production image for the Vue web UI: build with Node, serve static assets with nginx.
# (Dev uses `just web` / Vite on the host instead.)

FROM node:22-alpine AS builder
WORKDIR /app
COPY frontend/package.json frontend/pnpm-lock.yaml* ./
# Pin pnpm 9: reproducible, supports Node 22, and (unlike pnpm 10/11) doesn't block dependency build
# scripts, so esbuild's native binary is built for the vite build below.
RUN npm install -g pnpm@9 && (pnpm install --frozen-lockfile || pnpm install)
COPY frontend/ ./
RUN pnpm build

FROM nginx:1.27-alpine AS runtime
# API upstream is templated (envsubst at startup) so the same image serves both the host-run engine
# (API_UPSTREAM=host.docker.internal:8080, dev) and the containerized engine (engine:8080, `stack`).
COPY docker/default.conf.template /etc/nginx/templates/default.conf.template
COPY --from=builder /app/dist /usr/share/nginx/html
EXPOSE 80
# 127.0.0.1, not localhost: `listen 80` binds IPv4 only, while localhost resolves to ::1 first in
# this image — so the probe was refused on a container that was serving pages perfectly well, and the
# container sat permanently "unhealthy" (which any depends_on: service_healthy would wait on forever).
HEALTHCHECK --interval=30s --timeout=3s CMD wget -qO- http://127.0.0.1/ >/dev/null || exit 1
CMD ["nginx", "-g", "daemon off;"]
