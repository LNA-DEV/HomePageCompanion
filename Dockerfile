FROM golang:1.24 AS build

WORKDIR /app

COPY src .

# Run tests
# RUN go test ./...

RUN CGO_ENABLED=1 GOOS=linux go build -o home-page-companion

FROM golang:1.24 AS run

WORKDIR /app

COPY --from=build /app/home-page-companion .

HEALTHCHECK CMD curl --fail http://localhost:8080/health || exit 1

CMD ["./home-page-companion"]
