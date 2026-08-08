FROM --platform=$BUILDPLATFORM node:24.18.0-bookworm-slim AS web-build
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.25.4-bookworm AS go-build
WORKDIR /src
COPY go.mod go.sum ./
COPY vendor/ vendor/
COPY . ./
RUN CGO_ENABLED=0 go build -mod=vendor -trimpath -ldflags='-s -w' -o /out/zhixu-api ./cmd/api
RUN CGO_ENABLED=0 go build -mod=vendor -trimpath -ldflags='-s -w' -o /out/zhixu-worker ./cmd/worker
RUN CGO_ENABLED=0 go build -mod=vendor -trimpath -ldflags='-s -w' -o /out/zhixu-migrate ./cmd/migrate
RUN CGO_ENABLED=0 go build -mod=vendor -trimpath -ldflags='-s -w' -o /out/zhixu-modelctl ./cmd/modelctl
RUN CGO_ENABLED=0 go build -mod=vendor -trimpath -ldflags='-s -w' -o /out/zhixu-workspace-probe ./cmd/workspaceprobe

FROM go-build AS workspace-control-build
ARG ZHIXU_HOST_GOOS
ARG ZHIXU_HOST_GOARCH
RUN test "${ZHIXU_HOST_GOOS}" = darwin -o "${ZHIXU_HOST_GOOS}" = linux
RUN test "${ZHIXU_HOST_GOARCH}" = amd64 -o "${ZHIXU_HOST_GOARCH}" = arm64
RUN CGO_ENABLED=0 GOOS="${ZHIXU_HOST_GOOS}" GOARCH="${ZHIXU_HOST_GOARCH}" \
  go build -mod=vendor -trimpath -ldflags='-s -w' -o /host/zhixu-workspacectl ./cmd/workspacectl

FROM scratch AS workspace-control-bundle
COPY --from=workspace-control-build /host/zhixu-workspacectl /zhixu-workspacectl

FROM alpine:3.22 AS runtime
RUN apk add --no-cache ca-certificates git iproute2 iptables socat \
  && addgroup -S -g 10001 zhixu \
  && adduser -S -D -H -u 10001 -G zhixu zhixu
WORKDIR /app
COPY --from=go-build /out/zhixu-api /app/zhixu-api
COPY --from=go-build /out/zhixu-worker /app/zhixu-worker
COPY --from=go-build /out/zhixu-migrate /app/zhixu-migrate
COPY --from=go-build /out/zhixu-modelctl /app/zhixu-modelctl
COPY --from=go-build /out/zhixu-workspace-probe /app/zhixu-workspace-probe
COPY --from=web-build /src/web/dist /app/web
COPY --chmod=0755 deploy/loopback-firewall.sh /app/loopback-firewall.sh
COPY --chmod=0755 deploy/model-secrets-init.sh /app/model-secrets-init.sh
USER 10001:10001
ENV ZHIXU_HTTP_ADDR=0.0.0.0:8080 \
    ZHIXU_WEB_ASSETS_DIR=/app/web
EXPOSE 8080 8081
ENTRYPOINT ["/app/zhixu-api"]
