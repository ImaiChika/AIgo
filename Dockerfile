FROM node:22-alpine@sha256:c610fcdfb1d5b4740dd70c284ed3cb16bb857e0f7166196e36a5501df7a3aa32 AS frontend-build
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
ENV NODE_OPTIONS=--max-old-space-size=384
COPY web/ ./
RUN npm run build

FROM golang:1.26.9-alpine@sha256:3082400e369fa24d5fc60bca20edab3f6d604e0c5a690ec66b295eff4dd87ade AS backend-build
WORKDIR /src
ENV GOPROXY=https://goproxy.cn,direct
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN GOMAXPROCS=1 GOMEMLIMIT=384MiB CGO_ENABLED=0 GOOS=linux go build -p 1 -trimpath -ldflags="-s -w" -o /out/aigo ./cmd/aigo

FROM postgres:16-alpine@sha256:cf78e76683b9ca8c5733cbbdce6c9262b45b6767934dd0a95e671f9a0fc20685 AS backup
RUN addgroup -S -g 10001 aigo-backup \
    && adduser -S -D -H -u 10001 -G aigo-backup aigo-backup \
    && mkdir -p /backups \
    && chown -R aigo-backup:aigo-backup /backups
COPY --chown=aigo-backup:aigo-backup deploy/backup-tool.sh /usr/local/bin/aigo-backup
RUN chmod 0555 /usr/local/bin/aigo-backup
USER 10001:10001
HEALTHCHECK --interval=1m --timeout=10s --start-period=2m --retries=3 \
  CMD ["/usr/local/bin/aigo-backup", "health"]
ENTRYPOINT ["/usr/local/bin/aigo-backup"]
CMD ["daemon"]

FROM alpine:3.23@sha256:fd791d74b68913cbb027c6546007b3f0d3bc45125f797758156952bc2d6daf40 AS offsite
RUN apk add --no-cache age rclone ca-certificates tzdata \
    && addgroup -S -g 10001 aigo-offsite \
    && adduser -S -D -H -u 10001 -G aigo-offsite aigo-offsite \
    && mkdir -p /state /backups \
    && chown -R aigo-offsite:aigo-offsite /state /backups
COPY --chown=aigo-offsite:aigo-offsite deploy/offsite-tool.sh /usr/local/bin/aigo-offsite
RUN chmod 0555 /usr/local/bin/aigo-offsite
USER 10001:10001
HEALTHCHECK --interval=1m --timeout=10s --start-period=2m --retries=3 \
  CMD ["/usr/local/bin/aigo-offsite", "health"]
ENTRYPOINT ["/usr/local/bin/aigo-offsite"]
CMD ["daemon"]

FROM alpine:3.23@sha256:fd791d74b68913cbb027c6546007b3f0d3bc45125f797758156952bc2d6daf40 AS runtime
RUN sed -i 's#https://dl-cdn.alpinelinux.org/alpine#https://mirrors.aliyun.com/alpine#g' /etc/apk/repositories \
    && apk add --no-cache ca-certificates tzdata poppler-utils \
    && addgroup -S -g 10001 aigo \
    && adduser -S -D -H -u 10001 -G aigo aigo \
    && mkdir -p /app/output /app/configs \
    && chown -R aigo:aigo /app

WORKDIR /app
COPY --from=backend-build --chown=aigo:aigo /out/aigo /app/aigo
COPY --from=frontend-build --chown=aigo:aigo /src/web/dist /app/web
COPY --chown=aigo:aigo configs/review_flows.json /app/configs/review_flows.json

ENV AIGO_ENV_FILE=- \
    AIGO_HTTP_ADDR=0.0.0.0:8080 \
    AIGO_WEB_DIST_DIR=/app/web \
    AIGO_REGISTER_ENABLED=1 \
    TZ=Asia/Shanghai

USER 10001:10001
EXPOSE 8080
HEALTHCHECK --interval=15s --timeout=3s --start-period=20s --retries=4 \
  CMD wget -q -O /dev/null http://127.0.0.1:8080/health/ready || exit 1

ENTRYPOINT ["/app/aigo"]
CMD ["serve"]
