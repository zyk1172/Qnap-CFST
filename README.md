# CFHost

CFHost 是一个面向 NAS / QNAP 的 Cloudflare 优选与 Hosts 管理服务。

项目基于 [XIU2/CloudflareSpeedTest](https://github.com/XIU2/CloudflareSpeedTest) 的成熟测速核心，在其上增加 WebUI、Docker、域名级验证、智能 Repair 和宿主机 Hosts 管理。

> 当前开发版本为 v0.2。CFST 测速核心仍保持独立，QNAP / PT / Hosts 逻辑全部位于 `cmd/cfhost/`。

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

## License

本项目继承上游 CloudflareSpeedTest，使用 GNU GPL v3。
