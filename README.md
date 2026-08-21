# WeKnora（二次开发）

本仓库 fork 自腾讯开源项目 [Tencent/WeKnora](https://github.com/Tencent/WeKnora)，并在官方代码上做了定制改动（Wiki 原文分块关联、库级 `source_revision` 素材指纹、MCP `wiki_graph` 等）。

Gitee：https://gitee.com/fengshikeji_admin/weknora

## 无法使用官方 Docker 部署

本仓库 **不能** 按官方 README 用 Docker 部署。

官方方式（`docker compose pull`、`make start-all`）会拉取 Docker Hub 上的 `wechatopenai/weknora-*` 镜像，对应的是**上游官方代码**，不包含本仓库改动。拉下来跑的是官方版本，本仓库功能不会生效。

产品介绍、架构说明与官方文档仍以 [Tencent/WeKnora](https://github.com/Tencent/WeKnora) 为准。
