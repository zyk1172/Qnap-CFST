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

固定使用 CFHost 1.0.2 时，把 `.env` 的 `CFHOST_IMAGE` 设为 `ghcr.io/zyk1172/qnap-cfst:cfhost-amd64-v1.0.2`。默认 `cfhost-amd64` 是随主分支更新的标签。升级要点见 [1.0.2 发布说明](../../docs/releases/1.0.2.md)，其他 NAS 的部署与 Hosts 生效范围见 [通用说明](../README.md)。群晖、飞牛适配尚未在真实设备上运行验证。

## QNAP Container Station

在 Container Station 的“应用程序 / Compose”中建立项目，使用 [compose-amd64.yaml](compose-amd64.yaml) 的完整内容。

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

Compose 已内置默认值，即使 Container Station 不加载 `.env` 也可以直接创建应用。需要自定义时，可复制 `.env.amd64.example` 为 `.env`，或直接在 Container Station 项目环境变量中设置：

```text
CFHOST_IMAGE=ghcr.io/zyk1172/qnap-cfst:cfhost-amd64
CFHOST_PORT=9876
CFHOST_DATA_DIR=/share/Container/cfhost/data
CFHOST_HOSTS_DIR=/etc
CFHOST_DOCKER_TARGETS=0
TZ=Asia/Shanghai
```

不要把 `/data` 改成临时容器目录，否则重建容器后配置、状态、候选缓存和历史记录都会消失。

### /etc:/host/etc:rw

这里挂载的是 **QNAP 宿主机 Hosts 父目录**，不是容器自己的 `/etc`。同时将同一父目录只读挂载至 `/host/etc-check` 供校验。

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

## Hosts 重启恢复与容器目标

- QNAP Compose 使用宿主机 Hosts 父目录读写挂载，独立只读校验路径保持不变。这样系统替换 Hosts 文件后可以按当前路径读取新文件。**整个宿主机 /etc 可被 CFHost 写入，权限扩大，必须信任镜像和 WebUI 访问者。** 旧单文件部署仍可用，但文件替换时仍可能需要重建 CFHost。
- 开启自动应用后，已成功应用的受管映射随 state.json 持久化，并镜像到 /data/hosts-last-applied.json；宿主机完整旧文件仍按原机制保存 hosts-backup-*。启动及每 30 秒检查当前 Hosts，恢复受管区块，不用旧的整份 Hosts 覆盖 NAS 系统记录，不重新测速或请求 Tracker。原地写入当前 inode，系统自身替换文件不受 CFHost 控制。
- 首次升级应在宿主机仍有与 state.json 一致的映射时启动，或手动应用一次以建立恢复备份；手动清空、关闭自动应用后不会自动恢复旧映射。
- 容器目标功能默认关闭。取消 Compose 中 Docker socket 挂载的注释，设置 CFHOST_DOCKER_TARGETS=1 并重建 CFHost（保留 /data）。**Docker socket 提供极高的宿主机权限，CFHost 没有内置认证，绝对不要公开 WebUI 或允许不可信用户访问。**
- 在 WebUI 设置 → Hosts 与 Tracker → 容器 Hosts 管理中勾选目标并保存。目标须使用 Docker 独立 Hosts，支持 root exec、sh、cat、cmp、stat、base64、mktemp；已显式挂载 Hosts 或其父目录的容器拒绝修改。无需挂载宿主机 Hosts；旧挂载需要先在目标容器配置中移除并按原配置重建一次。
- 成功提交映射后即时尝试同步所选容器；周期检查负责启动、重启、重建及漂移恢复。每个容器独立报告结果，失败不撤销宿主机已提交结果。长任务期间检查延后。只有明确变化才写入；不重启目标容器。应用 DNS 缓存和既有连接不保证即时切换。
- 取消勾选停止管理，保留既有受管区块；如需删除，请先清空（同时会清除宿主机及其他已选目标并关闭自动应用）。停止的已选容器清空将在其启动后重试。

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


## Tracker 测试种子自动发现

CFHost 可以直接从 Transmission 或 qBittorrent 读取一个已完成种子，自动生成 Tracker 真实 announce 所需的测试样本。

不需要再手工维护每个 Tracker 的 `info_hash` 和完整 announce URL。

WebUI 路径：

```text
设置 → Hosts 与 Tracker
```

可以分别配置：

- Transmission RPC 地址、用户名、密码
- qBittorrent WebUI 地址、用户名、密码
- 自动发现开关
- 自动样本缓存路径
- qB Tracker 补查上限

默认自动样本文件：

```text
/data/tracker-samples.auto.tsv
```

宿主机对应：

```text
/share/Container/cfhost/data/tracker-samples.auto.tsv
```

自动发现只读取下载器信息：

- Transmission：`torrent-get`
- qBittorrent：`torrents/info` 和必要时的 `torrents/trackers`

不会暂停、启动、删除、reannounce 或修改下载器任务。

CFHost 只使用 **已完成种子**，并从中选择与受管 Tracker 域名匹配的 HTTPS announce URL 和 40 位 v1 info-hash。

手工文件：

```text
/data/tracker-samples.tsv
```

仍然保留，并且优先级高于自动发现结果。

### 同一台 QNAP 上的下载器地址

CFHost 默认运行在 Docker bridge 网络，因此：

```text
127.0.0.1
```

指向的是 CFHost 容器自己，不是 QNAP 宿主机。

如果 Transmission / qBittorrent 也运行在同一台 QNAP 上，通常应填写 QNAP 的局域网地址，例如：

```text
Transmission:
http://192.168.1.10:9091/transmission/rpc

qBittorrent:
http://192.168.1.10:8080
```

端口按实际 WebUI / RPC 端口填写。

此功能不需要新增 Docker volume，也不要求 `network_mode: host`。
