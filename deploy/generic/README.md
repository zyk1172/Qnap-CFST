# 通用 Linux NAS 部署

> **验证范围：尚未在真实群晖或飞牛设备上运行过 CFHost，也未实测其图形安装界面。** 已在 Linux amd64 Docker 环境校验部署配置、容器运行、Hosts 写入和数据持久化；ARM64 二进制已交叉编译。多架构镜像构建与 NAS 实机验证是不同层级的检查，具体机型、系统版本、权限与重启后的行为仍需使用者确认。

适用于支持 Docker 和 Docker Compose 的其他 Linux NAS，包括具备相应容器功能的自建 NAS。预构建镜像支持 amd64 / arm64。先阅读 [NAS 通用部署说明](../README.md)。

在 NAS 持久化存储上创建项目文件夹，进入该目录，将 [compose.yaml](compose.yaml) 和 [.env.example](.env.example) 保存到此处，然后：

```bash
cp .env.example .env
mkdir -p data
# 按需编辑 .env 的端口、数据路径、Hosts 路径与时区
sudo docker compose --env-file .env -f compose.yaml config
sudo docker compose --env-file .env -f compose.yaml up -d
sudo docker compose --env-file .env -f compose.yaml ps
```

默认数据目录为 Compose 文件旁的 `data`，WebUI 为 `http://NAS局域网IP:9876`。GUI 导入时建议使用绝对数据路径，并确认项目能读取 `.env`。

默认管理宿主机 `/etc/hosts`。若 NAS 平台限制访问系统文件，可将 `CFHOST_HOSTS_FILE` 指向预先创建的独立 Hosts 文件，再按 [共享 Hosts 说明](../README.md#hosts-生效范围) 配置需要使用它的下载器；单独生成文件不会自动改变其他应用的解析。

保留 `.env` 和数据目录，在同一目录执行 `pull`、`up -d` 更新。这份配置可用于支持 Compose 的 Linux NAS。尚未提供 Docker / Linux 容器支持的系统需要另行适配。
