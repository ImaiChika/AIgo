# AIgo 单机构生产部署

本目录提供第一版单机构生产部署骨架：AIgo 应用、PostgreSQL 和 Caddy 由 Docker Compose 编排；本地 Qwen 作为独立服务，通过内网地址接入，不和业务容器绑定。

当前部署底座、逻辑备份恢复、客户端加密异机上传和注册/登录防滥用已经过本地隔离环境验收，但在客户实际对象存储的 Object Lock、凭证权限、集中监控告警和本地模型容量验收完成前，仍不应直接承载正式客户数据。

## 1. 运行边界

- 只向宿主机发布 Caddy 的 80/443；应用 8080 和 PostgreSQL 5432 仅在容器网络内可见。
- PostgreSQL 健康后执行一次性 `migrate` 服务；迁移成功后应用启动；应用健康后 Caddy 才启动。
- 应用以 UID/GID 10001 非 root 用户运行，根文件系统只读，只有 `/app/output` 持久卷可写。
- PostgreSQL、应用输出和 Caddy 证书分别使用命名卷。
- 独立备份服务在部署后立即生成一次逻辑备份，之后按配置间隔运行；备份卷与数据库卷分离。
- JWT、数据库密码和初始管理员密码使用 Compose Secrets 文件，不写入镜像、Compose 环境变量或 Git。
- Caddy 输出 JSON 访问日志，执行 HTTP→HTTPS 跳转、健康检查和基础安全响应头。
- 备份使用 PostgreSQL custom archive，包含 SHA-256、Schema/应用/PostgreSQL 版本和核心表稳定指纹；自动按天数和份数清理。
- 可选的独立 `offsite` 服务用 `age` 公钥在应用服务器侧加密完整备份三件套，再以不可覆盖模式上传 S3 兼容对象存储；应用服务器不保存解密私钥。
- 自助注册默认开启。新注册用户只可登录和访问基础页面，不带角色、可分配业务权限或题库范围；管理员会在「用户管理」自动看到“新注册”账号并分配角色、直接权限和题库范围。
- 登录使用独立账号桶和来源IP桶做短时指数限速；公开注册另有来源IP频率限制。认证失败审计只保存账号/IP短哈希，不保存密码、Token或完整IP。

