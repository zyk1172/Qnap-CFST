# NAS 通用部署说明

> **验证范围：尚未在真实群晖或飞牛设备上运行过 CFHost，也未实测其图形安装界面。** 已在 Linux amd64 Docker 环境校验部署配置、容器运行、Hosts 写入和数据持久化；ARM64 二进制已交叉编译。多架构镜像构建与 NAS 实机验证是不同层级的检查，具体机型、系统版本、权限与重启后的行为仍需使用者确认。

CFHost 的程序、WebUI、配置格式和数据文件在各 NAS 上通用。平台适配提供不同的安装入口和数据路径，无需修改测速、域名验证或 Hosts 管理逻辑。

## 选择平台与镜像

| 平台 | 安装说明 | Compose |
| --- | --- | --- |
| 威联通 QNAP x86_64 | [Container Station](qnap/README-amd64.md) | [专用 amd64](qnap/compose-amd64.yaml) |
| 威联通 QNAP amd64 / arm64 | 数据目录默认 `/share/Container/cfhost/data`，按实际共享文件夹调整 | [多架构](qnap/compose.yaml) |
| 群晖 Synology | [Container Manager](synology/README.md) | [群晖配置](synology/compose.yaml) |
| 飞牛 fnOS | [Docker 应用 / SSH](fnos/README.md) | [飞牛配置](fnos/compose.yaml) |
| 其他 Linux NAS | [Docker Compose](generic/README.md) | [通用配置](generic/compose.yaml) |

多架构镜像为 `ghcr.io/zyk1172/qnap-cfst:cfhost`，包含 `linux/amd64` 与 `linux/arm64`。名称保留 `qnap-cfst`，群晖、飞牛同样使用这个镜像。无需指定 `platform`；Docker 自动匹配宿主机架构。主分支合并后由 GitHub Actions 更新镜像。

正式版 **1.0.0** 的固定多架构镜像为 `ghcr.io/zyk1172/qnap-cfst:cfhost-v1.0.0`，固定 amd64 镜像为 `ghcr.io/zyk1172/qnap-cfst:cfhost-amd64-v1.0.0`。将 `CFHOST_IMAGE` 改成固定版本可避免跟随主分支更新；默认 `cfhost` / `cfhost-amd64` 标签持续更新。镜像以发布工作流成功推送后的结果为准。如果拉取提示 `not found`，检查该工作流及 GHCR Package 的访问权限。也可在 NAS 上检出本项目源码，在仓库根目录执行 `sudo docker build -t cfhost:local .`，再将部署 `.env` 中的 `CFHOST_IMAGE` 设为 `cfhost:local`。

SSH 下可用 `uname -m` 检查：`x86_64` 对应 `amd64`，`aarch64` / `arm64` 对应 `arm64`。没有 Docker 套件的群晖机型、32 位 `armv7l` 和非 Linux 容器环境不在当前适配范围。

新平台配置使用 Compose 的长格式绑定挂载。Hosts 源文件必须存在，`create_host_path: false` 会阻止路径拼错时自动创建目录。如果容器应用报该字段不支持，请更新容器应用或使用支持此字段的 Docker Compose v2。

## 路径、端口和权限

| 参数 | 含义 | 默认值 |
| --- | --- | --- |
| `CFHOST_IMAGE` | 镜像名，可改为固定版本标签 | `ghcr.io/zyk1172/qnap-cfst:cfhost` |
| `CFHOST_PORT` | NAS 上的 WebUI 端口 | `9876` |
| `CFHOST_DATA_DIR` | NAS 上持久化配置、状态、历史和备份的目录 | 见各平台配置 |
| `CFHOST_HOSTS_FILE` | NAS 上待管理的 Hosts 文件 | `/etc/hosts` |
| `TZ` | 时区 | `Asia/Shanghai` |

复制平台目录的 `.env.example` 为 `.env` 后修改。CLI 命令均显式传入 `--env-file .env`。图形界面上传 YAML 时，还需把 `.env` 放在项目工作目录；若界面不读取 `.env`，直接把 YAML 中的 `${变量:-默认值}` 替换为实际值。

