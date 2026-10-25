# fnOS 原生包

此目录保存 FPK 模板，`qmediasync-amd64` / `qmediasync-arm64` 分别声明 `platform=x86` / `platform=arm`。安装身份、共享目录和入口 ID 均保留 `qmediasync`；对外名称为 QMediaSync。

从项目根目录运行 `scripts/release/package-fnos.sh`，输入二进制旁须有同次构建生成的 `.build.json`（版本、平台、架构、SHA-256）；正式发布与 Beta 工作流会一同传递。脚本在临时目录中复制模板、可执行文件和前端，并把 `common/service.sh` 放到包内 `app/scripts/fnos-service.sh`；不要把构建资源写进源码模板。正式版两份 manifest 的版本由发版脚本与更新日志一起提交，打包只校验；Beta 校验基础版本后仅修改临时副本的 manifest，再校验成品与二进制构建记录中的完整 Beta 版本一致，源码 manifest 始终不回写或提交。具体流程见[发布说明](../../docs/operations/release.md)。

`cmd/main` 使用公共脚本启停应用。公共脚本在平台私有运行目录 `TRIM_PKGVAR` 中保存 PID 与 Linux 进程启动时间，通过 `flock` 串行执行控制命令，停止最长等待 90 秒。只有首次配置服务退出且 `.initial-config-restart` 内容与其 PID 一致时，才再启动一次；平台停止标记优先，正式服务崩溃不会循环重启。请求路径由脚本通过 `QMS_INITIAL_CONFIG_RESTART_FILE` 注入，不是用户配置。

包装脚本创建私有运行文件时使用 `umask 077`；主程序及首次配置后的重新启动沿用平台传入的 umask，避免把仅供应用用户访问的权限传给媒体输出。权限边界见[部署说明](../../docs/operations/deployment.md#飞牛-fpk)。

安装钩子只准备目录，业务配置全部由 QMS 网页和通用后端生成；不覆盖已有 YAML、数据库或密钥。卸载默认保留数据；明确选择删除后，由 `uninstall_init` 验证单个 `qmediasync` 共享目录，只删除其 `config/`。平台目录权限、资源声明和旧目录授权回调继续保留，不使用新 SDK。

主配置位于共享目录下的 `config/`；高级 `.env` 和 HTTPS 证书仍遵循程序根目录 `config/` 的原规则。该目录还含平台配置，不能整体替换为共享目录的软链接。部署和数据保留约定见[部署说明](../../docs/operations/deployment.md)。

本地隔离验证：

```bash
python3 -m unittest discover -s scripts/tests -p 'test_fnos_lifecycle.py'
bash -n backend/fnos/common/service.sh
```

测试使用临时目录与替身进程；真实 FPK 安装、升级、保留数据重装、目录授权及系统自启仍须在 fnOS 上验证。验证入口见[验证说明](../../docs/engineering/verification.md)。
