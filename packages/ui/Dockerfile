FROM node:22-alpine AS build
WORKDIR /workspace

RUN corepack enable && corepack prepare pnpm@10.11.0 --activate

COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
COPY packages/schema/package.json packages/schema/package.json
COPY packages/ui/package.json packages/ui/package.json
RUN pnpm install --filter @nanoflare/ui... --frozen-lockfile

COPY packages/schema packages/schema
COPY packages/ui packages/ui
RUN pnpm --filter @nanoflare/ui run build

FROM nginx:1.27-alpine

ENV NANOFLARED_UPSTREAM=http://nanoflared:8080

COPY packages/ui/nginx/default.conf.template /etc/nginx/templates/default.conf.template
COPY --from=build /workspace/packages/ui/dist/ /usr/share/nginx/html/

EXPOSE 80
