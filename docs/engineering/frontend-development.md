# 前端开发约定

> 职责：定义 Vue 前端的 HTTP 客户端、状态刷新、路由、响应式布局和交互反馈约定。
>
> 权威范围：本文档维护前端实现与协作边界；请求字段和校验见 [请求校验约定](request-validation.md)，SSE 协议见 [实时事件](../architecture/realtime-events.md)，上传和 STRM 状态机见 [上传与 STRM 处理](../architecture/upload-and-strm-processing.md)。
>
> 修改时机：修改 HTTP 客户端注入、实时刷新策略、路由历史、通用响应式组件、通知渠道热刷新或全局交互约定时必须更新本文档。
>
> 相关代码：`frontend/src/http/`、`frontend/src/api/`、`frontend/src/composables/`、`frontend/src/router/`、`frontend/src/components/common/`、`frontend/src/utils/`、`backend/internal/controllers/notification.go`、`backend/internal/notificationmanager/`。

## 组件和请求边界

- Vue 代码使用 Composition API 和 `<script setup lang="ts">`。组件、页面和 composable 的通用类型约定见 [AI 编码助手工作说明](ai-assistant.md#前端约定)。
- `frontend/src/http/client.ts` 维护唯一 Axios 实例的超时、Cookie 凭据、CSRF 请求头和认证失效处理。`main.ts` 负责配置该实例并通过类型化 `httpKey` 提供给应用；组件和 composable 通过 `useHttpClient()` 取得实例，不使用 Axios 静态对象、字符串注入键或可选链绕过未初始化错误。
- 业务领域请求封装放在 `frontend/src/api/`；不要在其中加入 Axios 拦截器或全局客户端配置。不要为客户端设置全局 `Content-Type: application/json`：对象请求由 Axios 序列化，上传请求保留自己的 `multipart/form-data` 配置。
- 需要响应窗口尺寸变化的组件使用 `useDeviceType()`，由 composable 统一注册和清理监听。`frontend/src/utils/README.md` 只说明 `deviceUtils.ts` 和其他工具自身的局部 API。
- 分页页面使用 `components/common/ResponsivePagination.vue` 承担布局和事件透传；页面自身保留页码、每页数量和数据加载状态。

## HTTP 快照、SSE 与轮询

- 分页列表和详情页的 HTTP 响应是当前状态的权威快照。全局 SSE 事件是“列表可能变化”的至多一次通知；收到结构性事件或原生重连后，按当前页和筛选条件重新拉取快照，不把事件当作可靠消息队列。
- SSE 连接反馈必须区分首次 `connecting` 与原生错误后的 `reconnecting`；首次建连不展示断线告警，只有 `reconnecting` 才显示重连提示。具体状态语义见[实时事件](../architecture/realtime-events.md)。
- 上传队列的 `upload_queue_changed` 进度和源文件清理 patch 是现有例外：只对当前页已有任务局部合并展示字段；创建、清理、重试和其他结构性变化仍重新加载快照。修改该例外时，同时检查 `AppUploadQueue.vue`、`uploadQueueDisplayUtils.ts` 和上传 / STRM 架构文档。
- 长任务优先使用现有事件流；HTTP 状态接口用于首次加载、手动刷新和断线恢复。115 OAuth、二维码授权等短生命周期外部流程沿用各自轮询，不并入通用队列事件。
- 新增轮询必须在页面隐藏时暂停，避免请求重叠，并在卸载时清理定时器和事件监听。若定时器回调会等待 HTTP 请求完成后续排，卸载还必须使该回调失效，避免飞行中请求完成后在已离开的页面重新创建轮询。

## 路由与浏览器历史

- 页面标题只在 Vue Router 成功完成导航后更新，避免把目标页标题写入上一条浏览器历史记录。
- 业务页面保留按需加载时，使用 `router/asyncRoute.ts` 的同步路由壳：导航应先完成并显示目标页标题、加载中指示和骨架占位，再由壳内部加载页面模块；不得把 `defineAsyncComponent()` 返回值直接注册为路由 `component`。路由壳名称必须保持原页面组件名，确保 KeepAlive 包含名单不变。
- 登录成功、退出、认证失效和鉴权守卫跳转使用 `router.replace` 或带 `replace: true` 的重定向，避免登录页、失效页或被拦截页滞留在历史栈。
- 详情页和表单页的“返回 / 取消”使用 `navigateBackOrReplace(router, fallback)`：存在应用内上一页时回退，直接打开深链或没有上一页时替换到兜底列表页。
- 表单保存成功后直接 `replace` 到列表页，不复用“返回 / 取消”逻辑。

## 通知渠道配置生效

- 渠道创建、更新、启用、禁用和删除后，控制器同步调用通知管理器的单渠道重载或删除逻辑，保证接口返回后发送路由使用最新配置。
- 通知规则变更只调用 `ReloadRules()`，不得重建渠道 handler，避免影响正在运行的后台监听。
- 需要后台监听的渠道不能在 HTTP 请求路径中执行外部网络初始化。例如 Telegram Bot 的初始化、菜单设置和长轮询必须由 handler 自身在后台协程中完成。

## 交互和响应式布局

- 设置页保存类主操作使用 `type="success"`；启动和恢复等正向动作使用 `success`；暂停、停止、重试、恢复备份和数据库修复等有风险但非删除动作使用 `warning`；删除、清空和撤销使用 `danger` 并保留确认步骤；测试、搜索、生成和添加等中性主动作使用 `primary`。
- 设置页底部主操作使用 `size="large"`；工具栏使用默认尺寸；表格行内操作使用 `size="small"`、`link` 或 `text`。普通动作按钮优先使用 Element Plus 的 `:icon` 属性；下拉箭头、后缀状态图标等非主动作图标可以使用手写 `<el-icon class="el-icon--right">`。
- 已使用页面内状态提示（例如底部 `el-alert`）展示保存成功时，不再额外弹出成功 toast；错误、校验失败、复制、测试连接和启动任务等短生命周期反馈可以使用 `ElMessage`。
- 简单表单弹窗桌面端宽度不超过 `500px`，移动端使用 `min(500px, calc(100vw - 32px))`；同类新增和编辑弹窗复用相同规则。字段较多时，弹窗最大高度为 `calc(100dvh - 32px)`，只让正文区域滚动。运行时返回的状态或错误文本按最坏长度布局，可换行、允许连续 URL 断行，并在内容区内滚动。
- 移动端表单标签置于控件上方；二维码授权短状态文本居中并使用 Element Plus 语义色，只有 `failed` 的长错误文本左对齐且可滚动。页面局部操作区使用带页面前缀的语义化类名，避免通用类命中全局样式后破坏 Flex 对齐。
- 共享目录选择器中，点击目录行或按 Enter / Space 只更新临时选择并展开 / 收起，底部“选择”按钮才提交给调用方；展开和重试等嵌套控件的键盘事件不得冒泡到目录行。每个子目录 `group` 必须是拥有其展开状态的 `treeitem` 的后代，子节点键盘事件也不得冒泡并激活父节点。空目录成功加载后缓存为叶子节点，加载失败必须显示可重试状态，不能把失败当成空目录。
- 目录选择器的子目录加载同样使用节点级递增请求序号。新建文件夹成功后必须使该父节点较早发出的列表请求失效，避免旧响应覆盖刚插入的目录；叶子节点不设置 `aria-expanded`，只对可展开或尚未确认是否为空的目录设置该状态。
- 目录选择器使用面包屑和刷新按钮提供位置反馈，不增加搜索、收藏或最近目录等额外导航；点击已加载的面包屑祖先目录只切换临时选择并展开该目录，仍需点击底部“选择”提交。底部操作区不重复展示当前路径。长路径允许面包屑换行，不能裁切末尾的当前目录。刷新只重建当前临时选择的祖先链：目录仍存在则保留选择，缺失则回退最近可选父目录；若父级是不可提交的根目录，清空临时选择并提示重新选择。树区域要在 `100dvh` 可视高度内滚动；加载遮罩挂在树的非滚动容器上，底部操作区保持可达。
- 当调用方以 `rootPath` 表示受限目录范围时，身份型目录源还必须传递对应的 `rootId`；选择器的初始加载和刷新都用该 ID 作为 `parent_id`，确保面包屑显示的范围与实际可选范围一致。
- 当受限根目录为空且没有临时选择时，可将已知的 `rootId` / `rootPath` 仅作为“新建文件夹”的父目录；根目录不应成为树节点或“选择”按钮可提交的值。
- 目录源类型、账号、`rootId` 或 `rootPath` 变化会使之前的根目录请求失效；选择器只接受最新一次根目录加载的成功、失败和结束状态，避免旧响应覆盖新的受限目录树。根目录加载或刷新校验期间，底部“选择”、新建入口和已打开新建弹窗的确认按钮必须禁用；新建请求进行期间也不得启动刷新，避免刷新候选树覆盖创建结果。在未渲染的受限根目录下新建成功时，也要使此前根目录请求失效并结束其加载状态，保留刚插入的目录。

## 验证方式

- 修改 Vue 组件、composable、HTTP 客户端或路由后，按 [验证说明](verification.md) 选择相应的前端验证。
- 改动接口字段、校验提示或状态枚举时，同时检查 [请求校验约定](request-validation.md)、相应架构契约和前端表单 / 展示映射。
