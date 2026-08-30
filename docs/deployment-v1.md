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

访问：

- 控制台：`http://服务器地址:8080/`
- 存活探针：`GET /healthz`
- 就绪探针：`GET /readyz`

停止服务但保留数据：

```sh
AI_GDM_RUNTIME_ENV_FILE="$PWD/deploy/runtime.env" \
docker compose --env-file deploy/runtime.env down
```

只有明确需要删除全部数据库、缓存和 LHASA 制品时，才可额外使用 `down -v`。

## 验证

P10.1 在腾讯 Ubuntu 执行：

```sh
sudo sh scripts/validate-docker.sh
```

门禁从固定 Git tree 创建只读快照，验证镜像构建、非 root、只读根文件系统、GDAL 版本、PostGIS 扩展、9 个迁移、Redis、HTTP 探针和重启持久化，并在结束时回收本轮资源。

## 当前边界

- 容器离线可加载并启动不代表业务不需要互联网；实时 Earthdata、Open-Meteo、WorldPop、Overpass、geoBoundaries、高德、搜索和 LLM 仍需访问供应商。
- 高德路线是候选路线，不等同于交管部门确认道路开放。
- 仓库不内置资产价格和脆弱性数值。未导入真实、已批准且可审计的损失基线时，损失评估会返回 `insufficient_data`，不得用测试数据冒充生产结果。
- 当前直接暴露 HTTP `8080`，尚未提供 TLS 终止；正式公网长期运行应增加可信反向代理和证书。
