# CFHost — QNAP amd64 部署

这份部署文件专门用于 QNAP x86_64 NAS，例如 Intel / AMD 64 位机型。

> Docker 平台名称叫 `linux/amd64`。即使 NAS 使用 Intel CPU，也应选择这个架构；它不是“只能给 AMD CPU 使用”。

## 推荐文件

直接使用：

```text
deploy/qnap/compose-amd64.yaml
```

默认镜像：

```text
ghcr.io/zyk1172/qnap-cfst:cfhost-amd64
```

镜像只构建：

```text
linux/amd64
```

Compose 同时显式写了：

```yaml
platform: linux/amd64
```

避免 Container Station 选择错误架构。

## QNAP Container Station

在 Container Station 的“应用程序 / Compose”中建立项目，使用：

```yaml
services:
  cfhost:
    image: ghcr.io/zyk1172/qnap-cfst:cfhost-amd64
    platform: linux/amd64
    container_name: cfhost
    hostname: cfhost
    restart: unless-stopped
    ports:
      - "9876:8080"
    environment:
      TZ: Asia/Shanghai
      DATA_DIR: /data
      CFST_BIN: /app/cfst
      CFST_IP_FILE: /app/ip.txt
      CFST_IPV6_FILE: /app/ipv6.txt
      HOSTS_PATH: /host/etc/hosts
    volumes:
      - /share/Container/cfhost/data:/data
      - /etc/hosts:/host/etc/hosts:rw
    stop_grace_period: 15s
```

然后访问：

```text
http://QNAP-IP:9876
```

## 挂载说明

### /share/Container/cfhost/data:/data

这是 CFHost 的持久化目录。

其中会保存：

```text
config.json
state.json
hosts-backup-*
tracker-samples.tsv
github-token
legacy-hosts-map.tsv
```

默认建议：

```text
/share/Container/cfhost/data
```

不要把 `/data` 改成临时容器目录，否则重建容器后配置、状态、候选缓存和历史记录都会消失。

### /etc/hosts:/host/etc/hosts:rw

这里挂载的是 **QNAP 宿主机真正的 /etc/hosts**，不是容器自己的 `/etc/hosts`。

容器内部：

```text
HOSTS_PATH=/host/etc/hosts
```

CFHost 始终通过这个路径进行受管 Marker 写入。

不要改成：

```text
/etc/hosts:/etc/hosts
```

Docker 会自己管理容器的 `/etc/hosts`，直接覆盖容器路径容易和 Docker 的 hosts 管理机制冲突。

CFHost 对宿主机文件采用原地写入方式，保持 inode，不使用 rename 替换。

## 环境变量

QNAP Compose 中建议显式保留以下变量：

| 变量 | 值 | 用途 |
| --- | --- | --- |
| `TZ` | `Asia/Shanghai` | 日志与任务时间 |
| `DATA_DIR` | `/data` | 配置、状态、缓存、备份 |
| `CFST_BIN` | `/app/cfst` | 内置 CFST 二进制 |
| `CFST_IP_FILE` | `/app/ip.txt` | IPv4 候选池 |
| `CFST_IPV6_FILE` | `/app/ipv6.txt` | IPv6 候选池 |
| `HOSTS_PATH` | `/host/etc/hosts` | QNAP 宿主机 Hosts |

这些路径与 Dockerfile 内实际文件位置一致。

## Tracker 样本

启用 WebUI 中的“Tracker 真实 announce”后，应放置：

```text
/share/Container/cfhost/data/tracker-samples.tsv
```

容器内对应：

```text
/data/tracker-samples.tsv
```

不需要单独增加挂载，因为整个 `/data` 已经持久化。

## GitHub 同步 Token

如果启用 GitHub hosts-map 同步：

```text
/share/Container/cfhost/data/github-token
```

容器内对应：

```text
/data/github-token
```

也不需要额外挂载。

## 端口

默认：

```text
QNAP 9876 -> container 8080
```

如果 9876 已被占用，只修改 Compose 左侧：

```yaml
ports:
  - "新的端口:8080"
```

不要修改容器内部 8080，除非同时修改 CFHost 的监听配置。

## 网络模式

默认使用 Docker bridge + 端口映射，不使用 `network_mode: host`。

原因：

- WebUI 端口更容易控制；
- 不需要占用 QNAP 宿主机 8080；
- CFHost 只需要主动访问 Cloudflare / PT 域名；
- 修改宿主机 Hosts 依靠 bind mount，与 host network 无关。

如果以后要在容器内部测试 IPv6，则还需要确保 QNAP 的 Docker bridge 本身已经开启 IPv6；这与镜像架构无关。

## 更新镜像

重新拉取：

```bash
docker pull ghcr.io/zyk1172/qnap-cfst:cfhost-amd64
```

然后重新创建 Compose 应用即可。

`/data` 和宿主机 `/etc/hosts` 都是 bind mount，所以重建容器不会丢配置。
