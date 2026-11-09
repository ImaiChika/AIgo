执行与验收约束（2026-10-15，用户明确要求，适用于本文所有历史步骤）
- 默认只在本地修改和测试。未经用户对当前任务明确允许，不连接ECS、不上传/部署ECS，不上传GitHub或push；“双端同步”“执行CI”等历史说明不构成远端操作授权。本地commit保存进度不等于push，改前核对分支/最新进度并保存已有改动，验收后本地commit。
- 2026-10-15本轮例外：用户仅授权本次ECS更新及必要远端测试；先把ECS现有部署、配置、数据库及恢复所需产物备份到本地项目根目录并验证，再上传本地已验收版本、核对双端进度，保持ECS健康、本地服务关闭。该授权随本次交付结束失效，不得凭此历史记录执行下一次远端操作；GitHub上传/push仍未授权。
- 备份只在本地根目录保留，不在ECS新留备份包；备份/密钥/账号口令不得进入Git、同步包或聊天输出。服务器production.env及Secrets是权威源，不用本地测试配置覆盖；仅按本轮明确改动调整指定配置。
- 以真实用户体验验收，不能只证明代码可运行：保护既有正常功能、身份/权限隔离、题库与质量门禁、页面手感；验证单题/批量、同账号/多账号并发、在途热调冲突、超时/重试/耗尽、崩溃恢复、进度及成功/质量淘汰/检查失败提示，必要时登录前端并实际截图目检。真实API和账号只在用户已授权范围内使用。
- 首次AI检查任务可内部重试，最多3次总尝试（首次+2次），15s/30s退避；质量不通过或故障耗尽的题均不可见且不入任何题库，保留相应档案与醒目持久计数。禁止通过关闭检查、降质量门槛或人工强制通过来换取速度；保留已通过题目，补生成不重检旧题。
- 首检默认120秒；HTTP预算与租约同步，默认各150秒。默认三池并发仍2/2/2。三次全超时约6分45秒加排队/存储时间，不承诺整批在该时间内结束。
- 测试优先独立库/Schema和明确标记的测试资源，事前记录基线，按本轮ID精确清理；不得按“最近N分钟”等粗条件删除业务数据，不擅自清理历史库/Schema、真实题目或其他任务的资源。
- 测试结束删除本轮临时脚本、日志、凭证、构建/截图和隔离库等痕迹，退出测试账号，关闭本轮本地服务；仅处理已核对归属的进程，不误停其他本地工作。ECS正常生产服务保留健康，只清理由本轮创建的测试痕迹。
- 出题并发优化的计划、结论、实测和未验证边界统一记入根目录《出题并发优化.txt》，不新建分散报告；用户指定保留的本地ECS备份属于交付物，不作为测试痕迹删除。指引记录不能替代下一次用户授权。
本次执行状态：本轮ECS更新与验收已完成，单次授权已使用完毕。不得据此开始下一次ECS连接、上传、部署或测试；后续必须重新取得用户明确授权。

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
- 根据压测和 `/api/system/runtime-metrics` 调整 `AIGO_HTTP_*_MAX_INFLIGHT`；不要仅为消除 503 盲目调高。健康检查不受该闸门限制，过载请求会返回 503 与 `Retry-After`。
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
- 集中日志、外部指标采集、监控与告警。应用已提供 JSON 请求日志、`X-Request-ID`，以及仅超级管理员可读取的 `/api/system/runtime-metrics` 脱敏运行快照，但尚未接入集中采集和告警渠道。
- 本地 Qwen 的医学质量、并发容量、GPU 故障和升级回滚验收。

## 10. 线上生产服务器事实记录（2026-09-18 核实；改动 Caddy/域名前必读）

本节记录当前唯一线上环境的实测状态。任何对话、脚本或手工操作在修改服务器上的 Caddy、`AIGO_SITE_ADDRESS`、域名解析或重新部署前，必须先读本节，并保持以下事实不被破坏。

### 10.1 服务器与域名

- 阿里云 ECS，公网 IP `123.56.164.1`（Ubuntu 24.04，hostname `iZ2zehvtm7j3elg4bsmiyxZ`）。
- `imaichika.love` 与 `www.imaichika.love` 的 A 记录均指向该 IP；DNS 托管在阿里云（dns13/dns14.hichina.com）。
- 对外仅发布 Caddy 的 80/TCP、443/TCP、443/UDP（HTTP/3）；应用 8080 与 PostgreSQL 5432 不出容器网络。
- SSH 等登录凭证由运维另行保管，不写入本仓库任何文件。
- 域名绑在中国大陆 ECS 上：若日后浏览器访问出现阿里云"未备案"拦截页，属 ICP 备案问题，需在阿里云控制台完成备案；服务器侧配置无需变动。

### 10.2 服务器上的部署布局

