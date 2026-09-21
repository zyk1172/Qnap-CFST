# CFHost

CFHost 是一个面向 NAS / QNAP 的 Cloudflare 优选与 Hosts 管理服务。

项目基于 [XIU2/CloudflareSpeedTest](https://github.com/XIU2/CloudflareSpeedTest) 的成熟测速核心，在其上增加 WebUI、Docker、域名级验证、智能 Repair 和宿主机 Hosts 管理。

> 当前开发版本为 v0.4。CFST 测速核心仍保持独立，QNAP / PT / Hosts 逻辑全部位于 `cmd/cfhost/`。

## v0.2 第二批

本批重点把旧 Shell 中真正需要的 Repair / Tracker 行为迁到 Go，但不照搬原来的大单体结构。

### current-IP-first Repair

Repair 不再每次先启动完整 CFST：

```text
当前映射
   ↓
逐域名验证
   ├─ 成功 → 直接保留
   └─ 失败
        ↓
   有效期内候选缓存
        ↓
   按候选顺序逐个验证
        ├─ 成功 → 立即停止
        └─ 全失败 → 记录 failure streak
                         ↓
                 达到阈值 + 冷却允许
                         ↓
                    完整 CFST
```

默认：

- Repair 每 15 分钟可执行
- candidate cache TTL：24 小时
- 连续失败 2 次后才允许完整测速
- 完整测速基础冷却：1 小时
- 持续失败退避：1h → 2h → 4h → 8h → 12h 上限

因此一个长期坏域名不会再每 15 分钟触发一次完整 Cloudflare 扫描。

首次安装且没有映射、没有候选缓存时仍会立即执行一次 CFST，不需要等待两轮失败。

### Tracker 真实 announce

开启 `Tracker 真实 announce` 后，Tracker 不再只看 HTTPS endpoint 是否能连接。

每个 Tracker 使用一条真实种子样本：

```text
domain<TAB>announce_path<TAB>40位 info_hash<TAB>完整 HTTPS announce URL
```

默认文件：

```text
/data/tracker-samples.tsv
```

仓库提供：

```text
tracker-samples.example.tsv
```

旧 QNAP Cloudflare Hosts Manager 的 `state/tracker-test-samples.tsv` 格式与新版本一致，可以直接迁移到 Docker 的 `data/tracker-samples.tsv`。

成功条件：

- HTTPS 请求确实通过指定候选 IP
- HTTP 200
- 返回 bencode dictionary
- 包含正常 announce 字段（`interval` 或 `peers`）
- 不包含 `failure reason`

探测方式已经改成按需顺序验证：

```text
当前 IP
  ↓失败
候选 1
  ↓失败
候选 2
  ↓成功
停止
```

不会像旧脚本那样预先对每个 Tracker 的全部 Top N IP 做真实 announce。

缺少 Tracker 样本属于配置问题，不会触发 CFST 重测速，因为换一批 Cloudflare IP 无法解决“没有样本”。

### 候选排序

CFHost 对 CFST 候选再次按 PT latency 策略排序：

1. 丢包率低
2. 延迟低
3. 下载速度高
4. IP

同一站点组仍优先尝试已验证的共享 IP，但每个域名都独立验证，失败就继续下一个候选。

### Hosts

Repair 最终只保留本轮验证成功的映射。

如果某个域名当前 IP 已确认失效、缓存和新候选都无法替换，它不会继续把坏 IP 写回 Hosts。

如果全部映射失效，CFHost 会清除自己的 Marker 区域，让域名恢复正常 DNS；不会恢复已经确认失败的旧映射。

## Docker Compose

```yaml
services:
  cfhost:
    build: .
    container_name: cfhost
    restart: unless-stopped
    ports:
      - "9876:8080"
    environment:
      TZ: Asia/Shanghai
      DATA_DIR: /data
      HOSTS_PATH: /host/etc/hosts
    volumes:
      - ./data:/data
      - /etc/hosts:/host/etc/hosts
```

启动：

```bash
docker compose up -d --build
```

打开：

```text
http://NAS-IP:9876
```

首次启动会自动生成 `data/config.json`。

如果要启用 PT Tracker 真实 announce，把旧项目的样本复制过去：

```bash
cp /旧项目/state/tracker-test-samples.tsv ./data/tracker-samples.tsv
```

也可以按照 `tracker-samples.example.tsv` 自己创建。

## WebUI

当前可以直接配置：

- CFST 延迟上限
- 最大丢包率
- 最低下载速度
- 下载测速数量
- IPv4 / IPv6
- Repair 间隔
- candidate cache TTL
- failure streak 阈值
- refresh cooldown
- 最大 backoff
- Tracker real announce
- Tracker 样本路径
- announce 重试次数
- 域名 / 分组 / HTTP / Tracker 类型
- 自动 Repair
- Repair 后自动写 Hosts

状态页显示：

- 当前映射
- 每个域名连续失败次数
- 候选 IP 与采样时间
- 最近一次完整 CFST
- 下次允许完整 CFST 的时间
- Repair / Tracker 日志

## 与上游 CFST 的关系

上游测速核心仍保留在：

```text
main.go
task/
utils/
```

CFHost 服务代码：

```text
cmd/cfhost/
```

Docker 镜像构建两个程序：

```text
/app/cfst
/app/cfhost
```

第二批仍没有修改上游 CFST 的测速实现。

## 尚未迁入

下一批适合独立处理：

1. GitHub hosts-map/status 同步
2. 同步格式收敛为单次原子发布
3. 更完整的运行历史/统计
4. QNAP 实机部署后的兼容性修正

## v0.3 第三批

### GitHub 原子同步

CFHost 可以继续发布兼容现有 macOS / Windows 客户端的 10 列 `hosts-map.tsv`，并同时生成 `status.json`。

与旧 Shell 不同，v0.3 不再对两个文件分别调用 Contents API。现在使用 GitHub Git Data API：

```text
hosts-map.tsv ─→ blob ┐
status.json    ─→ blob ├→ one tree → one commit → update branch ref
                       ┘
```

因此客户端不会再看到“map 已更新但 status 还是旧版本”或相反的瞬时状态。

只有映射、策略类别或候选指标发生变化时才自动发布；普通 Repair 仅重新确认同一映射不会制造新的 Git commit。WebUI 的“立即同步”可以强制发布一次。

默认同步目标：

```text
zyk1172/cloudflare-hosts-sync
branch: main
token file: /data/github-token
```

也支持容器环境变量 `GITHUB_TOKEN`。

### siteGroup 与 class

v0.3 明确区分：

- `group`：同一 PT 站点的共享组，例如 `mteam`、`ptcafe`
- `class`：同步/测速策略类别，仅为 `latency` 或 `bandwidth`

旧配置没有 `class` 时自动迁移为 `latency`，因此不会破坏 v0.2 配置。

### 运行历史

`state.json` 保留最近 200 次任务记录，包括：

- run / repair 类型
- 开始和结束时间
- 耗时
- 成功 / 失败
- 是否触发完整 CFST
- 映射数量变化
- 候选 IP 数量

WebUI 直接显示最近运行历史与 GitHub 同步状态。

### QNAP / Container Station

主 Compose 支持通过环境变量覆盖：

```text
CFHOST_PORT
CFHOST_DATA_DIR
CFHOST_HOSTS_FILE
```

例如 QNAP：

```bash
CFHOST_DATA_DIR=/share/Container/cfhost/data docker compose up -d
```

容器增加 `/healthz`、Docker healthcheck、SIGTERM graceful shutdown 和 15 秒停止宽限期。Hosts 仍通过单文件 bind mount 原地写入，保持宿主机文件 inode。

仓库同时提供 `deploy/qnap/compose.yaml`。打 `cfhost-v*` tag 后，镜像工作流可以发布 amd64/arm64 镜像到 GHCR。

## License

本项目继承上游 CloudflareSpeedTest，使用 GNU GPL v3。


## v0.4 第四批

### 严格 HTTP 验证

普通网站默认恢复严格验证，不再把“能收到任意 HTTP 响应”直接视为成功：

- 指定候选 IP 连接原域名
- 最多重试 2 次
- 跟随最多 3 次跳转
- 跳转到其它域名后恢复正常 DNS，不错误地继续绑定候选 IP
- 最终必须是 2xx
- HTTP 200 正文默认至少 512 bytes
- 拒绝 Cloudflare challenge、nginx/宝塔默认页、parked domain 等常见占位内容

这些参数都可以在 WebUI 修改。

### Hosts 事务与迁移

Hosts 写入现在会：

1. 校验 CFHost 和旧 CF-YX Marker 是否完整、唯一且不嵌套。
2. 生成新 Hosts 后确认非受管内容没有变化。
3. 先备份，再原地写入并重新读取校验，继续保持 inode。
4. 如果写入失败，立即恢复原内容。
5. Repair/Optimize 写 Hosts 后如果 state.json 提交失败，再把 Hosts 回滚到旧版本。

备份默认只保留最近 10 份。

首次启动且 CFHost state 为空时，会自动从宿主机现有 CFHost/CF-YX Marker 导入映射；也会读取可选的 `/data/legacy-hosts-map.tsv`。导入映射只作为“待验证 current IP”，下一次 Repair 会重新验证。首次成功应用新 Hosts 后，旧的：

```text
# CF-YX-LATENCY-BEGIN
# CF-YX-LATENCY-END
# CF-YX-BANDWIDTH-BEGIN
# CF-YX-BANDWIDTH-END
```

会被移除并收敛到一个 CFHost Marker。

### 真正的 latency / bandwidth 选择策略

每个域名的 `class` 现在实际参与候选选择：

```text
latency:
loss -> delay -> speed -> IP

bandwidth:
loss -> speed -> delay -> IP
```

bandwidth 还会额外应用默认阈值：

- loss <= 0
- delay <= 180 ms
- speed >= 0.5 MB/s

每个域名默认最多验证 Top 10 个候选。

CFST 本身仍负责生成带真实下载速度的候选；CFHost 不修改上游测速核心。

### 逐域名 Repair 与 Full Optimize

自动维护默认采用“哪个域名坏了就修哪个”的策略：

```text
Smart Repair:
A 域名当前 IP 可用 -> A 保持原 IP
B 域名当前 IP 失败 -> 只给 B 尝试共享 IP / 缓存候选 / 必要时 CFST
C 域名当前 IP 可用 -> C 保持原 IP
```

即使某个失效域名最终触发了完整 CFST，新的候选池也只用于解决当前 unresolved 域名；本轮验证正常的域名不会因为它而重新选 IP。

`Full Optimize` 是不同的显式全局操作：

```text
手动 Full Optimize:
强制 CFST -> 按最新 latency/bandwidth 策略重新排序
-> 每个域名重新验证 -> 允许所有域名重新选择当前最优 IP
```

周期性全局优化现在默认关闭。旧版本中的 `optimize.enabled=true` 不会自动迁移成周期全局换 IP，避免升级后继续改变健康域名。

如果确实需要定期全局重选，可在 WebUI 中主动开启“允许周期性全局优化”；手动点击“完整优化”始终保留。

### Tracker

旧生产配置实际为：

```text
TRACKER_REAL_ANNOUNCE_RETRIES=2
TRACKER_REAL_ANNOUNCE_REQUIRED_SUCCESSES=1
```

所以 v0.4 保持“一次有效真实 announce 即证明候选可用”，没有错误地改成必须连续成功两次。

## QNAP amd64 image

A dedicated QNAP/x86_64 image is published as:

```text
ghcr.io/zyk1172/qnap-cfst:cfhost-amd64
```

The image manifest contains only `linux/amd64`. The QNAP compose file pins the same platform explicitly and bind-mounts the NAS host `/etc/hosts` at `/host/etc/hosts`.
