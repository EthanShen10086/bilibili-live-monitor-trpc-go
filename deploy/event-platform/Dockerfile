FROM golang:1.26.9-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/event-platform ./cmd/event-platform
FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata wget && adduser -D -u 10001 platform
COPY --from=build /out/event-platform /app/event-platform
COPY deploy/event-platform/trpc_go.yaml /app/trpc_go.yaml
USER 10001:10001
WORKDIR /app
ENTRYPOINT ["/app/event-platform", "--trpc-config", "/app/trpc_go.yaml"]
