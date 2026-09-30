# 群晖 Synology 部署

> **验证范围：尚未在真实群晖或飞牛设备上运行过 CFHost，也未实测其图形安装界面。** 已在 Linux amd64 Docker 环境校验部署配置、容器运行、Hosts 写入和数据持久化；ARM64 二进制已交叉编译。多架构镜像构建与 NAS 实机验证是不同层级的检查，具体机型、系统版本、权限与重启后的行为仍需使用者确认。

适用于提供 Container Manager 套件的 DSM 机型，推荐使用 DSM 7.2 或更新版本的「项目」功能。镜像支持 amd64 / arm64，但能否安装容器套件取决于具体机型。先阅读 [NAS 通用部署说明](../README.md)，尤其是容器下载器的 Hosts 生效范围。

## Container Manager 项目

1. 在套件中心安装并打开 Container Manager。
2. 用 File Station 在 `docker` 共享文件夹下建立 `cfhost` 和 `cfhost/data`。其常见系统路径为 `/volume1/docker/cfhost`；存储卷不是 volume1 时改用实际路径。
3. 下载本目录的 [compose.yaml](compose.yaml)，上传到项目目录，命名为 `docker-compose.yml`。
4. 将 [.env.example](.env.example) 保存为项目目录中的 `.env`，按实际路径修改 `CFHOST_DATA_DIR`。File Station 的共享文件夹名称与 Compose 所需的系统绝对路径不同。
5. 在 Container Manager → 项目 → 新增，项目名称填 `cfhost`，路径选 `docker/cfhost`，来源选现有文件 / 上传 `docker-compose.yml`。也可使用编辑器粘贴 YAML；这种方式需要确认 `.env` 已在项目目录，或直接填写实际值。
6. 建立并启动项目。默认使用 bridge 网络、端口 `9876:8080`，数据目录挂载至 `/data`，宿主机 `/etc/hosts` 挂载至 `/host/etc/hosts`。
7. 打开 `http://群晖局域网IP:9876`，确认页面和容器健康状态正常，再保存域名并手动运行维护、应用 Hosts。

不需要为该项目建立 Web Station 门户。若有端口冲突，修改 `CFHOST_PORT`；若项目启动提示 Hosts 挂载权限不足，可用 SSH 的管理员方式部署并检查权限，无需开启 privileged。

操作入口参考：[群晖官方 Container Manager 项目说明](https://kb.synology.com/zh-cn/DSM/help/ContainerManager/docker_project?version=7)。

## SSH 部署与更新

把 `compose.yaml` 与 `.env.example` 下载到 NAS 的实际项目目录后执行。下面以 volume1 为例：

```bash
cd /volume1/docker/cfhost
cp .env.example .env
mkdir -p data
# 编辑 .env，确认数据目录、端口和 Hosts 文件路径
sudo docker compose --env-file .env -f compose.yaml config
sudo docker compose --env-file .env -f compose.yaml up -d
```

只执行一次 `cp`，更新时保留已有 `.env`。如果项目采用 `docker-compose.yml` 文件名，命令中的 `-f` 改为该名称。

更新时在项目目录执行 `pull` 和 `up -d`，见 [通用更新步骤](../README.md#检查更新与迁移)。GUI 项目可在 YAML 配置页面编辑后重新部署，确认它使用最新拉取的镜像。不要直接删除含数据的项目目录。

旧版 DSM 的 Docker 套件如果没有项目功能，可使用 SSH 下可用的 Compose。若仅有 `docker-compose` 命令，替换命令名，并先确认它能解析本文件的长格式挂载；解析失败时请升级 Compose / 容器套件。本文不保证所有旧版 DSM 的兼容性。
