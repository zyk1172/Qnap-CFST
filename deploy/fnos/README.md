# 飞牛 fnOS 部署

> **验证范围：尚未在真实群晖或飞牛设备上运行过 CFHost，也未实测其图形安装界面。** 已在 Linux amd64 Docker 环境校验部署配置、容器运行、Hosts 写入和数据持久化；ARM64 二进制已交叉编译。多架构镜像构建与 NAS 实机验证是不同层级的检查，具体机型、系统版本、权限与重启后的行为仍需使用者确认。

使用飞牛的 Docker 应用或 SSH 下的 Docker Compose 部署。多架构镜像自动匹配 amd64 / arm64；设备的 fnOS 版本也需要支持 Docker。先阅读 [NAS 通用部署说明](../README.md)，确认宿主机与下载器容器的 Hosts 生效范围。

## Docker 应用的 Compose 部署

1. 在应用中心安装 Docker 应用。基础应用安装参考 [飞牛官方说明](https://help.fnnas.com/articles/v1/start/install-app)。
2. 在存储空间中创建持久化的 `cfhost` 项目文件夹及其 `data` 子文件夹。飞牛存储路径随存储空间和用户变化，不要套用 QNAP 的 `/share/Container` 或群晖的 `/volume1`。
3. 把本目录的 [compose.yaml](compose.yaml) 和 [.env.example](.env.example) 放入该目录，复制后者为 `.env`。
4. 支持 Compose 的 Docker 应用版本可从 Compose / 项目入口导入配置，工作目录选上面的项目文件夹。不同版本入口名称可能变化；没有该入口时使用下面的 SSH 方式。
5. 在 GUI 部署时，将 `CFHOST_DATA_DIR` 改为实际系统绝对路径。可在文件管理器查看路径，或通过 SSH 进入该目录后用 `pwd -P` 确认。界面未读取 `.env` 时，直接将 YAML 的对应变量替换为该路径。
6. 保持 Hosts 源路径 `/etc/hosts`、目标路径 `/host/etc/hosts`，端口默认 `9876:8080`，建立并启动容器。
7. 访问 `http://飞牛局域网IP:9876`，确认健康状态及数据文件，再保存域名、执行维护并手动应用 Hosts。

## SSH 部署

在飞牛开启 SSH，用有 Docker 管理权限的账号连接。先进入你建立的持久化项目目录，确认其中已有 `compose.yaml` 和 `.env.example`：

```bash
pwd -P
cp .env.example .env
mkdir -p data
# 编辑 .env；SSH 方式可保留 CFHOST_DATA_DIR=./data
sudo docker compose --env-file .env -f compose.yaml config
sudo docker compose --env-file .env -f compose.yaml up -d
sudo docker compose --env-file .env -f compose.yaml ps
```

`./data` 指向 Compose 文件旁的子目录，避免假定飞牛用户 ID 或存储卷编号。只在首次安装时复制 `.env`，后续修改保留已有文件。

更新与备份见 [通用部署说明](../README.md#检查更新与迁移)。迁移项目文件夹时，如果使用绝对路径，需要同时修改 `.env`；迁移前停止旧容器。下载器 RPC URL 填飞牛的局域网 IP 与实际发布的端口，不填 CFHost 容器中的 `127.0.0.1`。