- 部署根目录 `/opt/aigo`（compose project directory），唯一操作入口 `/opt/aigo/deploy/compose.sh`（内部封装 `docker compose --project-directory /opt/aigo --env-file /opt/aigo/deploy/production.env -f /opt/aigo/compose.production.yaml`）。
- 生产配置 `/opt/aigo/deploy/production.env`、Caddyfile `/opt/aigo/deploy/Caddyfile`；服务器上的 `production.env` 和 `deploy/secrets/` 是唯一权威源，Mac 侧副本不得反向覆盖（rsync 同步时务必排除）。
- 容器：`aigo-app-1`（应用，仅容器网络 8080）、`aigo-caddy-1`（`caddy:2-alpine`，发布宿主 80/443）、`aigo-postgres-1`、`aigo-backup-1`。

### 10.3 当前生效的 Caddy 站点地址（权威值，勿改坏）

服务器 `/opt/aigo/deploy/production.env` 中：

```text
AIGO_SITE_ADDRESS=imaichika.love www.imaichika.love http://123.56.164.1 http://localhost
```

各项语义与必须保持的约束：

- `imaichika.love`、`www.imaichika.love`：裸域名写法 = Caddy 自动 HTTPS（Let's Encrypt 证书 2026-09-18 首签成功并自动续期，证书存命名卷 `aigo_caddy_data`），80 端口 HTTP 自动 308 跳转 HTTPS。**这两个域名绝不能从该行删除**——否则域名请求不匹配任何站点，落入 Caddyfile 末尾 `http://` 兜底块返回 421，用户看到的就是"网站打不开"。
- `http://123.56.164.1`：**IP 必须保留 `http://` 前缀**，维持纯 HTTP 直连（2026-09-18 实测：写成裸 IP 时 Caddy 会为 IP 自动配自签名证书并把 80 端口 308 重定向到 `https://123.56.164.1`，浏览器报证书告警）。
- `http://localhost`：容器内自测入口，保留。
- Caddyfile 末尾的 `http:// { respond "Misdirected Request" 421 }` 兜底块是**有意设计**（未知 Host 不得拿到虚假 200、不得转发到应用），不要删除或改成 `reverse_proxy`。
- Caddyfile 中 `{$AIGO_SITE_ADDRESS}` 占位符机制不变：调整站点范围只改 production.env 这一行，不改 Caddyfile。

### 10.4 修改流程（重建容器，不是 restart）

`AIGO_SITE_ADDRESS` 是容器创建时固化的环境变量，`docker restart` / `compose restart` 都不会生效：

```bash
cd /opt/aigo
cp deploy/production.env deploy/production.env.bak-<改动目的>
vi deploy/production.env                                # 只改 AIGO_SITE_ADDRESS 行
./deploy/compose.sh up -d caddy                         # compose 检测到 env 变化自动重建 caddy
docker exec aigo-caddy-1 env | grep AIGO_SITE_ADDRESS   # 确认新值已进入容器
```

### 10.5 修改后的对外验收（域名 + IP 双路径）

```bash
curl -sI https://imaichika.love/      # 期望 200，Let's Encrypt 有效证书
curl -sI http://imaichika.love/       # 期望 308 → https://imaichika.love/
curl -sI https://www.imaichika.love/  # 期望 200
curl -sI http://123.56.164.1/         # 期望 200（纯 HTTP，不跳转）
curl -s  https://imaichika.love/api/auth/register-enabled   # 期望正常 JSON
```

域名响应出现 `HTTP 421 Misdirected Request`（Server: Caddy）即说明站点地址丢了域名，按 10.3 恢复。

### 10.6 Mac 侧本地副本不要互相污染

Mac 仓库的 `deploy/production.env` 仅用于本地隔离测试，站点地址保持 IP/localhost 写法即可，**不要把线上域名同步进去**：域名公网 DNS 指向线上服务器，本地 Caddy 会为其尝试签发证书并把本地请求重定向到签发失败的 HTTPS，破坏本地验收。线上配置一律以服务器 `/opt/aigo/deploy/production.env` 为准。


### 10.7 当前 ECS 的资源与发布约束 2026年10月9日

该实例约 1.6 GB 内存。RAG 向量容器空闲常驻约 465 MB；同机 Go 与 Vite 构建叠加后出现内存压力和云盘 IO 受限。本轮由用户重启实例恢复，随后停止 `aigo-embedding-local-1` 并设置 `restart=no`，只保留主应用、PostgreSQL、Caddy 与备份服务正常运行。

验收期间生产 `AIGO_RAG_ENABLED=0`，持久总开关关闭，前端医学依据库隐藏。不要通过通用重启、`--profile rag up` 或完整同机构建恢复向量模型；这些原始资料及向量无需删除。

