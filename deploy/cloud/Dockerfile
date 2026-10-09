FROM golang:1.26.9-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/monitor ./cmd/monitor
FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 monitor && mkdir -p /app/var && chown monitor:monitor /app/var
COPY --from=build /out/monitor /app/monitor
USER 10001:10001
WORKDIR /app
ENTRYPOINT ["/app/monitor", "--root", "/app", "run", "--managed", "cloud"]
