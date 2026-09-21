FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/cfst .
RUN CGO_ENABLED=0 go build -trimpath -o /out/cfhost ./cmd/cfhost

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=build /out/cfst /app/cfst
COPY --from=build /out/cfhost /app/cfhost
COPY ip.txt ipv6.txt /app/
ENV DATA_DIR=/data \
    CFST_BIN=/app/cfst \
    CFST_IP_FILE=/app/ip.txt \
    CFST_IPV6_FILE=/app/ipv6.txt \
    HOSTS_PATH=/host/etc/hosts \
    TZ=Asia/Shanghai
VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["/app/cfhost"]
