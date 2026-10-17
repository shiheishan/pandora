# syntax=docker/dockerfile:1
#
# Pandora Docker 镜像：一个 Dockerfile 五个阶段。
# - release-build：在构建机平台编译两架构发布树（node + Go，跑 build-release.sh）。
# - release：scratch，只放 amd64/arm64 发布目录；发版可用 --build-context release=<目录> 替换本阶段。
# - pandora：distroless 运行 serve/worker/migrate 用的面板二进制与 geoip、pdnd-dist。
# - pandora-nginx：nginx 边缘，入口脚本渲染 nginx-pandora.conf。
# - pandora-ops：postgres 同主版本 + age/openssl，维护 migrate/backup/psql 等。
#
# 构建上下文为仓库根目录，例如：
#   HUGEICONS_NPM_TOKEN=… docker build -f panel/deploy/docker/Dockerfile \
#     --secret id=hugeicons_npm_token,env=HUGEICONS_NPM_TOKEN --target pandora .
# （从源码构建要装前端私有图标包，没有令牌 npm ci 回 401；用 --build-context release=… 替换 release 阶段时不需要。）
# 发版构建可覆盖 release 阶段（同名构建上下文会替换同名阶段）：
#   docker build -f panel/deploy/docker/Dockerfile --build-context release=<已验 tarball 解开的目录> --target pandora .

ARG PANDORA_VERSION=dev
ARG PANDORA_REVISION=unknown

# 构建阶段钉在 $BUILDPLATFORM，不引用 TARGETARCH，多架构镜像构建时 release-build 只跑一次。
# build-release.sh 按各 go.mod 的 go 指令设 GOTOOLCHAIN，并下载核对 ip2region 离线库。
FROM --platform=$BUILDPLATFORM node:22-bookworm AS release-build
COPY --from=golang:1.26-bookworm /usr/local/go /usr/local/go
ENV PATH=/usr/local/go/bin:$PATH
WORKDIR /src
COPY . .
ARG PANDORA_VERSION
# 前端的 @hugeicons-pro 走私有 npm 源：令牌经 BuildKit secret 只在这一步作为环境变量出现
# （docker build --secret id=hugeicons_npm_token,env=HUGEICONS_NPM_TOKEN），不进构建参数、不进镜像层；
# 写进 npmrc 的只是 ${NODE_AUTH_TOKEN} 占位，由 npm 运行时展开，用完即删。
RUN --mount=type=secret,id=hugeicons_npm_token,env=NODE_AUTH_TOKEN \
    --mount=type=cache,target=/root/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/root/.npm \
    export NPM_CONFIG_USERCONFIG=/tmp/hugeicons.npmrc \
 && printf '%s\n' '@hugeicons-pro:registry=https://npm.collectui.pro/' '//npm.collectui.pro/:_authToken=${NODE_AUTH_TOKEN}' > "$NPM_CONFIG_USERCONFIG" \
 && PANDORA_VERSION="$PANDORA_VERSION" bash panel/deploy/build-release.sh /tmp/dist \
 && rm -f "$NPM_CONFIG_USERCONFIG" \
 && mkdir /release \
 && mv "/tmp/dist/pandora-panel_${PANDORA_VERSION}_linux_amd64" "/tmp/dist/pandora-panel_${PANDORA_VERSION}_linux_arm64" /release/

FROM scratch AS release
COPY --from=release-build /release/ /

