# 前端开发约定

> 职责：定义 Vue 前端的 HTTP 客户端、状态刷新、路由、响应式布局和交互反馈约定。
>
> 权威范围：本文档维护前端实现与协作边界；请求字段和校验见 [请求校验约定](request-validation.md)，SSE 协议见 [实时事件](../architecture/realtime-events.md)，上传和 STRM 状态机见 [上传与 STRM 处理](../architecture/upload-and-strm-processing.md)。
>
> 修改时机：修改 HTTP 客户端注入、实时刷新策略、路由表结构、路由历史、侧边栏菜单、通用响应式组件、通知渠道热刷新或全局交互约定时必须更新本文档。
>
> 相关代码：`frontend/src/http/`、`frontend/src/api/`、`frontend/src/composables/`、`frontend/src/router/`、`frontend/src/App.vue`、`frontend/src/components/common/`、`frontend/src/utils/`、`backend/internal/controllers/notification.go`、`backend/internal/notificationmanager/`。

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

## 路由表结构与侧边栏菜单

- 侧边栏一级菜单由 `meta.showInMenu` 为 `true` 且没有 `meta.parent` 的路由生成，子菜单由 `meta.parent` 指向该一级菜单的 `name` 生成。一级菜单只做导航容器，不注册自己的页面组件，必须用 `redirect` 指向其首个子页面；例如 `/settings` 重定向到 `/settings/user`、`/database` 重定向到 `/database/backup/settings`。
- `meta.parent` 是归属关系的唯一来源，不得用路径前缀推断归属。路由路径与所属一级菜单可以不一致，例如 `/settings/strm` 归属 `sync`，`/settings/tmdb`、`/settings/ai` 和 `/settings/category-strategy` 归属 `scrape`。
- 侧边栏展开状态按 `route.meta.parent` 解析出一级菜单的 `path` 后再展开。Element Plus 的 `el-menu` 只在初始化时读取一次 `default-openeds` 且不监听该 prop，因此该 prop 只负责首屏展开；SPA 内跳转到 `showInMenu` 为 `false` 的详情页或表单页时，必须通过菜单实例的 `open()` 补上展开，不能只依赖计算属性。
- 详情页和表单页即使 `showInMenu` 为 `false`，也要设置 `meta.parent`，否则进入这些页面时所属一级菜单不会展开。
- 侧边栏为滚动和展开性能覆盖了 Element Plus 的默认过渡，覆盖必须用 `.el-aside` 限定，`el-main` 内的 `el-collapse` 仍使用原生折叠动画。菜单项和子菜单标题不得重新引入基于 `background-color` 的悬停过渡。
- 滚轮滚动期间由 `is-menu-scrolling` 关闭菜单的 `pointer-events`，避免指针静止时每帧重算 hover 触发重绘；触摸和拖动滚动条不触发 `wheel`，不进入该状态。
- 子菜单展开和收起不使用 Element Plus 的 `max-height` 折叠动画（逐帧触发布局），改为高度一次性到位、由 `opacity` 和 `transform` 提供过渡；收起用 `transition-delay` 把高度塌陷推迟到动画结束，该延迟必须与收起动画时长取自同一个自定义属性，两者不一致会出现淡出被裁断或塌陷前空等。时长只影响合成器插值帧数，不增加布局和重绘成本，可按观感调整。`prefers-reduced-motion: reduce` 下必须同时关闭 `animation` 和 `transition`，只关 `animation` 会保留收起延迟。
- 侧边栏动效以 144Hz 显示器（帧预算 6.94ms）为基准。Element Plus 原生折叠动画实测展开和收起的中位帧时间约 180ms 和 167ms（约 6fps），高度一次性到位后回到 6.9ms 满帧；`contain: paint` 和菜单容器的 `flex` 取值实测均无影响，不要以性能为由改动它们。`text-rendering: optimizeLegibility` 实测同样无影响，且现代浏览器默认就开启 kerning 和常用连字（`font-kerning` 初始值 `auto`、`font-variant-ligatures` 初始值 `normal`），该声明在本项目是空操作，已从 `base.css` 移除；本项目正文用 `微软雅黑`、`Arial` 和等宽字体，中文字体没有 kern pair，等宽字体不做连字，删除前后渲染一致。
- 帧时间无法自动化验证（`frontend/test/regression/sidebar-menu-motion.test.ts` 只对滚动态的进入和退出做行为断言，动效本身仍只锁源码契约），调整侧边栏动效后需人工复核：开启绘制闪烁时滚轮滚动菜单不应持续闪绿，用 `requestAnimationFrame` 采样展开和收起的中位帧间隔应接近显示器刷新周期。测量须在页面保持焦点、没有浏览器扩展注入的窗口进行并丢弃起始若干帧，否则 rAF 起始延迟会产生刷新周期整数倍的假尖峰。
- `.el-menu-vertical` 上的 `will-change: transform` 是 Chromium 绘制缺陷的规避手段，不是性能优化，不能删。上方子菜单展开使菜单项整体位移后，图标 `<svg>` 内 `<path>` 的绘制会被持续跳过：元素盒、背景和 outline 都正常绘制，计算样式、几何、命名空间和 `d` 均正常，hover 或改背景色这类普通重绘修不好，只有改变页面缩放才恢复；`--disable-gpu` 和 Firefox 下均不复现。把菜单提升为独立合成层可稳定规避，该规则若在后续 Chromium 版本失效，备选为 `.el-aside .el-icon { backface-visibility: hidden }`。

