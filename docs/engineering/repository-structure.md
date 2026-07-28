# 项目结构

> 职责：说明 QMediaSync 仓库顶层目录和构建产物的职责。
>
> 权威范围：本文档维护仓库结构；模块局部职责以代码目录内的 `README.md` 和源码为准。
>
> 修改时机：新增、删除或移动顶层目录、构建产物目录、打包目录或代码内 README 时必须更新本文档。
>
> 相关代码：`backend/`、`frontend/`、`docker/`、`scripts/`、`.github/`。

```text
backend/             Go 后端
  internal/           业务控制器、模型、同步、刮削、通知和基础能力
  emby302/            嵌入的 Emby 302 代理子项目
  openxpanapi/        百度网盘 OpenAPI 客户端
  assets/             嵌入的初始化、迁移和图标资源
  FNOS/               飞牛 FPK 打包模板与素材
  web_statics/        运行时 Web UI 静态资源（生成且忽略）
docker/              Dockerfile、容器入口脚本和在线更新监视脚本
frontend/            Vue / Vite 前端源码
  test/               受版本管理的 Vitest 测试和构建后检查
  dist/               前端生产构建输出（生成且忽略）
docs/                面向维护者和 AI 的正式文档，索引为 docs/README.md
reviews/             针对单次改动的审查记录，不属于正式文档，不进入 docs/README.md 索引
scripts/release/     GitHub Actions 发布打包辅助脚本、changelog 生成脚本和发布脚本共享函数
scripts/install/     Linux 裸机安装辅助脚本
.github/workflows/   CI、分支镜像和正式发布工作流
.changes/            每个版本的 GitHub Release 正文
cliff.toml           git-cliff 配置（从提交记录生成 changelog）
```

前端生产构建会输出到 `frontend/dist`。发布包、Docker 镜像和运行目录仍使用 `web_statics` 作为 Web UI 静态资源目录，后端从程序根目录下的 `web_statics` 提供 Web UI。

`config/`、`data/`、`logs/`、`backend/config/`、`backend/update/` 和上述生成目录属于运行时、本地构建或发布产物，均不作为源码或正式文档入口。

## 原项目地址

本仓库基于以下原项目合并而来：

- 后端：[qicfan/qmediasync](https://github.com/qicfan/qmediasync)
- 前端：[qicfan/q115-strm-frontend](https://github.com/qicfan/q115-strm-frontend)
- Wiki：[qicfan/qmediasync/wiki](https://github.com/qicfan/qmediasync/wiki)
