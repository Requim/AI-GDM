# AI-GDM 发布包 v1

## 目标

发布包同时包含 Linux/Windows Go 可执行文件、Linux AMD64 完整 Docker 原始镜像、Compose 定义、空密钥配置模板、来源清单、manifest 和 SHA-256。发布产物只写入 `dist/`，不得进入 Git；默认要求发布目录至少保留 4 GiB 可用空间。

## 构建

在具备 Docker 的 Linux AMD64 主机执行：

```sh
sudo env PACKAGE_VERSION=v0.1.0 PACKAGE_PULL_IMAGES=1 \
  PACKAGE_REQUIRE_SOURCE_COMMIT=1 sh scripts/package-release.sh
```

默认输出：

```text
dist/
  ai-gdm-v0.1.0-linux-amd64/
    bin/
    images/ai-gdm-images-linux-amd64.tar
    deploy/
    docs/
    compose.yaml
    manifest.json
    SHA256SUMS
  ai-gdm-v0.1.0-linux-amd64.tar.gz
  ai-gdm-v0.1.0-linux-amd64.tar.gz.sha256
```

`images/ai-gdm-images-linux-amd64.tar` 由 `docker save` 生成，包含应用、PostGIS 和 Redis 的完整运行镜像。应用镜像已包含固定 GDAL 运行层，不包含 Go 构建器镜像。`deploy/release-images.env` 与 `deploy/compose.offline.yaml` 把离线启动绑定到包内三个固定标签，并禁止联网拉取替代镜像。

## 验证

```sh
sudo env PACKAGE_VERSION=v0.1.0 PACKAGE_PULL_IMAGES=0 sh scripts/validate-package.sh
```

门禁验证固定源码 tree、精确文件清单、二进制格式和 Go 构建信息、内部与外层 SHA-256、原始镜像 tar 结构、镜像平台与大小、空密钥模板，以及运行后不存在发布临时 tag。Docker 29 的镜像 ID 按 OCI 顶层 descriptor 摘要记录；校验器继续向下绑定唯一 `linux/amd64` manifest、legacy Config 和实际配置内容，不能用平台 config 摘要冒充顶层镜像 ID。构建时间必须是严格 UTC，版本必须是 `vMAJOR.MINOR.PATCH`。P10.3 再在隔离 Docker 数据目录中只通过 `docker load` 验证一键部署。

## 边界

- Windows 二进制在 Linux 上交叉编译并检查 PE/Go 构建信息；Windows Docker Desktop 端到端由具备 Windows 环境的评估方执行。
- 离线镜像解决的是镜像仓库依赖；实时 Earthdata、气象、人口、道路、高德、搜索和 LLM 仍要求部署服务器访问互联网。
- 发布包不包含 API Key、管理员令牌、数据库口令、运行数据或损失评估基线数据。
- `manifest.json` 记录源码 commit/tree/source SHA-256、镜像 ID/平台/大小和全部 payload 摘要。预提交固定 tree 验证会明确记录 `sourceCommit=unknown`；正式发布必须设置 `PACKAGE_REQUIRE_SOURCE_COMMIT=1`，且该 commit 的真实 Git tree 必须与打包 tree 完全一致。
