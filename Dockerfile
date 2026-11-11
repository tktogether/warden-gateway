# syntax=docker/dockerfile:1
FROM golang:1.26-alpine AS build
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown
WORKDIR /src
RUN apk add --no-cache ca-certificates git
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X github.com/tktogether/warden-gateway/internal/version.Version=${VERSION} -X github.com/tktogether/warden-gateway/internal/version.Commit=${COMMIT} -X github.com/tktogether/warden-gateway/internal/version.BuildTime=${BUILD_TIME}" -o /out/warden-gateway ./cmd/warden-gateway

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && addgroup -S warden && adduser -S -G warden -H -s /sbin/nologin warden
COPY --from=build /out/warden-gateway /usr/local/bin/warden-gateway
EXPOSE 80 443 8443
ENTRYPOINT ["/usr/local/bin/warden-gateway"]
CMD ["serve", "--config", "/etc/warden-gateway/config.json"]