`./data` 相对于 Compose 文件所在目录解析。图形界面部署建议填 NAS 的绝对路径，避免应用重新保存项目时改变数据位置。群晖默认 `/volume1/docker/cfhost/data` 只是示例，使用其他存储卷或共享文件夹时务必调整。

容器应用需要能够读写数据目录及选定的 Hosts 文件。镜像默认以 root 运行，以便管理宿主机 Hosts；无需开启 privileged 或 host 网络。若有写入权限错误，请检查共享文件夹 ACL、挂载是否只读以及容器应用的访问限制。NAS 防火墙需允许局域网访问所选 WebUI 端口。

## Hosts 生效范围

默认把 NAS 的 `/etc/hosts` 绑定到容器的 `/host/etc/hosts`，WebUI 中的 Hosts 路径保持 `/host/etc/hosts`。CFHost 写前备份原文件，原地更新并保留非受管内容，因此改动作用于 NAS 本机通过系统解析器进行的域名解析。

**同一台 NAS 上的其他 Docker 容器不会自动继承宿主机 `/etc/hosts`。** 下载器运行在容器中时，需要单独配置其解析方式。修改下载器 RPC 地址只能让 CFHost 连接下载器，并不会改变下载器的 DNS。

需要给容器使用共享 Hosts 文件时，可采用以下方式：

1. 在 NAS 上建立独立文件，例如在项目目录执行 `cp /etc/hosts ./downloader-hosts`，保留基础记录。
2. 将 `CFHOST_HOSTS_FILE` 改为该文件的绝对路径，CFHost 内部路径仍为 `/host/etc/hosts`。
3. 在下载器的 Compose 中把同一文件只读挂载到 `/etc/hosts`，重新创建下载器容器。先确认下载器镜像允许这样配置，并保留它需要的 localhost、主机名等记录；此方式会接管下载器容器的 Hosts 文件。
4. CFHost 原地写入可保持该绑定文件的更新可见。若手动替换文件改变了 inode，需要重新创建两个容器。使用自带 DNS / DoH 的应用还需关闭绕过系统解析的设置。

新安装默认关闭自动 Repair 和自动应用。先保存域名、执行一次维护 / Full Optimize，确认映射，再手动应用 Hosts；按需开启自动化。恢复已有数据目录会沿用原有开关。

NAS 重启、升级或网络设置变化可能重新生成系统 Hosts。检查受管区块是否仍存在，必要时在 WebUI 再应用一次；若原文件被替换，先重新创建 CFHost 容器以刷新绑定挂载。不要用定时任务整份覆盖 `/etc/hosts`。

## 下载器设置

Transmission / qBittorrent 的 RPC URL 通常使用 `http://NAS局域网IP:已发布端口/...`。bridge 网络中的 `127.0.0.1` 指向 CFHost 容器自身。若通过容器服务名连接下载器，需让两个容器加入同一个 Docker 网络；各自独立项目的默认网络不能直接互通服务名。

按下载器要求配置 RPC 认证与访问白名单。密码输入框会遮罩显示。自动样本发现只读取已完成任务的 hash 和 Tracker URL；真实 announce 与 keepalive 按相应设置执行。

## 检查、更新与迁移

在保存 Compose 和 `.env` 的目录执行：

```bash
sudo docker compose --env-file .env -f compose.yaml config
sudo docker compose --env-file .env -f compose.yaml up -d
sudo docker compose --env-file .env -f compose.yaml ps
sudo docker compose --env-file .env -f compose.yaml logs --tail=100 cfhost
```

访问 `http://NAS局域网IP:9876`，健康检查地址是 `/healthz`。看到页面后，确认 `/data/config.json` 对应的文件出现在选定的数据目录；重新创建容器后配置应保留。

更新时先备份数据目录，然后：

```bash
sudo docker compose --env-file .env -f compose.yaml pull
sudo docker compose --env-file .env -f compose.yaml up -d
```

不要删除数据目录。群晖在删除项目时可能一并删除项目目录，备份应放在项目目录外。跨 NAS 迁移时，停止旧实例后复制整个数据目录，修改新设备的挂载源路径；配置里的 Hosts 路径仍使用 `/host/etc/hosts`，下载器 URL 按新设备地址调整。旧实例和新实例不要同时管理同一 Hosts 文件。
