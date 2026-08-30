ARG GO_IMAGE=golang:1.26.7-bookworm@sha256:6ef6e30f0ea5c384f6d111cf856e024e3086bbdcb1779da3f3b3fbba0aea53d2
ARG GDAL_IMAGE=ghcr.io/osgeo/gdal@sha256:44fee7d4f9be0966851d7b14a0a387216897d8347f9e0ebc4e812f7217bc39d6

FROM ${GO_IMAGE} AS build
WORKDIR /src

ARG GOPROXY=https://goproxy.cn,direct
COPY go.mod go.sum ./
RUN GOPROXY=${GOPROXY} go mod download && go mod verify

COPY cmd ./cmd
COPY internal ./internal

ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -mod=readonly -trimpath -buildvcs=false \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/ai-gdm-server ./cmd/server && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -mod=readonly -trimpath -buildvcs=false \
    -ldflags="-s -w" -o /out/ai-gdm-healthcheck ./cmd/healthcheck

FROM ${GDAL_IMAGE}

ARG VERSION=dev
ARG VCS_REF=unknown
ARG BUILD_DATE=unknown

LABEL org.opencontainers.image.title="AI-GDM" \
      org.opencontainers.image.description="地质灾害监控与辅助决策 Web 服务" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${VCS_REF}" \
      org.opencontainers.image.created="${BUILD_DATE}" \
      org.opencontainers.image.source="https://github.com/Requim/AI-GDM" \
      ai.gdm.gdal.version="3.13.3"

RUN groupadd --gid 10001 ai-gdm && \
    useradd --uid 10001 --gid 10001 --no-create-home \
      --home-dir /var/lib/ai-gdm --shell /usr/sbin/nologin ai-gdm && \
    install -d -o ai-gdm -g ai-gdm -m 0750 \
      /var/lib/ai-gdm /var/lib/ai-gdm/lhasa /tmp/ai-gdm

COPY --from=build --chown=root:root /out/ai-gdm-server /usr/local/bin/ai-gdm-server
COPY --from=build --chown=root:root /out/ai-gdm-healthcheck /usr/local/bin/ai-gdm-healthcheck

ENV APP_HTTP_ADDR=:8080 \
    GDAL_BINARY=/usr/bin/gdal \
    GDAL_TEMP_DIR=/tmp/ai-gdm \
    HOME=/var/lib/ai-gdm \
    LHASA_DATA_DIR=/var/lib/ai-gdm/lhasa

VOLUME ["/var/lib/ai-gdm/lhasa"]
EXPOSE 8080
USER 10001:10001
STOPSIGNAL SIGTERM

HEALTHCHECK --interval=30s --timeout=5s --start-period=60s --retries=3 \
  CMD ["/usr/local/bin/ai-gdm-healthcheck", "http://127.0.0.1:8080/readyz"]

ENTRYPOINT ["/usr/local/bin/ai-gdm-server"]
