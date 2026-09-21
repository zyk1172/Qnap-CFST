# CFHost

CFHost 是一个面向 NAS / QNAP 的 Cloudflare 优选与 Hosts 管理服务。

项目基于 [XIU2/CloudflareSpeedTest](https://github.com/XIU2/CloudflareSpeedTest) 的成熟测速核心，在其上增加 WebUI、Docker、域名级验证、分组复用、自动 Repair 和宿主机 Hosts 管理。

> 当前为 v0.1 foundation。测速核心保持与上游 CFST 分离，CFHost 通过独立服务调用 CFST 二进制，便于以后继续同步上游。

## v0.1 已实现

- 原版 CFST IPv4 / IPv6 测速核心
- Docker 单容器部署
- WebUI 配置
- 延迟、丢包率、最低下载速度、测速数量等参数
- 域名增删改
- HTTP / Tracker 两种域名类型
- PT 站点分组：同组优先复用同一个已验证 IP
- 对候选 IP 按具体域名执行 HTTPS/SNI 链路验证
- 当前映射、候选 IP、日志查看
- 手动测速
- 测速并解析
- 自动 Repair 调度
- 可选 Repair 后自动写 Hosts
- Hosts Marker 局部管理
- 写宿主机 Hosts 时原地写入，保持 inode
- Hosts 修改前备份到 data 目录
- 配置与运行状态持久化
- Go test + Docker build CI

### Tracker 说明

v0.1 的 Tracker 类型先验证 Tracker endpoint 的 TCP/TLS/SNI/HTTP 链路是否可达，尚未迁入旧项目的真实 announce 验证。真实 announce、失败退避和更完整的 repair 状态机会作为下一阶段迁移内容。

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

首次启动会自动生成 `data/config.json`，默认带入现有 PT 域名，也可以全部在 WebUI 中编辑。

## 工作流

```text
CFST
  ↓
候选 IP
  ↓
域名级验证
  ↓
站点分组复用
  ↓
映射结果
  ↓
/host/etc/hosts
```

CFST 只负责回答“哪些 Cloudflare IP 网络质量更好”。

CFHost 再回答“这个 IP 对这个具体域名是否真的能建立 HTTPS 链路”，之后才进入 Hosts 映射。

## 与上游的关系

上游测速核心仍保留在根目录 `main.go`、`task/`、`utils/` 中。

CFHost 自己的服务代码位于：

```text
cmd/cfhost/
```

Docker 镜像中会生成两个二进制：

```text
/app/cfst
/app/cfhost
```

这种结构刻意避免把 QNAP / PT / Hosts 逻辑塞回 CFST 测速核心。

## 后续

计划从旧 QNAP Cloudflare Hosts Manager 逐项迁入：

1. Tracker 真实 announce 验证
2. current-IP-first repair
3. candidate cache / TTL
4. failure streak + refresh cooldown/backoff
5. GitHub hosts-map 同步
6. 更完整的状态与历史记录

## License

本项目继承上游 CloudflareSpeedTest，使用 GNU GPL v3。