实现遵循 Docker 官方的[多阶段构建](https://docs.docker.com/build/building/multi-stage/)、[Compose Secrets](https://docs.docker.com/compose/how-tos/use-secrets/)和[健康依赖启动顺序](https://docs.docker.com/compose/how-tos/startup-order/)方案；HTTPS 由 Caddy 的[自动 HTTPS](https://caddyserver.com/docs/automatic-https)提供。

## 2. 服务器前置条件

- Linux 服务器安装 Docker Engine 和 Docker Compose 插件。
- 正式域名的 A/AAAA 记录已指向服务器。
- 防火墙允许公网访问 80/TCP、443/TCP 和 443/UDP。
- 如果使用本地 Qwen，模型服务已经部署，且其地址能从 `app` 容器访问；实时端点也可以在管理员登录后从 Web 配置。
- 客户已明确数据存储、备份位置、证书和 Secret 保管责任人。

不建议在首个商业版本引入 Kubernetes；单机构单机部署先使用 Compose，待容量和高可用需求明确后再升级编排层。

## 3. 首次部署

```bash
cp deploy/production.env.example deploy/production.env
./deploy/init-secrets.sh
```

编辑 `deploy/production.env`：

- `AIGO_SITE_ADDRESS` 填写正式域名，例如 `exam.example.com`，不要保留 `http://localhost`。
- 如需用环境变量首次预置本地端点，再填写 `QWEN_BASE_URL` 和 `QWEN_MODEL`；也可以先启动服务，再由超级管理员进入“AI 服务配置”填写实时地址、API Key、生成模型和检查模型。
- 根据服务器容量调整应用 CPU、内存和进程数限制。
- `AIGO_REGISTER_ENABLED=1` 是当前产品口径；遇到注册滥用或维护窗口时可临时设为 `0` 关闭。

检查并启动：

```bash
./deploy/compose.sh config --quiet
./deploy/compose.sh up -d --build
./deploy/compose.sh ps
./deploy/compose.sh logs --tail=100 migrate app caddy
```

验收：

```bash
curl -f https://你的域名/health/live
curl -f https://你的域名/health/ready
```

随后在浏览器完成管理员登录，进入“AI 服务配置”保存并启用实时端点，再进行题库、审核流程、审核结果和导出回归。初始管理员密码保存在 `deploy/secrets/admin_password`，不要打印到日志或发送到聊天工具；首次登录后立即修改密码，并把 Secret 文件纳入客户侧加密备份。API Key 会在数据库中加密保存，页面只显示脱敏值。

注册后验证：新用户应自动出现在管理员「用户管理」列表并标记为“新注册”，有效权限显示0项；管理员分配角色或权限前，该用户不得访问题库、审核、导出或系统管理接口。Compose 内仅应用容器信任 Caddy 注入的 `X-AIgo-Client-IP`，不要把应用8080端口发布到宿主机。

确认自动备份服务：

```bash
./deploy/compose.sh ps backup
./deploy/compose.sh logs --tail=50 backup
./deploy/backup.sh list
./deploy/backup.sh verify latest
```

## 4. 日常操作

```bash
# 状态
./deploy/compose.sh ps

# JSON 日志
./deploy/compose.sh logs -f --tail=200 app caddy postgres backup

# 停止并保留数据卷
./deploy/compose.sh down

# 重新启动
./deploy/compose.sh up -d
```

不要对客户环境执行 `docker compose down -v`，该参数会删除 PostgreSQL、应用输出、证书和备份卷。

## 5. 升级边界

上线前必须先创建并验证备份，再执行：

```bash
./deploy/backup.sh create
./deploy/backup.sh verify latest
./deploy/compose.sh build --pull app backup
./deploy/compose.sh up -d
```

每次启动都会等待数据库健康并执行独立迁移；已应用迁移会报告零变更。当前数据库迁移是前向版本，旧应用会拒绝连接更新版本的数据库，因此不能把“切回旧镜像”等同于完整回滚。正式回滚必须结合数据库备份恢复。

## 6. 本地备份与恢复演练

自动备份参数位于 `deploy/production.env`：

- `AIGO_BACKUP_INTERVAL_SECONDS`：备份间隔，默认 86400 秒。
- `AIGO_BACKUP_RETENTION_DAYS`：本地保留天数，默认 14 天。
- `AIGO_BACKUP_MAX_COUNT`：本地最多保留份数，默认 30 份。
- `AIGO_BACKUP_LOCK_TIMEOUT_SECONDS`：自动/手工操作互斥锁等待时间。

手工操作：

```bash
./deploy/backup.sh create
./deploy/backup.sh list
./deploy/backup.sh verify latest

# 恢复到临时数据库，校验后自动删除
./deploy/backup.sh drill latest aigo_restore_drill

# 恢复到新的保留数据库；禁止填写当前业务数据库名
./deploy/backup.sh restore <备份文件名> aigo_restored

# 导出备份卷，供人工应急转移
./deploy/backup.sh export /secure/offsite-staging/aigo
```

备份文件、`.sha256` 和 `.meta` 必须一起保留。单独保存在同一台 Docker 主机不能防范磁盘损坏、误删或主机入侵；正式环境应继续配置下一节的异机加密备份。`export` 仅作为人工应急入口，不替代自动异机链路。

恢复工具只接受本系统自行生成且来源可信的 archive。它始终恢复到新数据库，并拒绝覆盖当前业务数据库；恢复使用单事务、遇错退出，完成后执行 `ANALYZE`，并校验 Schema 和备份时稳定的核心表指纹。完成验证后，由运维在维护窗口修改数据库名并重启应用。

当前实现是单数据库逻辑备份，不包含 PostgreSQL 全局角色，也不是 WAL/PITR。Compose 会从 Secret 重建应用数据库角色；如客户要求更短 RPO，再单独建设 WAL 归档和时间点恢复。

## 7. 加密异机备份

异机备份位于独立的 `compose.offsite.yaml`，默认不启用，也不会让缺少 S3 配置的现有应用启动失败。它只读本地备份卷，使用独立状态卷和网络；远端不可用时本地备份、应用和数据库仍可继续运行。

保护边界如下：

- 本地 custom archive、`.sha256` 和 `.meta` 先完成源文件校验，再被打包并使用 [`age`](https://github.com/FiloSottile/age) 接收者公钥加密；私钥只保存在离线恢复介质。
- 加密包和加密包 SHA-256 先上传，完成回读哈希校验后，最后上传 receipt 作为成功标记。receipt 记录源备份哈希、密文哈希、公钥指纹和上传时间。
- 客户端使用 rclone [`--immutable`](https://rclone.org/docs/#immutable) 拒绝覆盖同名远端对象；同名源备份内容不一致、远端密文被篡改或下载校验文件异常都会失败。
- `--immutable` 不能阻止拥有删除权限的其他客户端。真正的不可变保留必须由 bucket 的版本控制和默认 Object Lock/WORM 策略提供，例如 [Amazon S3 Object Lock](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock.html)；上传凭证不得拥有 DeleteObject、删除版本或 BypassGovernanceRetention 权限。

### 7.1 一次性准备

在与应用服务器隔离的恢复工作站生成私钥，并把私钥保存在加密离线介质；只把输出的接收者公钥复制到 `deploy/production.env`：

```bash
age-keygen -o /离线加密介质/aigo-offsite-identity.txt
age-keygen -y /离线加密介质/aigo-offsite-identity.txt
```

由存储管理员预先创建专用 bucket，并完成以下验收：

1. 开启版本控制和默认 Object Lock/WORM 保留期；保留期必须覆盖客户约定的恢复窗口。
2. 创建仅供 AIgo 上传使用的最小权限凭证，只允许目标 bucket/prefix 的 List、Get、Put，显式禁止删除和绕过保留策略。
3. 使用 HTTPS 端点，确认服务器 CA 信任链、区域、path-style 要求和网络连通性。
4. 由另一管理员实际尝试覆盖、删除和绕过保留期，确认均被存储端拒绝并保存验收记录。

将 access key 和 secret key 分别写入以下仅管理员可读文件，不要写入 `production.env`、Git 或命令历史：

```text
deploy/secrets/offsite_access_key
deploy/secrets/offsite_secret_key
```

文件权限设为 `0600`，然后填写 `deploy/production.env` 中的 `AIGO_OFFSITE_*` 配置。仅在存储管理员完成上述 Object Lock 验收后，才将 `AIGO_OFFSITE_BUCKET_OBJECT_LOCK_CONFIRMED` 改为 `1`；生产默认要求该确认，否则上传服务拒绝运行。`AIGO_OFFSITE_ALLOW_INSECURE_ENDPOINT=1` 和 `AIGO_OFFSITE_REQUIRE_OBJECT_LOCK=0` 只允许隔离测试，不能用于客户环境。

### 7.2 启动与日常校验

```bash
# 启动独立异机备份服务；不修改主应用 Compose 文件
./deploy/offsite-compose.sh config --quiet
./deploy/offsite-compose.sh up -d --build offsite
./deploy/offsite-compose.sh ps offsite

# 立即扫描并上传本地所有尚未完成的备份
./deploy/offsite.sh upload

# 列出加密包、完整回读并校验最新备份
./deploy/offsite.sh list
./deploy/offsite.sh verify latest
```

服务默认每 300 秒扫描一次。上传成功会写本地 marker；即使状态卷丢失，也会读取远端 receipt、重新验证密文后恢复 marker，不会重复覆盖。连续失败会反映为容器 unhealthy/restart，当前仍需由后续监控系统把该状态发送给值班人员。

### 7.3 下载、离线解密和恢复

先将远端加密包下载到专用恢复目录，脚本会同时下载并校验密文 SHA-256，拒绝覆盖现有文件：

```bash
./deploy/offsite.sh fetch <备份.dump.tar.age> deploy/offsite-recovery/<恢复工单号>
```

将 `.tar.age` 安全转移到持有私钥的隔离恢复工作站，在加密磁盘中解密；不要把私钥复制到应用服务器：

```bash
mkdir decrypted
age -d -i /离线加密介质/aigo-offsite-identity.txt \
  <备份.dump.tar.age> | tar -xf - -C decrypted
```

核对解密后的 `.dump/.sha256/.meta` 三件套，将它们通过受控渠道送回恢复服务器的临时加密目录，再导入备份卷并沿用现有校验/演练/恢复入口：

```bash
./deploy/backup.sh import /secure/recovery/decrypted <备份.dump>
./deploy/backup.sh verify <备份.dump>
./deploy/backup.sh drill <备份.dump> aigo_restore_drill
./deploy/backup.sh restore <备份.dump> aigo_restored
```

恢复确认后按客户数据处置制度清理临时明文、离线工作站缓存和恢复数据库。不得删除仍在合规保留期内的远端版本。

本地隔离验收已经覆盖加密、不可覆盖上传、断网重试、进程重启、重复扫描、状态丢失、同名冲突、密文篡改、下载失败清理，以及“真实 PostgreSQL archive → 下载 → 离线解密 → 导入 → 指纹恢复演练”的完整链路。测试对象存储不支持真实 Object Lock，因此客户 S3/MinIO 的 WORM 和最小权限策略仍是每个部署必须单独完成的上线验收，不得仅凭本地测试判定通过。

## 8. HTTPS 说明

公网域名满足 DNS 指向、80/443 可达且 Caddy 数据卷持久化时，Caddy 会自动申请、续期证书并将 HTTP 重定向到 HTTPS。

内网域名或 `localhost` 会使用 Caddy 本地 CA，客户端必须显式信任该 CA。不要在正式客户环境通过关闭证书校验绕过信任配置。

## 9. 当前仍未完成

- 客户实际 S3/MinIO 的版本控制、默认 Object Lock/WORM、最小权限凭证及失败告警验收；更短 RPO 所需的 WAL/PITR。
- 镜像仓库、签名/SBOM、漏洞扫描和正式发布流水线。
- release/镜像保留与应用+数据库联合回滚策略。
- MFA/企业身份认证、密码找回、注册通知和集中式安全告警。
- 应用结构化日志、指标、集中日志、监控与告警。
- 本地 Qwen 的医学质量、并发容量、GPU 故障和升级回滚验收。
