# 项目结构

> 职责：说明 QMediaSync 仓库顶层目录和构建产物的职责。
>
> 权威范围：本文档维护仓库结构；模块局部职责以代码目录内的 `README.md` 和源码为准。
>
> 修改时机：新增、删除或移动顶层目录、构建产物目录、打包目录或代码内 README 时必须更新本文档。
>
> 相关代码：`backend/`、`frontend/`、`packaging/`、`docker/`、`scripts/`、`.github/`。

```text
qmediasync/
├── backend/
│   ├── main.go                       # 薄入口、构建变量和嵌入资源声明
│   ├── assets/                       # 默认配置、首次配置向导和图标资源
│   │   ├── db_config.html           # 首次配置向导
│   │   └── emby302.yaml             # 编译嵌入的 302 默认配置
│   ├── rsrc_windows_*.syso           # Windows 资源，属于根 main 包
│   ├── go.mod
│   ├── go.sum
│   ├── internal/
│   │   ├── app/                     # 启动、生命周期、路由和代理组装
│   │   ├── v115/
│   │   │   ├── auth/                # 115 授权来源和会话
│   │   │   └── openapi/             # 115 API 客户端
│   │   ├── emby/
│   │   │   └── client/              # Emby REST 客户端，父目录保留业务编排
│   │   ├── strm/
│   │   │   ├── config/              # 同步目录聚合保存
│   │   │   └── scope/               # 共享范围协调
│   │   ├── baidupan/
│   │   │   └── internal/openxpanapi/ # 仅供百度适配器使用的生成 SDK
│   │   ├── emby302/                 # 嵌入的 Emby 302 代理
│   │   ├── fnosproxy/               # 飞牛影视反代、协议兼容和来源缓存
│   │   └── …                        # 现有控制器、模型、同步、刮削等模块
│   └── web_statics/                  # 运行时 Web UI（生成且忽略）
├── packaging/
│   └── fnos/                        # FPK 模板、两架构资产及共享生命周期脚本
├── frontend/
│   ├── test/                        # Vitest 测试和构建后检查
│   └── dist/                        # 前端生产构建输出（生成且忽略）
├── docker/                          # Dockerfile、容器入口和更新脚本
├── scripts/
│   ├── release/                     # 发布、打包、changelog 及共享函数
│   ├── install/                     # Linux 裸机安装脚本
│   └── tests/                       # 脚本替身测试，不依赖真实部署
├── docs/                            # 维护者文档，索引为 docs/README.md
├── .github/                         # CI、发布工作流和 issue 表单
├── .changes/                        # 各版本的 GitHub Release 正文
└── cliff.toml                       # 从提交记录生成 changelog
```

根包向 `internal/app` 传入构建默认值和嵌入资源，应用组装调用现有业务模块；业务模块不反向依赖 app。同一供应商下的子目录仍是独立 Go 包，现有包名保留。`models`、`helpers`、`syncstrm` 等业务职责仍以当前实现为准。

fnOS 模板和生命周期脚本的局部约束见 [原生包说明](../../packaging/fnos/README.md)，二进制与前端只在临时目录组包。

前端生产构建会输出到 `frontend/dist`。发布包、Docker 镜像和运行目录仍使用 `web_statics` 作为 Web UI 静态资源目录，后端从程序根目录下的 `web_statics` 提供 Web UI。

`config/`、`data/`、`logs/`、`backend/config/`、`backend/update/` 和上述生成目录属于运行时、本地构建或发布产物，均不作为源码或正式文档入口。

## 原项目地址

本仓库基于以下原项目合并修改而来：

- 后端：[qicfan/qmediasync](https://github.com/qicfan/qmediasync)
- 前端：[qicfan/q115-strm-frontend](https://github.com/qicfan/q115-strm-frontend)
- Wiki：[qicfan/qmediasync/wiki](https://github.com/qicfan/qmediasync/wiki)