## 路由与浏览器历史

- 重命名或移动已发布的路由路径时，必须保留旧路径的 `redirect` 记录，保证书签、浏览器历史和外部链接继续可用；旧路径记录不设置 `meta`，因此不会进入侧边栏菜单。
- 路由表末尾必须保留 `/:pathMatch(.*)*` 兜底记录并重定向到首页。没有兜底记录时，未知路径匹配不到任何记录，`to.meta.requiresAuth` 为 `undefined` 会让鉴权守卫整体失效，页面只剩布局和空白正文；重定向到首页可以让首页的 `requiresAuth` 继续触发守卫，未登录时仍然跳转登录页。
- 页面标题只在 Vue Router 成功完成导航后更新，避免把目标页标题写入上一条浏览器历史记录。
- 业务页面保留按需加载时，使用 `router/asyncRoute.ts` 的同步路由壳：导航应先完成并显示目标页标题、加载中指示和骨架占位，再由壳内部加载页面模块；不得把 `defineAsyncComponent()` 返回值直接注册为路由 `component`。路由壳名称必须保持原页面组件名，确保 KeepAlive 包含名单不变。
- 登录成功、退出、认证失效和鉴权守卫跳转使用 `router.replace` 或带 `replace: true` 的重定向，避免登录页、失效页或被拦截页滞留在历史栈。
- 详情页和表单页的“返回 / 取消”使用 `navigateBackOrReplace(router, fallback)`：存在应用内上一页时回退，直接打开深链或没有上一页时替换到兜底列表页。
- 表单保存成功后直接 `replace` 到列表页，不复用“返回 / 取消”逻辑。

### 页面头部、标题元信息与加载态

- 路由 `meta.title` 是菜单标题和浏览器标题的兼容字段；页面展示身份使用可选的 `meta.page`，字段包括 `title`、`description`、`icon` 和有限的 `variant`（`management`、`compact`、`settings`、`detail`）。页面展示标题缺失时回退到 `meta.title`，不得把菜单标题假定为页面展示标题。
- 页面视图使用 `components/common/PageHeader.vue` 呈现页面级身份。页面头部是透明、轻量的内容身份区，不使用灰色底色、渐变、阴影或卡片边框；页面业务操作通过 `actions` slot 传入，默认位于头部右侧，返回类操作可以使用 `actions-position="start"` 放在标题左侧。需要在标题区域下方展示的统计通过 `stats` slot 传入并保持左对齐，不放入 actions 区域；管理列表页的统计也必须沿用这一位置。请求、loading、disabled、确认和统计计算仍由页面视图负责，不提升到 `App.vue`。Emby 使用一个共享页面头部作为页面级 `h1`，配置卡片保留 `h2/h3` 内容标题和静止状态下可见的边框/分隔线；数据库修复使用共享页面头部，修复说明和操作保留在内容卡片中。文件管理器使用共享页面头部，账号栏、记录详情和分类卡片等内容分区标题不迁移为页面头部。同步任务详情是卡片内特例，共享页面头部放在详情卡片的 `header` 插槽中。
- 页面级统计卡片统一使用 `components/common/PageStats.vue`，页面视图只提供统计项的图标、值、标签和语义色调。共享组件桌面端保留四项横向卡片的既有尺寸，移动端使用两列紧凑网格；新增管理页不得复制 `stats-bar`、`stat-item` 等局部统计样式。
- `PageHeader` 是页面内容视图的唯一页面级 `h1` 来源，页面正文不得再添加同级页面标题；标题统一使用共享字号和间距，图标为装饰内容时必须隐藏于无障碍树。桌面端显示共享头部身份，移动端由 `App.vue` 顶部栏提供默认页面标题，并默认隐藏 `PageHeader` 的 identity；需要在移动端保留完整页面主副标题的页面显式使用 `show-identity-on-mobile`，例如首页。首页移动端的 identity 和 actions 均水平居中，形成连续的标题与操作区。带 `actions` 或 `stats` 时仍保留必要的操作或统计区域，不能为了隐藏重复标题而一并隐藏这些内容。移动端顶部标题继续使用菜单兼容字段 `meta.title`，页面展示标题允许换行，不得挤压顶部栏的菜单和用户入口。管理列表页的统计必须通过 `stats` slot 放在标题区域左侧，并在移动端继续显示。
- actions 的桌面端布局默认保持在头部右侧并与页面身份区垂直居中；`actions-position="start"` 的返回类操作不下移，移动端统一恢复为 `0`。需要恢复“返回操作在上、页面身份在下”的详情页头部时使用 `actions-layout="stacked"`，不要通过页面专用 class 单独改变同类页面的按钮位置。
- `router/asyncRoute.ts` 的同步路由壳和 `RouteLoading` / `RouteLoadError` 只表示页面模块加载。桌面端加载/失败态可以显示标题占位，移动端隐藏重复的页面级标题，但保留 `role="status"` / `aria-live` / `aria-busy` 或错误告警、可键盘操作的重试按钮。页面 API 数据加载继续由页面或数据组件负责，不与路由模块加载态合并。
- 路由加载动画和页面头部动效必须在 `prefers-reduced-motion: reduce` 下关闭；新增或迁移页面头部后应检查 320px/480px 窄屏、键盘焦点、长标题换行和页面操作可达性。

