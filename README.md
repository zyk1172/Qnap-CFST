<p align="center">
  <img src="cmd/cfhost/web/assets/cfhost-logo.svg" alt="CFHost Logo" width="180">
</p>

<h1 align="center">CFHost</h1>

<p align="center"><strong>面向威联通、群晖、飞牛及其他 NAS 的 Cloudflare 优选、域名验证与 Hosts 自动管理服务。</strong></p>

<p align="center">
  <code>QNAP / Synology / fnOS</code> · <code>Cloudflare 优选</code> · <code>Smart Repair</code> · <code>Tracker announce</code> · <code>Hosts 管理</code>
</p>

CFHost 基于 [XIU2/CloudflareSpeedTest](https://github.com/XIU2/CloudflareSpeedTest) 的测速核心，为 NAS 场景增加 WebUI、Smart Repair、Full Optimize、Tracker 真实 announce、宿主机 Hosts 管理、运行历史和 GitHub 映射同步。

**当前正式版本：CFHost 1.0.0**。完整功能、默认行为、升级方法、支持边界及未验证事项见 [1.0 发布说明](docs/releases/1.0.0.md)，发布入口为 [CFHost 1.0 正式版](https://github.com/zyk1172/Qnap-CFST/releases/tag/cfhost-v1.0.0)。版本号属于 CFHost 服务层，上游 CloudflareSpeedTest 的自身版本号保持独立。

> **群晖和飞牛适配尚未在真实设备上运行过，也未实测其图形安装流程。** 本次确认的是 Linux amd64 Docker 环境中的配置、运行、Hosts 和持久化行为，以及 ARM64 二进制交叉编译；不代表所有 NAS 机型、DSM / fnOS 版本或 ARM64 设备都已验证。设备路径、权限、容器应用版本和重启后的系统 Hosts 行为需要按实际环境确认。

它解决的不是单纯“找一个延迟最低的 Cloudflare IP”，而是：

```text
CFST 找候选
   ↓
按 latency / bandwidth / normal / follow 策略处理
   ↓
逐域名验证实际可用性
   ↓
Tracker 可选真实 announce
   ↓
生成当前有效映射
   ↓
事务式写入 NAS /etc/hosts
   ↓
失效时只 Repair 该域名
   ↓
需要时手动 Full Optimize
```

## 主要功能

- **CFST 优选**：复用 CloudflareSpeedTest 的成熟测速核心。
- **Smart Repair**：优先验证当前 IP，失效后再尝试缓存候选，必要时才重新跑完整 CFST。
- **Full Optimize**：显式全局优化；手动执行时重新测速并允许全部域名重选 IP，周期执行默认关闭。
- **四类选择策略**
  - `latency`：验证域名后按 loss → delay → speed
  - `bandwidth`：验证域名后按 loss → speed → delay
  - `normal`：不做 HTTP/Tracker 有效性验证，直接取 CFST 候选中延迟最低的 IP
  - `follow`：不独立测速或验证，直接复用另一个已有非 follow 域名的当前映射 IP
- **严格 HTTP 验证**：支持重试、跳转、正文大小、Challenge / 占位页识别。
- **Tracker 真实 announce**：使用真实种子样本验证候选 IP 是否真正能用于 PT Tracker。
- **下载器自动取样本**：支持 Transmission 与 qBittorrent；每个 Tracker 域名只取 1 个已完成 v1 种子样本，手工样本仍具有最高优先级。
- **Transmission 实际 Tracker 健康反馈**：Repair 会读取样本种子的 `tracker_stats`；Transmission 实际报告 `Could not connect to tracker` / timeout 等连接错误时，即使 CFHost 自身 probe 能通，也会把当前映射视为失效并修复。
- **样本状态可见**：域名页直接显示 `未获取 / 已获取待测试 / 样本通过 / 样本失败`，并标明手工或下载器来源。
- **单域名维护**：每个域名都有独立“维护”按钮，只检查/修复该域名，并同步直接跟随它的域名；即使需要刷新 CFST，也不会重新选择其他独立域名的映射。
- **NAS Hosts 管理**
  - 只管理自己的 Marker
  - 保留非受管内容
  - 原地写入，保持 inode
  - 写前备份
  - 写后校验
  - 失败自动回滚
- **旧版本迁移**：可导入旧 CF-YX Marker 和 `legacy-hosts-map.tsv`。
- **GitHub 原子同步**：一次 commit 同时发布 `hosts-map.tsv` 和 `status.json`。
- **运行历史与日志**：保存最近任务、耗时、候选数量、映射变化和错误信息。
- **统一品牌资源**：浏览器标签页、启动画面、左侧栏与 README 共用项目图标，保持 WebUI 与项目主页视觉一致。
- **MoviePilot V3 风格 WebUI**
  - Dashboard
  - 域名管理
  - 候选 IP
  - 任务与历史
  - 实时日志
  - 设置
  - Light / Dark / Glass / 跟随系统
  - Cmd/Ctrl + K 命令面板
  - 响应式手机 / 平板布局

---

# 默认行为与使用边界

| 项目 | 1.0 行为 / 限制 |
| --- | --- |
| 自动运行 | 全局自动 Repair、自动应用 Hosts、周期 Full Optimize、GitHub 同步默认关闭。真实 Tracker announce 和自动发现样本默认开启，但下载器连接默认未启用；手动执行相关任务仍可能向站点发请求。已有 Transmission keepalive 观察任务可触发后台 Repair，不能把关闭自动 Repair 理解为暂停所有后台动作。 |
| 网站验证 | 默认使用 HTTPS，检查状态、正文与 Challenge / 占位页特征。小正文 API、登录页和站点规则可能误判；验证通过不等于登录、下载或所有路径都正常。跨域跳转的后续域名走自身解析，不代表原候选 IP 承载了全部跳转。 |
| 测速结果 | 延迟和吞吐来自当次网络与测速 URL，无法保证全天最快或具体业务下载速度。优选只适合允许通过候选 Cloudflare IP 访问的域名。 |
| `normal` / `follow` | `normal` 跳过域名验证；`follow` 只同步目标 IP、不验证自身可用性，且不支持自跟随或链式跟随。映射存在不等于该域名已验证成功。 |
| Tracker | 样本验证支持 HTTPS announce、40 位 v1 info-hash；不支持 UDP / 明文 HTTP Tracker 或纯 v2 样本。合法业务拒绝可表示候选可达，不能解决 passkey、账号、种子注册、站点风控等问题。 |
| 下载器 | Transmission 与 qBittorrent 支持自动取样本；主动 reannounce 保活和完整运行时健康反馈针对 Transmission。自动发现本身只读；保活可能对连接失败的种子发 reannounce，请按站点规则使用。 |
| Hosts 生效 | 默认只管理当前 NAS 的系统 Hosts。其他容器、局域网设备和使用独立 DNS / DoH 的应用不会自动继承；具体共享文件配置见 [部署说明](deploy/README.md#hosts-生效范围)。 |
| 系统恢复 | NAS 重启、升级或网络变更可能重写系统 Hosts，应用也可能缓存解析。CFHost 不安装系统启动钩子；需检查文件，必要时重建容器并重新应用。 |
| 平台范围 | Docker / Compose 下的 Linux amd64、arm64；不是群晖 SPK、QNAP QPKG 或飞牛原生应用安装包。32 位 ARM、无容器套件机型、旧 Compose 与所有 NAS 固件版本不保证兼容。 |
| 局域网与凭据 | 面向可信局域网，没有内置登录、多用户或 TLS 接入。密码输入框遮罩不等于加密存储；配置 / API / 备份及样本文件可能包含密码或 passkey。 |
| 数据与同步 | 配置与状态依赖 `/data` 持久化，不提供多实例协调。GitHub 同步只发布映射和状态，不会自动应用到远端设备；同步失败不撤销已成功的本地 Hosts 写入。 |
| 健康状态 | `/healthz` 返回服务状态和版本，仅说明 Web 服务可响应，不证明测速、目标站点、下载器或 Hosts 权限正常。 |

首次使用请先确认下载器实际如何解析域名，手动维护少量域名并检查结果，再按需开启自动应用和周期任务。更完整的参数、判定语义与未覆盖事项见 [1.0 发布说明](docs/releases/1.0.0.md)。

---

# 选择 NAS 部署方式

CFHost 使用 Linux 容器运行，预构建的 `cfhost` 镜像支持 `linux/amd64` 与 `linux/arm64`，Docker 会按设备架构自动选择。

| 设备 | 部署入口 | 数据目录默认值 |
| --- | --- | --- |
| 威联通 QNAP | [QNAP amd64 安装说明](deploy/qnap/README-amd64.md)；[多架构 Compose](deploy/qnap/compose.yaml) | `/share/Container/cfhost/data` |
| 群晖 Synology | [Container Manager 项目安装](deploy/synology/README.md) | `/volume1/docker/cfhost/data` |
| 飞牛 fnOS | [Docker Compose 安装](deploy/fnos/README.md) | 项目目录下的 `data` |
| 其他 Linux NAS | [通用 Compose 安装](deploy/generic/README.md) | 项目目录下的 `data` |

设备需要支持 Docker / 容器应用，具体群晖机型以套件中心是否提供 Container Manager 为准。32 位 ARM 设备暂不提供预构建镜像。

首次部署、下载器网络设置以及宿主机 / 容器 Hosts 的生效范围，请先看 [NAS 通用部署说明](deploy/README.md)。以下保留 QNAP amd64 的详细安装步骤。

---

# QNAP amd64 快速部署

适用于 Intel / AMD x86_64 QNAP NAS。

> Docker 平台名统一叫 `linux/amd64`。Intel x86_64 机型同样使用这个架构。

## 1. 镜像

推荐使用专用 QNAP amd64 镜像：

```text
ghcr.io/zyk1172/qnap-cfst:cfhost-amd64
```

镜像固定为：

```text
linux/amd64
```

如果 GHCR Package 已设为 **Public**，可以匿名拉取。

如果仍是 Private，需要先登录：

```bash
docker login ghcr.io
```

---

## 2. 准备持久化目录

推荐：

```text
/share/Container/cfhost/data
```

SSH 下可以先创建：

```bash
mkdir -p /share/Container/cfhost/data
```

这个目录会保存：

```text
config.json
state.json
hosts-backup-*
tracker-samples.tsv
tracker-samples.auto.tsv
github-token
legacy-hosts-map.tsv
```

重建或升级容器不会丢失配置和运行状态。

---

## 3. Container Station Compose

在 QNAP **Container Station → 应用程序 → 创建** 中粘贴：

```yaml
services:
  cfhost:
    image: ${CFHOST_IMAGE:-ghcr.io/zyk1172/qnap-cfst:cfhost-amd64}
    platform: linux/amd64
    container_name: cfhost
    hostname: cfhost
    restart: unless-stopped

    ports:
      - "${CFHOST_PORT:-9876}:8080"

    environment:
      TZ: ${TZ:-Asia/Shanghai}
      DATA_DIR: /data
      CFST_BIN: /app/cfst
      CFST_IP_FILE: /app/ip.txt
      CFST_IPV6_FILE: /app/ipv6.txt
      HOSTS_PATH: /host/etc/hosts

    volumes:
      - ${CFHOST_DATA_DIR:-/share/Container/cfhost/data}:/data
      - ${CFHOST_HOSTS_FILE:-/etc/hosts}:/host/etc/hosts:rw

    stop_grace_period: 15s
```

默认无需额外填写环境变量。

如果需要覆盖，可使用：

```env
CFHOST_IMAGE=ghcr.io/zyk1172/qnap-cfst:cfhost-amd64
CFHOST_PORT=9876
CFHOST_DATA_DIR=/share/Container/cfhost/data
CFHOST_HOSTS_FILE=/etc/hosts
TZ=Asia/Shanghai
```

仓库中也提供现成文件：

```text
deploy/qnap/compose-amd64.yaml
deploy/qnap/.env.amd64.example
deploy/qnap/README-amd64.md
```

---

## 4. 启动后访问

默认：

```text
http://QNAP-IP:9876
```

首次启动会自动创建：

```text
/share/Container/cfhost/data/config.json
/share/Container/cfhost/data/state.json
```

然后直接在 WebUI 中配置：

- 受管域名
- HTTP / Tracker 类型
- latency / bandwidth 策略
- CFST 延迟、丢包和速度阈值
- Smart Repair
- Full Optimize
- Hosts 自动应用
- Tracker 真实 announce
- Transmission / qBittorrent 自动发现测试种子
- GitHub 同步

---

# 为什么这样挂载 Hosts

Compose 使用：

```yaml
- /etc/hosts:/host/etc/hosts:rw
```

容器内：

```text
HOSTS_PATH=/host/etc/hosts
```

这里修改的是 **NAS 宿主机的真实 `/etc/hosts`**。

不要写成：

```yaml
- /etc/hosts:/etc/hosts
```

Docker 自己会管理容器内部的 `/etc/hosts`，直接覆盖它容易和 Docker 的 Hosts 机制冲突。

CFHost 会：

1. 校验现有 Marker。
2. 保留非受管 Hosts 内容。
3. 写入前备份。
4. 原地 truncate + write，保持 inode。
5. fsync 后重新读取验证。
6. 失败则自动恢复旧内容。

---

# Tracker 真实 announce

普通网站只需要 HTTP 验证；PT Tracker 可以启用真实 announce 验证。

CFHost 支持两种样本来源：

1. **手工样本**：`/data/tracker-samples.tsv`
2. **自动发现样本**：`/data/tracker-samples.auto.tsv`

手工样本优先级更高，不会被自动发现覆盖。

## 从 Transmission 自动发现

在 WebUI：

```text
设置 → Hosts 与 Tracker
```

填写 Transmission RPC 地址、用户名和密码即可。

例如同一台 NAS 上：

```text
http://192.168.1.10:9091/transmission/rpc
```

不要填 `127.0.0.1`，因为 CFHost 默认运行在 Docker bridge 网络中，容器里的 localhost 指向 CFHost 自己。

Transmission 自动发现：

- 兼容 Transmission 4.1+ JSON-RPC 2.0
- 兼容旧版 `torrent-get` RPC
- 使用正常的 `X-Transmission-Session-Id` 409 握手
- 先只读取轻量 torrent 元数据
- 只选已完成 / 正在做种的 40 位 v1 info-hash
- 分批读取 Tracker，避免一次拉取整个 PT 库的 tracker 数组
- **每个目标 Tracker 域名找到 1 个样本后就停止继续寻找该域名**
- Repair / 单域名维护会针对这个样本种子额外读取 `tracker_stats`，检查 Transmission 真实做种路径的最近 announce 状态
- `Could not connect to tracker`、announce timeout 等连接层错误会直接触发当前域名修复；普通 Tracker 业务拒绝不会误触发


### 在域名页查看样本状态

自动发现完成后，不需要再去查看 `tracker-samples.auto.tsv` 判断是否成功。域名管理表会直接显示：

```text
未获取
已获取 · 待测试
样本通过
样本失败
无需样本
```

其中：

- `已获取 · 待测试`：已经从 Transmission / qBittorrent 或手工文件取得样本，但还没有对当前候选 IP 做真实 announce。
- `样本通过`：已通过样本连接到 Tracker。一般 Tracker 业务错误也属于“候选可达”；但“Could not connect to tracker”这类上游连接失败会显示为样本失败。
- `样本失败`：网络/TLS/HTTP/Tracker 响应格式层面没有打通。
- `无需样本`：HTTP 域名、关闭真实 announce，或使用 `normal` 策略的 Tracker。

编辑 Tracker 域名时，同样会在表单底部显示该域名当前的样本状态。


## 从 qBittorrent 自动发现

填写 qBittorrent WebUI 地址、用户名和密码，例如：

```text
http://192.168.1.10:8080
```

CFHost 会读取 completed torrents，优先使用当前工作 Tracker；目标域名仍缺失时再按上限查询 torrent tracker 列表。

自动发现只读取 torrent hash、完成状态和 Tracker URL，不会：

- 暂停 / 恢复任务
- reannounce
- 修改 Tracker
- 删除任务
- 改变下载或做种状态

## 手工样本格式

如果需要手工维护：

```text
domain<TAB>announce_path<TAB>40位 info_hash<TAB>完整 HTTPS announce URL
```

默认位置（容器内）：

```text
/data/tracker-samples.tsv
```

NAS 上对应 `CFHOST_DATA_DIR/tracker-samples.tsv`；QNAP 默认示例为 `/share/Container/cfhost/data/tracker-samples.tsv`，群晖、飞牛按各自的数据挂载目录放置。

仓库提供：

```text
tracker-samples.example.tsv
```

Tracker 样本验证的“候选可达”要求：

- TCP 实际连接指定候选 Cloudflare IP
- TLS Host / SNI 仍使用 Tracker 域名
- HTTP 200
- 返回完整、合法的 bencode dictionary

只要已经收到 Tracker 的合法业务响应，就证明这个候选 IP 能到达 Tracker。因此：

- 正常 `interval / peers / peers6` 响应：样本通过。
- `Missing key peer_id`、站点反作弊、封禁、未注册种子等**业务错误**：仍记为样本通过 / candidate reachable，不会因此继续更换 Cloudflare IP。
- `Could not connect to tracker`、`Could not connect to track...`、`Failed/Unable to connect to tracker` 等明确表示**未连到 Tracker/上游**的错误：样本失败，并继续尝试其他候选 IP。
- TCP/TLS 失败、HTTP 非 200、HTML Challenge、空响应或非法/截断 bencode：样本失败。

默认：

```text
retries = 2
required successes = 1
```

---

# Smart Repair 与 Full Optimize

## Smart Repair

自动维护遵循：

> **哪个域名坏了，就只修哪个域名。**

例如：

```text
A 当前 IP 正常 → 保持 A 原 IP
B 当前 IP 失败 → 只给 B 找替代 IP
C 当前 IP 正常 → 保持 C 原 IP
```

B 的修复流程：

```text
Transmission 样本种子的真实 tracker_stats
  ↓ 明确连接失败时直接判当前 IP 失效
CFHost 当前 IP 主动验证
  ↓ 失败
有效期内候选缓存
  ↓ 全失败
达到 failure threshold + cooldown
  ↓
完整 CFST 刷新候选池

如果是 Transmission 新出现的明确连接错误（例如 Could not connect to tracker），缓存候选仍无法解决时会立即刷新一次 CFST，不等待 failure threshold。旧的 Transmission 错误会用 last_announce_time 与最近成功修复时间比较，避免刚换 IP 后被旧错误再次触发。
  ↓
只继续解决仍 unresolved 的域名
```

即使 B 触发了新的 CFST，A / C 也不会因此重新选择 IP。

### 手动单域名维护

域名管理表每一行都有“维护”按钮。

单域名维护遵循 Smart Repair 的低扰动原则：

```text
只检查选中的域名
  ↓
Tracker 缺样本时只为该域名尝试自动发现
  ↓
先验证当前 IP
  ↓
失败后尝试缓存候选
  ↓
仍失败则立即刷新一次 CFST
  ↓
修改这个域名的映射，并同步直接跟随它的域名
```

CFST 候选池属于全局测速结果，因此必要时会刷新候选池；但其他独立域名的当前 Hosts 映射不会因为这次手动维护而重新选择。直接跟随所选域名的 follow 映射会同步更新；目标无法解析时，也会移除对应的跟随映射。

默认：

- Repair 间隔：15 分钟
- candidate cache TTL：24 小时
- failure threshold：2
- Refresh 基础冷却：1 小时
- 最大退避：12 小时

## Full Optimize

Full Optimize 是明确的**全局重选**操作：

```text
强制 CFST
   ↓
按 latency / bandwidth 排序
   ↓
重新验证全部启用域名
   ↓
允许所有域名选择当前最优可用 IP
```

默认策略：

- **手动 Full Optimize：可随时执行**
- **周期性 Full Optimize：默认关闭**
- 只有主动打开 WebUI 中的“允许周期性全局优化”后，才按配置周期执行

旧版本的 `optimize.enabled=true` 不会自动迁移成周期全局换 IP。

---

# latency / bandwidth / normal / follow

## latency

适合 Tracker 和延迟敏感域名：

```text
loss → delay → speed → IP
```

## bandwidth

适合更关注下载吞吐的域名：

```text
loss → speed → delay → IP
```

默认 bandwidth 门槛：

```text
loss <= 0
delay <= 180 ms
speed >= 0.5 MB/s
```

## normal

普通策略不做域名级 HTTP 或 Tracker announce 验证。

它直接使用 CFST 已经完成测速和全局阈值筛选后的候选，并按照：

```text
delay → loss → speed → IP
```

选择延迟最低的 IP。

行为：

- HTTP 域名不发起 HTTP 有效性验证。
- Tracker 域名不要求测试种子，也不执行真实 announce。
- Smart Repair 有新鲜 CFST 候选时直接应用最低延迟 IP。
- 如果暂时没有新鲜候选但已有映射，先保留旧映射，不会因为“不验证”而删除 Hosts。
- Full Optimize 会重新运行 CFST，并直接为 normal 域名应用最低延迟候选。

每个需要验证的 latency / bandwidth 域名默认最多验证 Top 10 候选；normal 与 follow 都不做独立域名验证。

## follow

follow 是显式的“映射跟随”策略，适合多个域名必须始终使用同一个 Cloudflare IP 的场景。

行为：

- 在域名编辑器原“站点组”位置选择一个已有域名作为跟随目标。
- 只有选择 `follow` 策略时“跟随域名”下拉框才启用；latency / bandwidth / normal 下该字段会被忽略并在保存时清空。
- 跟随域名不会独立跑 CFST、HTTP 验证、Tracker announce、Transmission 保活或候选选择。
- Repair / Full Optimize 先处理被跟随的独立域名，再把其最终 IP 原样同步给 follow 域名。
- 被跟随目标停用或当前没有有效映射时，follow 域名也会进入未解析状态，并移除旧的跟随映射，避免保留错误 IP。
- 不允许自跟随，也不允许 follow → follow 链式依赖，避免循环和多级状态传播。
- 删除一个仍被其他域名跟随的目标会被 WebUI 阻止；重命名目标域名时，WebUI 会同步更新引用。

旧配置中的 `group` / 站点组字段不再参与任何映射选择，配置再次保存后会自然移除。

---

# GitHub 映射同步

CFHost 可以把当前映射发布到独立仓库：

```text
hosts-map.tsv
status.json
```

两个文件通过 Git Data API 在 **同一个 commit** 中原子更新。

`status.json` 使用 schema 4，并单独统计 `follow_count`。`hosts-map.tsv` 保留原有前十列，在末尾增加 `follow_target` 列；follow 行的策略列为 `follow`、状态为 `FOLLOWED`，不生成独立验证时间或 HTTP 状态码。normal 行仍标记为 `SELECTED`。

默认 Token 文件（容器内）：

```text
/data/github-token
```

NAS 上对应 `CFHOST_DATA_DIR/github-token`，QNAP 默认示例：

```text
/share/Container/cfhost/data/github-token
```

也可以使用运行环境中的 `GITHUB_TOKEN`。

---

# 更新与固定版本

正式版多架构镜像（amd64 / arm64）：

```text
ghcr.io/zyk1172/qnap-cfst:cfhost-v1.0.0
```

固定 amd64 镜像：

```text
ghcr.io/zyk1172/qnap-cfst:cfhost-amd64-v1.0.0
```

把部署 `.env` 中的 `CFHOST_IMAGE` 改为所需标签。默认 `cfhost`、`cfhost-amd64` 和兼容标签 `qnap-amd64` 会随主分支更新，不能用于锁定 1.0。

先备份数据目录，然后在项目目录运行：

```bash
sudo docker compose --env-file .env -f compose.yaml pull
sudo docker compose --env-file .env -f compose.yaml up -d
```

使用 QNAP 专用配置时把 `-f` 改为 `compose-amd64.yaml`；图形项目使用其实际保存的文件名，并重新创建 / 更新应用。仅执行 `docker pull` 不会替换正在运行的容器。

数据与状态保存在 `/data` 对应的 NAS 挂载目录。保留挂载目录并不会免除备份需求；不要删除包含数据的图形项目目录。恢复旧版本时还需要与旧版本匹配的数据备份，本项目不保证跨版本降级兼容。

---

# 常用路径与端口

| 项目 | 默认值 |
| --- | --- |
| WebUI | `http://NAS-IP:9876` |
| 容器监听端口 | `8080` |
| QNAP 数据目录 | `/share/Container/cfhost/data` |
| 容器数据目录 | `/data` |
| NAS 宿主机 Hosts | `/etc/hosts` |
| 容器 Hosts 挂载 | `/host/etc/hosts` |
| 手工 Tracker 样本 | `/data/tracker-samples.tsv` |
| 自动 Tracker 样本 | `/data/tracker-samples.auto.tsv` |
| GitHub Token | `/data/github-token` |
| IPv4 IP 池 | `/app/ip.txt` |
| IPv6 IP 池 | `/app/ipv6.txt` |

---

# 架构

上游 CFST 核心保持在：

```text
main.go
task/
utils/
```

CFHost 服务：

```text
cmd/cfhost/
```

WebUI 品牌资源：

```text
cmd/cfhost/web/assets/cfhost-logo.svg  # 浏览器与界面使用的矢量图标
cmd/cfhost/web/assets/cfhost-logo.png  # Apple touch icon 的透明 PNG 兼容资源
```

矢量图用于浏览器标签页、启动画面、左侧品牌区与本 README；PNG 仅作 Apple touch icon 兼容资源。

镜像中包含两个程序：

```text
/app/cfst
/app/cfhost
```

WebUI 由 Go `embed.FS` 直接打包，不需要 Vue / Node / Nginx 等额外运行时。

---

# 与 CloudflareSpeedTest 的关系

本项目保留 CloudflareSpeedTest 作为候选 IP 测速核心，并在其之上增加 NAS 场景的域名验证、Repair、Hosts、Tracker 和 WebUI 服务层。

上游项目：

[https://github.com/XIU2/CloudflareSpeedTest](https://github.com/XIU2/CloudflareSpeedTest)

---

# License

本项目继承上游 CloudflareSpeedTest，使用 GNU GPL v3。

MoviePilot-Frontend 的视觉语言被用于 WebUI 设计参考；相关 MIT License 说明见：

```text
THIRD_PARTY_NOTICES.md
```
