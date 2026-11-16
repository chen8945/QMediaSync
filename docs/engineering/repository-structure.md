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
│   │   ├── httpapi/                 # 自有 JSON 公共响应与请求失败诊断
│   │   ├── controllers/             # HTTP 绑定、校验、业务调用与结果投影
│   │   ├── notification/            # 通知配置类型与共用纯校验
│   │   │   └── config/              # 通知配置写入、事务与应用编排
│   │   ├── v115/
│   │   │   ├── auth/                # 115 授权来源和会话
│   │   │   └── openapi/             # 115 API 客户端
│   │   ├── emby/                    # 两端共用的媒体库同步与事件业务
│   │   ├── medialibrary/
│   │   │   └── client/              # 共用传输与 Emby/Jellyfin 原生协议
│   │   ├── strm/
│   │   │   ├── config/              # 同步目录聚合保存
│   │   │   └── scope/               # 共享范围协调
│   │   ├── baidupan/
│   │   │   └── internal/openxpanapi/ # 仅供百度适配器使用的生成 SDK
│   │   ├── emby302/                 # 嵌入的 Emby 302 代理
│   │   ├── fnosproxy/               # 飞牛影视反代、协议兼容和来源缓存
│   │   ├── proxyhttp/               # 两代理共用的连接登记与最终关闭
│   │   └── …                        # 现有控制器、模型、同步、刮削等模块
│   └── web_statics/                  # 运行时 Web UI（生成且忽略）
├── packaging/
│   └── fnos/                        # FPK 模板、两架构资产及共享生命周期脚本
├── frontend/
│   ├── src/
│   │   ├── main.ts                  # 启动、认证 bootstrap 和 HTTP 注入
│   │   ├── App.vue                  # 应用接线、布局和全局进度展示
│   │   ├── views/                   # 路由页面，按领域归组
│   │   │   ├── auth/
│   │   │   ├── dashboard/
│   │   │   ├── accounts/
│   │   │   ├── files/
│   │   │   ├── sync/
│   │   │   ├── scrape/
│   │   │   ├── queues/
│   │   │   ├── backup/
│   │   │   ├── settings/
│   │   │   └── system/
│   │   ├── components/              # common 与各领域复用组件，含 media-library 设置面板
│   │   ├── api/                     # 领域请求、响应和 API 专用类型
│   │   ├── http/                    # 唯一客户端、认证和错误解析
│   │   ├── composables/             # 复用状态和生命周期
│   │   ├── stores/                  # auth、backup 和 pageState
│   │   ├── types/                   # 文件、备份、目录监控、授权等共享类型
│   │   ├── router/                  # 路由表、历史和同步懒加载壳
│   │   ├── utils/
│   │   ├── constants/
│   │   ├── const.ts                 # SERVER_URL 等现有常量入口
│   │   └── assets/
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

普通 JSON 由 `httpapi` 统一写出，协议边界见 [公共响应契约](../reference/api-response.md)。通知配置的依赖方向为 `controllers → notification/config → models/notificationmanager`；应用服务不依赖 HTTP DTO、Gin 或包络，模型和 manager 不反向依赖 config。共用纯校验归 notification 根包，事务与后台生命周期仍由原拥有者维护。

认证/CORS、下载代理和系统入口分别位于同一 controllers 包的 `auth_middleware.go`、`download_proxy.go` 和 `system.go`；按文件职责定位，函数和路由身份保持。

fnOS 模板和生命周期脚本的局部约束见 [原生包说明](../../packaging/fnos/README.md)，二进制与前端只在临时目录组包。

前端生产构建会输出到 `frontend/dist`。发布包、Docker 镜像和运行目录仍使用 `web_statics` 作为 Web UI 静态资源目录，后端从程序根目录下的 `web_statics` 提供 Web UI。

前端页面、组件和类型的归属及引用边界见 [前端开发约定](frontend-development.md#目录与类型归属)。

`config/`、`data/`、`logs/`、`backend/config/`、`backend/update/` 和上述生成目录属于运行时、本地构建或发布产物，均不作为源码或正式文档入口。

## 原项目地址

本仓库基于以下原项目合并修改而来：

- 后端：[qicfan/qmediasync](https://github.com/qicfan/qmediasync)
- 前端：[qicfan/q115-strm-frontend](https://github.com/qicfan/q115-strm-frontend)
- Wiki：[qicfan/qmediasync/wiki](https://github.com/qicfan/qmediasync/wiki)
