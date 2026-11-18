FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /out/cfst .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /out/cfhost ./cmd/cfhost
RUN set -eu; \
    mkdir -p /out/licenses/Go; \
    cp LICENSE THIRD_PARTY_NOTICES.md /out/licenses/; \
    cp "$(go env GOROOT)/LICENSE" "$(go env GOROOT)/PATENTS" /out/licenses/Go/; \
    go list -m -f '{{if not .Main}}{{.Path}}|{{.Dir}}{{end}}' all > /tmp/cfhost-license-modules; \
    while IFS='|' read -r module dir; do \
        [ -n "$module" ] || continue; \
        [ -d "$dir" ]; \
        destination="/out/licenses/modules/$module"; \
        mkdir -p "$destination"; \
        find "$dir" -maxdepth 1 -type f \( -iname 'license*' -o -iname 'notice*' -o -iname 'copying*' -o -name PATENTS \) -exec cp '{}' "$destination/" \;; \
    done < /tmp/cfhost-license-modules

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=build /out/cfst /app/cfst
COPY --from=build /out/cfhost /app/cfhost
COPY --from=build /out/licenses/ /app/licenses/
COPY ip.txt ipv6.txt /app/
ENV DATA_DIR=/data \
    CFST_BIN=/app/cfst \
    CFST_IP_FILE=/app/ip.txt \
    CFST_IPV6_FILE=/app/ipv6.txt \
    HOSTS_PATH=/host/etc/hosts \
    TZ=Asia/Shanghai
VOLUME ["/data"]
EXPOSE 8080
STOPSIGNAL SIGTERM
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["/app/cfhost"]
