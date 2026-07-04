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

FROM scratch AS packages
COPY --from=go-build /out/ /bin/
COPY --from=ui-build /src/packages/ui/dist/ /ui/
COPY packages/worker-sdk/ /packages/worker-sdk/