# 无 shell、非 root（65532）、根文件系统可只读挂载。
# 不设 PANDORA_LOG_FILE，日志走 stderr，由 Docker 日志驱动采集。
FROM gcr.io/distroless/static-debian12:nonroot AS pandora
ARG TARGETARCH
ARG PANDORA_VERSION
ARG PANDORA_REVISION
COPY --from=release /pandora-panel_${PANDORA_VERSION}_linux_${TARGETARCH}/bin/pandora /opt/pandora/bin/pandora
COPY --from=release /pandora-panel_${PANDORA_VERSION}_linux_${TARGETARCH}/pdnd-dist/ /opt/pandora/pdnd-dist/
COPY --from=release /pandora-panel_${PANDORA_VERSION}_linux_${TARGETARCH}/geoip/ /opt/pandora/geoip/
WORKDIR /opt/pandora
USER 65532:65532
ENTRYPOINT ["/opt/pandora/bin/pandora"]
CMD ["serve"]
LABEL org.opencontainers.image.title="pandora" \
      org.opencontainers.image.description="Pandora panel: serve / worker / migrate" \
      org.opencontainers.image.source="https://github.com/shiheishan/pandora" \
      org.opencontainers.image.version="${PANDORA_VERSION}" \
      org.opencontainers.image.revision="${PANDORA_REVISION}" \
      org.opencontainers.image.licenses="GPL-3.0-only"

# 无 RUN：arm64 镜像不需要模拟器；目录与默认站点在 nginx-entrypoint.sh 里处理。
FROM nginx:1.28-bookworm AS pandora-nginx
ARG TARGETARCH
ARG PANDORA_VERSION
ARG PANDORA_REVISION
COPY --from=release --chmod=0755 /pandora-panel_${PANDORA_VERSION}_linux_${TARGETARCH}/deploy/render-nginx.sh /opt/pandora/deploy/render-nginx.sh
COPY --from=release --chmod=0644 /pandora-panel_${PANDORA_VERSION}_linux_${TARGETARCH}/deploy/nginx-pandora.conf /opt/pandora/deploy/nginx-pandora.conf
COPY --chmod=0755 panel/deploy/docker/nginx-entrypoint.sh /opt/pandora/docker/nginx-entrypoint.sh
STOPSIGNAL SIGQUIT
ENTRYPOINT ["/opt/pandora/docker/nginx-entrypoint.sh"]
LABEL org.opencontainers.image.title="pandora-nginx" \
      org.opencontainers.image.description="Pandora edge nginx rendered from nginx-pandora.conf" \
      org.opencontainers.image.source="https://github.com/shiheishan/pandora" \
      org.opencontainers.image.version="${PANDORA_VERSION}" \
      org.opencontainers.image.revision="${PANDORA_REVISION}" \
      org.opencontainers.image.licenses="GPL-3.0-only"

# 基础镜像与 compose 里 PostgreSQL 同主版本，pg_dump 与服务端版本一致。
# /var/backups/pandora 组 65532（= pandora 镜像运行用户），命名卷首次挂载继承属主，serve 只读挂载后系统状态页能列出备份。
# 部署脚本要求程序与 /etc/pandora 归 root 且不可他写；COPY 缺省 root 所有。
FROM postgres:18-bookworm AS pandora-ops
ARG TARGETARCH
ARG PANDORA_VERSION
ARG PANDORA_REVISION
RUN apt-get update && apt-get install -y --no-install-recommends age openssl && rm -rf /var/lib/apt/lists/*
COPY --from=release /pandora-panel_${PANDORA_VERSION}_linux_${TARGETARCH}/bin/pandora /opt/pandora/bin/pandora
COPY --from=release /pandora-panel_${PANDORA_VERSION}_linux_${TARGETARCH}/deploy/ /opt/pandora/deploy/
COPY --chmod=0755 panel/deploy/docker/ops.sh /opt/pandora/docker/ops.sh
COPY --chmod=0644 panel/deploy/docker/.env.example /opt/pandora/docker/env.example
RUN install -d -o root -g root -m 0755 /etc/pandora && install -d -o root -g 65532 -m 0750 /var/backups/pandora
ENV PANDORA_IMAGE_VERSION=${PANDORA_VERSION}
ENTRYPOINT ["/opt/pandora/docker/ops.sh"]
CMD ["help"]
LABEL org.opencontainers.image.title="pandora-ops" \
      org.opencontainers.image.description="Pandora maintenance: migrate, backup, restore, psql" \
      org.opencontainers.image.source="https://github.com/shiheishan/pandora" \
      org.opencontainers.image.version="${PANDORA_VERSION}" \
      org.opencontainers.image.revision="${PANDORA_REVISION}" \
      org.opencontainers.image.licenses="GPL-3.0-only"
