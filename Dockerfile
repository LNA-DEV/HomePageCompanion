# Build Go backend
FROM golang:1.24 AS build

WORKDIR /app

COPY src ./

RUN CGO_ENABLED=1 GOOS=linux go build -o home-page-companion

# Runtime
FROM debian:bookworm-slim AS run

RUN apt-get update && apt-get install -y ca-certificates curl && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY --from=build /app/home-page-companion .

EXPOSE 8080

CMD ["./home-page-companion"]