## 通知渠道配置生效

- 渠道创建、更新、启用、禁用和删除后，控制器同步调用通知管理器的单渠道重载或删除逻辑，保证接口返回后发送路由使用最新配置。
- 通知规则变更只调用 `ReloadRules()`，不得重建渠道 handler，避免影响正在运行的后台监听。
- 需要后台监听的渠道不能在 HTTP 请求路径中执行外部网络初始化。例如 Telegram Bot 的初始化、菜单设置和长轮询必须由 handler 自身在后台协程中完成。

## 交互和响应式布局

- `App.vue` 的 `.main-content` 是页面 shell 的统一留白边界：桌面、平板和常规移动端保持一致的顶部留白，窄屏仅按同一规则适度收紧；移动端顶部栏的负 `margin-top` 必须精确抵消当前 shell 的顶部 `padding`，并同步抵消对应的水平 padding，避免顶部栏改变页面内容起始位置。当前约定是桌面端使用 `20px`、平板端使用 `15px`、常规移动端使用 `10px`，`max-width: 480px` 窄屏使用 `8px`。
- 短屏横屏紧凑规则只适用于 `max-width: 768px` 的移动端，并同时满足短高度和横屏条件；此时 shell 与顶部栏按 `8px` 对齐，顶部栏使用 `-8px` 的负顶部偏移。不得让该规则影响桌面或平板，也不要在其他页面单独复制一套顶部栏偏移。
- 本轮目标的管理列表和设置页根容器（网盘账号、STRM 同步目录、刮削目录、API Key、登录设备）以及普通 `.main-content-container` 页面通过共享 shell 样式统一标题顶部留白：桌面和平板为 `20px`，移动端为 `10px`；`full-width-container` 等特殊全宽容器不纳入该覆盖，保留其自身的 shell 抵消规则。该规则只覆盖页面顶部，不改变既有水平和底部间距。

### 长内容 Tooltip

- 页面级长路径、长错误原因和帮助说明使用 `qms-contained-tooltip`；需要稳定目标的组件通过 `append-to="body"` 挂载，必须局部挂载的组件使用已挂载的 `HTMLElement` 引用，不使用尚未插入 `document` 的页面根节点选择器。共享样式保留文本换行并允许连续路径断行。
- 共享 `ResponsiveRecordTable` 的表格溢出 Tooltip 通过 `tooltip-options` 统一使用 `qms-contained-tooltip` 和 `strategy: 'fixed'`，不得覆盖为 `absolute`，避免弹出层被表格 wrapper 裁剪。
- 目录选择器和网盘文件管理中的目录选择树不添加路径 Tooltip；当前目录位置由面包屑展示，不使用原生 `title` 或全局容器选择器。
- 短按钮提示继续使用 Element Plus 默认 Tooltip，避免全局覆盖菜单、下拉框和其他浮层样式。

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
