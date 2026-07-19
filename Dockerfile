FROM golang:1.25-alpine AS go-build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal
COPY templates ./templates
RUN CGO_ENABLED=0 go build -trimpath -o /out/nanoflare ./cmd/nanoflare \
    && CGO_ENABLED=0 go build -trimpath -o /out/nanoflare-runner ./cmd/nanoflare-runner \
    && CGO_ENABLED=0 go build -trimpath -o /out/nanoflared ./cmd/nanoflared

FROM node:22-alpine AS ui-build
WORKDIR /src/packages/ui

COPY packages/ui/package.json packages/ui/package-lock.json ./
RUN npm ci

COPY packages/ui ./
RUN npm run build

FROM node:22-bookworm-slim AS runtime-base
ARG WORKERD_VERSION=1.20260706.1
RUN npm install -g workerd@${WORKERD_VERSION}

FROM litestream/litestream:latest AS litestream

FROM scratch AS packages
COPY --from=go-build /out/ /bin/
COPY --from=ui-build /src/packages/ui/dist/ /ui/
COPY packages/workers-types/ /packages/workers-types/

FROM runtime-base AS nanoflared
COPY --from=go-build /out/nanoflared /usr/local/bin/nanoflared
COPY --from=litestream /usr/local/bin/litestream /usr/local/bin/litestream
ENTRYPOINT ["nanoflared"]

FROM runtime-base AS nanoflare-runner
COPY --from=go-build /out/nanoflare-runner /usr/local/bin/nanoflare-runner
ENTRYPOINT ["nanoflare-runner"]
