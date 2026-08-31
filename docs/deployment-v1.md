# AI-GDM 容器部署 v1

## 服务组成

- `app`：Go Web 服务与 GDAL 3.13.3 运行时，容器内使用 UID/GID `10001:10001`。
- `postgres`：PostgreSQL 17 + PostGIS 3.5，启动时由应用执行嵌入式迁移。
- `redis`：Redis 7.4.10，启用 AOF；仅作为缓存与最后成功结果辅助存储。

PostgreSQL 和 Redis 只连接内部 Docker 网络，不映射宿主机端口。应用容器使用只读根文件系统，只允许写入 LHASA 命名卷和受限临时目录。

## 配置

从 `deploy/runtime.env.example` 创建私有配置文件。至少填写：

- `POSTGRES_PASSWORD`
- `REDIS_PASSWORD`
- `DATABASE_URL`，使用 Compose 内部主机 `postgres:5432`
- `APP_ADMIN_TOKEN`

需要高德或 LLM 时，再分别启用开关并填写对应服务端 Key。高德 Key 必须是 Web 服务 Key；Key 只进入服务端容器环境，不会下发到浏览器。

所有密钥必须互不相同，配置文件权限应为 `0600`，不得提交 Git 或放入公开 Release。Compose 通过 `AI_GDM_RUNTIME_ENV_FILE` 读取私有配置。

## 构建与启动

```sh
docker build \
  --build-arg GOPROXY="${GOPROXY:-https://goproxy.cn,direct}" \
  --build-arg VERSION=dev \
  --build-arg VCS_REF="$(git rev-parse HEAD)" \
  -t ai-gdm/server:local .

AI_GDM_RUNTIME_ENV_FILE="$PWD/deploy/runtime.env" \
docker compose --env-file deploy/runtime.env up -d --wait
```

## 离线包一键部署

Linux 服务器解压正式发布包并验证外层 `.sha256` 后，在包目录执行：

```sh
sudo sh deploy/deploy.sh
```

Windows Docker Desktop 必须切换为 Linux containers，然后在 PowerShell 中执行：

```powershell
powershell -ExecutionPolicy Bypass -File .\deploy\deploy.ps1
```

部署入口会在任何 Docker 副作用前检查包内 `SHA256SUMS`，随后通过 `docker load` 加载应用、PostGIS 和 Redis 三个 Linux AMD64 镜像，并以 `--pull never --no-build` 启动。首次运行会原子生成 `deploy/runtime.env`、三个互不相同的随机密钥并限制文件权限；重复运行只校验和复用已有配置与命名卷，不重置密码或数据。

高德、博查或 LLM Key 不进入发布包。需要启用供应商时，应先在服务器私有目录创建完整 `runtime.env`，再指定：

```sh
sudo env AI_GDM_RUNTIME_ENV_FILE=/home/ubuntu/.config/ai-gdm/runtime.env \
  sh deploy/deploy.sh
```

也可在首次运行时通过进程环境提供 `AMAP_API_KEY`、`BOCHA_API_KEY`、`LLM_API_KEY`；脚本只把值写入权限受限的运行配置，不会打印。长期部署优先使用预先创建的私有配置文件，避免凭据进入命令历史。PowerShell 入口遵循相同边界，并对新文件收紧为当前用户 ACL。

访问：

- 控制台：`http://服务器地址:8080/`
- 存活探针：`GET /healthz`
- 就绪探针：`GET /readyz`

停止服务但保留数据：

```sh
sudo env AI_GDM_RUNTIME_ENV_FILE="$PWD/deploy/runtime.env" \
docker compose --env-file deploy/runtime.env down
```

只有明确需要删除全部数据库、缓存和 LHASA 制品时，才可额外使用 `down -v`。

## 验证

P10.1 和 P10.3 在腾讯 Ubuntu 执行：

```sh
sudo sh scripts/validate-docker.sh
sudo sh scripts/validate-deploy.sh
```

P10.1 门禁从固定 Git tree 创建只读快照，验证镜像构建、非 root、只读根文件系统、GDAL 版本、PostGIS 扩展、9 个迁移、Redis、HTTP 探针和重启持久化，并在结束时回收本轮资源。

P10.3 门禁从固定 Git tree 构建发布包，再启动独立 Docker-in-Docker 守护进程；内层守护进程初始镜像数必须为零，只允许从包内 tar 加载三个镜像。门禁连续运行部署脚本两次并执行一次保留卷的停启，验证非 root、只读根、GDAL、PostGIS、9 个迁移、Redis、HTTP 访问、运行配置幂等及 PostgreSQL/Redis/LHASA 三类持久化，结束时只按本轮所有权标签回收资源。

## 当前边界

- 容器离线可加载并启动不代表业务不需要互联网；实时 Earthdata、Open-Meteo、WorldPop、Overpass、geoBoundaries、高德、搜索和 LLM 仍需访问供应商。
- 高德路线是候选路线，不等同于交管部门确认道路开放。
- 仓库不内置可作为生产结论的资产价格和脆弱性数值。未导入真实、已批准且可审计的损失基线时，只允许基于真实道路暴露和公开案例生成明确标记的 `reference_only` 研究参考区间；输入不足时仍返回 `insufficient_data`，不得用测试数据冒充生产结果。
- 当前直接暴露 HTTP `8080`，尚未提供 TLS 终止；正式公网长期运行应增加可信反向代理和证书。
- Windows 脚本可校验和启动 Linux 容器栈，但本阶段的完整空缓存验收运行在腾讯 Ubuntu；Windows Docker Desktop 需至少分配 4 GiB 内存并预留镜像 tar、Docker 存储和持久卷空间。
