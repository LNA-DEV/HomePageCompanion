# The companion in one container: the admin shell, the core and every module
# of build.yaml (docs/concepts/modular-architecture.md §5.6).

# The admin shell.
FROM node:22-alpine AS shell
WORKDIR /web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# The program: hpc build against this checkout.
FROM golang:1.26 AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=shell /web/build /shell
RUN go run ./cmd/hpc build -f build.yaml -o /out/companion --shell /shell --work /tmp/hpc --version "${VERSION}"

# glibc for SQLite, CA certificates and time zones; nothing else.
FROM gcr.io/distroless/base-debian12
WORKDIR /app
COPY --from=build /out/companion /app/companion
# Config and state live in the volume: data/companion.yaml, data/.env,
# data/db/, data/blobs/, data/secret.key. An old installation migrates in
# place: docker compose run --rm companion migrate
ENV COMPANION_CONFIG=/app/data/companion.yaml COMPANION_DATA=/app/data
VOLUME /app/data
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s CMD ["/app/companion", "health"]
ENTRYPOINT ["/app/companion"]
CMD ["serve"]