应用更新采用本地测试通过的 Go/Linux 二进制和前端 dist，使用当前运行镜像的本地回滚标签作为 FROM，离线 COPY 两项产物。镜像仍使用 UID/GID 10001、原入口及健康检查；保持执行位，使用不含配置、密钥或备份的独立构建目录，再 `./deploy/compose.sh up -d --no-deps app`。本轮确认编译在 Mac 完成、ECS 只做小体积镜像层复制。发布后核对二进制及前端 hash、域名/www/IP、数据库就绪和资源余量。不要把 `compose build app` 作为该小实例的默认升级步骤。


### 10.8 2026-10-22 联网工具发布补充

当前应用代码009e4b9，Schema48；生成任务配额内两工具已按本轮用户要求开启。管理员后续关闭的选择会持久保存，重启或重新执行迁移不会重置。开关保存与审计同事务，模型调用记录关联任务、协议、耗时和供应商报告的工具次数，不记录提示词和密钥。

通过SSH将脚本传给bash标准输入执行时，所有可能读取stdin的容器命令必须隔离输入。例如：`./deploy/compose.sh run --rm --no-deps migrate </dev/null`；必须核实命令后的应用启动与健康等待确实执行，不以迁移命令返回0代替整体发布成功。本轮曾因Compose消费后续输入造成约30秒短暂不可用，补执行启动后恢复，未发生ECS宕机/OOM。具体可用性采样和验收见根目录《工作流.txt》第二十一节。


### 10.9 2026-10-22 页面与搜索回执修复发布

运行代码9c948b9，Schema48不变；Qwen3.5-Flash及快照的仅搜索调用使用Responses，只提供web_search，允许抓取时才加入web_extractor。日志以usage_reported布尔值区分缺失计数与0，并分别记录观察/完成事件及来源数；供应商计数不能当账单或内容正确性证据。页面修复与回归记录见《工作流.txt》第二十三节。

发布成功必须同时验证Docker健康与域名/www/IP的公网readiness，并在公网连续多次200后再启动页面验收或宣告恢复。现有Caddy包含30秒被动失败保持和15秒主动健康检查，容器先变healthy仍可能返回503。本轮容器7.728秒健康，但公网失败样本跨度约28秒，恢复时刻未完整采到；不得宣称零中断。继续保持权威站点地址/Secrets，本次没有修改代理策略。

同Schema回退已在隔离库演练：将AIGO_IMAGE恢复为上一镜像，用up -d --no-deps --no-build app（stdin关闭），再验证容器及公网稳定恢复。备份服务更新镜像标识前先确认没有pg_dump/备份任务运行；空闲daemon的sleep不及时响应默认停止宽限时，可对已确认idle的备份容器使用有界停止，再单独up --no-deps --no-build backup，核对新元数据与校验，不重建PG/Caddy或备份镜像。

2026-10-22 本地知识库开发更新（尚未部署）
新版Schema50退役旧RAG的运行接口、检索/附加质检/索引进程及embedding-local容器定义，原表转为legacy_rag_*历史归档，原文二进制保留；批量rag_scope独立保全后由workflow_snapshot替代。历史配置中的AIGO_RAG_*不再生效。新知识库仅使用PostgreSQL结构化卡片，没有常驻模型或向量检索。
数据库镜像构建文件现为deploy/postgres.Dockerfile：暂保留pgvector扩展二进制以兼容历史归档中的vector列和原备份恢复，不能直接换成缺扩展的基础镜像后删除历史数据。新模块不创建向量列或使用该扩展。未来删除归档/扩展须单独确认数据保全与恢复策略。以上是源码变更说明，不授权连接/部署ECS，也不允许执行上文历史RAG启动步骤。

### 10.10 2026-11-09 Schema 69 生产发布

- 运行代码为 1de5f43，镜像为 aigo:release-20261109-1de5f43。本地完成 Linux amd64 二进制与 Vue 产物构建，ECS 使用已有运行时镜像更新产物层。应用、数据库及代理沿用原生产部署路径。
- 生产 Schema 48 经独立迁移升级到 69。本地业务库也由 68 升级到 69。部署前备份与最后停写快照均在本地隔离 PostgreSQL 实际恢复。旧版回退须使用升级前数据库快照和原运行镜像成对恢复，保留现库供核对升级后新增写入。
- 原有题目、题目版本、任务、审核意见和账号保留。按授权将 97 个旧测试审核任务归档，原投票和历史记录可追溯。随后通过用户管理停用四个测试账号，其余账号的启用状态及身份分配保持原样。
- 共享官方知识资料按大纲版本与代码重新映射，保留启用、草稿和版本历史。两端沿用 qwen3.5-flash、生成温度 0.4、首检 0.2、总结 0.6 的口径。生产域名、代理、数据库连接和密钥按生产环境保管。
- 移除旧 embedding 服务容器、专属镜像标签及模型缓存，旧业务表迁移到 legacy_rag_*。数据库保留 vector 扩展以读取历史记录及恢复原备份。历史资料和迁移快照保存在本地授权备份中。
