# CFHost

**面向 QNAP / NAS 的 Cloudflare 优选、域名验证与 Hosts 自动管理服务。**

CFHost 基于 [XIU2/CloudflareSpeedTest](https://github.com/XIU2/CloudflareSpeedTest) 的测速核心，为 NAS 场景增加 WebUI、Smart Repair、Full Optimize、Tracker 真实 announce、宿主机 Hosts 管理、运行历史和 GitHub 映射同步。

它解决的不是单纯“找一个延迟最低的 Cloudflare IP”，而是：

```text
CFST 找候选
   ↓
按 latency / bandwidth / normal 策略排序
   ↓
逐域名验证实际可用性
   ↓
Tracker 可选真实 announce
   ↓
生成当前有效映射
   ↓
事务式写入 QNAP /etc/hosts
   ↓
失效时只 Repair 该域名
   ↓
需要时手动 Full Optimize
```

## 主要功能

- **CFST 优选**：复用 CloudflareSpeedTest 的成熟测速核心。
- **Smart Repair**：优先验证当前 IP，失效后再尝试缓存候选，必要时才重新跑完整 CFST。
- **Full Optimize**：显式全局优化；手动执行时重新测速并允许全部域名重选 IP，周期执行默认关闭。
- **三类选择策略**
  - `latency`：验证域名后按 loss → delay → speed
  - `bandwidth`：验证域名后按 loss → speed → delay
  - `normal`：不做 HTTP/Tracker 有效性验证，直接取 CFST 候选中延迟最低的 IP
- **严格 HTTP 验证**：支持重试、跳转、正文大小、Challenge / 占位页识别。
- **Tracker 真实 announce**：使用真实种子样本验证候选 IP 是否真正能用于 PT Tracker。
- **下载器自动取样本**：支持 Transmission 与 qBittorrent；每个 Tracker 域名只取 1 个已完成 v1 种子样本，手工样本仍具有最高优先级。
- **QNAP Hosts 管理**
  - 只管理自己的 Marker
  - 保留非受管内容
  - 原地写入，保持 inode
  - 写前备份
  - 写后校验
  - 失败自动回滚
- **旧版本迁移**：可导入旧 CF-YX Marker 和 `legacy-hosts-map.tsv`。
- **GitHub 原子同步**：一次 commit 同时发布 `hosts-map.tsv` 和 `status.json`。
- **运行历史与日志**：保存最近任务、耗时、候选数量、映射变化和错误信息。
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

这里修改的是 **QNAP 宿主机的真实 `/etc/hosts`**。

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

例如同一台 QNAP 上：

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

默认位置：

```text
/share/Container/cfhost/data/tracker-samples.tsv
```

仓库提供：

```text
tracker-samples.example.tsv
```

真实 announce 成功要求：

- TCP 实际连接指定候选 Cloudflare IP
- TLS Host / SNI 仍使用 Tracker 域名
- HTTP 200
- 返回合法 bencode dictionary
- 包含 `interval` 或 `peers`
- 不包含 `failure reason`

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
当前 IP
  ↓ 失败
同站点组已验证 IP
  ↓ 不可用
有效期内候选缓存
  ↓ 全失败
达到 failure threshold + cooldown
  ↓
完整 CFST 刷新候选池
  ↓
只继续解决仍 unresolved 的域名
```

即使 B 触发了新的 CFST，A / C 也不会因此重新选择 IP。

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

# latency / bandwidth / normal

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
- 不使用站点组共享 IP 来覆盖最低延迟排序。
- Smart Repair 有新鲜 CFST 候选时直接应用最低延迟 IP。
- 如果暂时没有新鲜候选但已有映射，先保留旧映射，不会因为“不验证”而删除 Hosts。
- Full Optimize 会重新运行 CFST，并直接为 normal 域名应用最低延迟候选。

每个需要验证的 latency / bandwidth 域名默认最多验证 Top 10 候选；normal 不做这一步。

---

# GitHub 映射同步

CFHost 可以把当前映射发布到独立仓库：

```text
hosts-map.tsv
status.json
```

两个文件通过 Git Data API 在 **同一个 commit** 中原子更新。

默认 Token 文件：

```text
/share/Container/cfhost/data/github-token
```

容器内：

```text
/data/github-token
```

也可以使用运行环境中的 `GITHUB_TOKEN`。

---

# 更新

拉取最新版：

```bash
docker pull ghcr.io/zyk1172/qnap-cfst:cfhost-amd64
```

如果使用 Container Station Compose，重新创建 / 更新应用即可。

因为以下内容都在 bind mount 中：

```text
/data
/host/etc/hosts
```

升级镜像不会删除 CFHost 配置和状态。

---

# 常用路径与端口

| 项目 | 默认值 |
| --- | --- |
| WebUI | `http://QNAP-IP:9876` |
| 容器监听端口 | `8080` |
| QNAP 数据目录 | `/share/Container/cfhost/data` |
| 容器数据目录 | `/data` |
| QNAP Hosts | `/etc/hosts` |
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

镜像中包含两个程序：

```text
/app/cfst
/app/cfhost
```

WebUI 由 Go `embed.FS` 直接打包，不需要 Vue / Node / Nginx 等额外运行时。

---

# 与 CloudflareSpeedTest 的关系

本项目保留 CloudflareSpeedTest 作为候选 IP 测速核心，并在其之上增加 NAS / QNAP 场景的域名验证、Repair、Hosts、Tracker 和 WebUI 服务层。

上游项目：

[https://github.com/XIU2/CloudflareSpeedTest](https://github.com/XIU2/CloudflareSpeedTest)

---

# License

本项目继承上游 CloudflareSpeedTest，使用 GNU GPL v3。

MoviePilot-Frontend 的视觉语言被用于 WebUI 设计参考；相关 MIT License 说明见：

```text
THIRD_PARTY_NOTICES.md
```
