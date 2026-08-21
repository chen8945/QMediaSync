# 刮削命名模板与其他类型 NFO 契约

> 职责：说明刮削整理时命名模板的渲染与回退规则，以及媒体类型为其他时可用的 NFO 信息来源。
>
> 权威范围：本文件唯一维护模板渲染结果的清理与回退行为、其他类型的 NFO 兼容范围和番号取值顺序；模板可用变量的完整清单由 [整理文件（夹）模板可用变量](https://github.com/qicfan/qmediasync/wiki/%E6%95%B4%E7%90%86%E6%96%87%E4%BB%B6%EF%BC%88%E5%A4%B9%EF%BC%89%E6%A8%A1%E6%9D%BF%E5%8F%AF%E7%94%A8%E5%8F%98%E9%87%8F) 维护，数据库字段语义见 [数据库 schema 与迁移](database-schema.md)。
>
> 修改时机：修改命名模板渲染、模板变量、文件夹与文件名生成、NFO 解析或其他类型的信息来源时必须更新本文件。
>
> 相关代码：`backend/internal/models/scrapemedia.go`、`backend/internal/scrape/scrape_movie.go`、`backend/internal/scrape/scrape_tvshow.go`、`backend/internal/scrape/scrape_episode.go`、`backend/internal/helpers/nfo_decode.go`、`backend/internal/helpers/nfo_movie.go`。

## 模板渲染流程

刮削目录的 `folder_name_template` 和 `file_name_template` 在生成新文件夹名和新文件名时渲染：

1. 模板同时包含 `{{` 和 `}}`，或包含 `{% if`，按新语法（pongo2）渲染；否则按旧语法做变量替换。
2. 旧语法先替换旧语法变量；残留的单花括号占位符再按新语法变量名取值补齐，例如 `{videoFormat}` 与 `{{videoFormat}}` 取值相同。仍然取不到值的占位符原样保留在名称里，并写入 `命名模板 ... 中的变量 ... 无法取值，已原样保留` WARN 日志。`{tmdb_id}` 生成的 `{tmdbid-157336}` 含数字和连字符，不会被当作占位符。
3. 渲染结果按 `/` 和 `\` 逐层清理：去掉每层首尾空白，丢弃空层级以及 `.`、`..` 层级。
4. 清理后的结果为空时，回退到原名称。

## 渲染为空时的回退

模板留空表示保留原名称；模板变量全部取不到值时，行为与模板留空一致：

| 位置 | 回退顺序 |
| --- | --- |
| 电影文件夹名 | 来源端原文件夹名 → 视频文件名（不含扩展名） |
| 电视剧文件夹名 | 来源端原文件夹名 → 剧集名称 → 视频文件名（不含扩展名） |
| 电影与集的文件名 | 来源端原文件名（不含扩展名） |

回退时向 `app.log` 写入 `命名模板 ... 的变量全部为空，保留原名称` WARN 日志，包含模板、保留的名称和视频文件名。

## 新旧语法变量名互通

旧语法（单花括号）除旧语法专有变量外，也可以使用新语法的变量名，取值与新语法一致。旧语法专有的格式不变：`{tmdb_id}` 生成 `{tmdbid-157336}`，`{bitrate}` 生成 `20Mbps`，`{season_number}` 和 `{episode_number}` 不补零。当前媒体类型不支持的变量（例如电影模板里的 `{season_episode}`）按取不到值处理，原样保留。

## 演员变量

`{actors}` 是当前变量名，`{actor}` 是历史文档中的写法，两者取值相同，新旧语法都可用。取值规则：无演员为空字符串，1 至 2 位用 `, ` 连接，3 位及以上为 `多人演员`。

## 路径安全

命名模板和二级分类名都参与目标路径拼接，是外部可控输入，按三层防线处理：

1. 保存时校验：`folder_name_template` 和 `file_name_template` 拒绝绝对路径、反斜杠和 `..` 片段；二级分类名必须是单层目录名，拒绝路径分隔符、`.`、`..` 和纯空白。规则见 [请求校验约定](../engineering/request-validation.md)。
2. 取值时清理：模板渲染结果和二级分类名都经过 `helpers.SanitizePathSegments`，按 `/` 和 `\` 逐层丢弃空层级、`.` 和 `..`，因此存量配置和 NFO、TMDB 里带分隔符的字段值也无法跨出目标根目录。
3. 创建目录前校验：影视剧和季目录创建前用 `helpers.EnsureWithinDir` 校验目标路径仍在目标根目录内，越界时整理直接失败并记录错误日志，避免数据库里的旧值绕过前两层；本地二级分类目录使用 `helpers.SafeJoin` 创建。

网盘来源的路径是网盘内的逻辑路径，`..` 片段同样会被清理，不会影响宿主文件系统。

## 媒体类型为其他

其他类型只能整理，不能刮削，`scrape_type` 固定为仅整理，所有元数据只来自视频文件同目录的 NFO：

- 扫描阶段要求视频文件有对应 NFO，接受 `<视频文件名>.nfo`、`movie.nfo`、`tvshow.nfo`、`season.nfo` 以及 `season` 开头的 NFO；没有 NFO 的视频文件会被跳过，并在扫描完当前目录后写入 WARN 日志说明跳过数量。
- NFO 按根节点分派解析，支持 `movie`、`tvshow`、`season`、`episodedetails`；其他根节点按解析失败处理。
- NFO 声明 GBK 等非 UTF-8 编码时按声明的编码解析。
- 数值标签写成 `120 min`、`8.6 分` 这类带单位的值时，取其中的数字后重新解析，并写入 WARN 日志；取不到数字按空值处理。
- 标题取值顺序：`title` → `originaltitle` → `sorttitle`，都为空时使用视频文件名（不含扩展名）。
- 年份取值顺序：`year` → `premiered` → `releasedate` → `aired`。
- 番号取值顺序：`num` → `uniqueid[type=num]` → `code` → `id`。`code` 和 `id` 只接受同时包含字母和数字、且不是 IMDb ID 形式（`tt` 加数字）的值。
- 命中已有刮削信息且其中缺少番号或演员时，用 NFO 中的内容补全，不覆盖已有值。

## 不变量

- 模板渲染结果不得作为空名称使用：不能生成只剩扩展名的文件（例如 `.mp4`），也不能让目标层级塌回目标根目录。
- 旧语法模板中取不到值的变量占位符原样保留，不得从名称中移除，以保持既有模板的输出稳定。
- 模板渲染结果、二级分类名参与路径拼接前必须清理为安全的相对路径；最终落盘路径必须位于目标根目录内。
- 清理后的名称不包含空层级和 `.`、`..` 层级。
- 仅刮削（`only_scrape`）不改变文件夹名和文件名，模板不参与渲染。
- 其他类型的整理不查询 TMDB，`{title}`、`{year}`、`{num}`、`{actors}` 全部来自 NFO。

## 验证方式

```bash
(cd backend && go test ./internal/models/ -run 'TestOldSyntax|TestNewSyntax|TestSanitizeTemplateName|TestGenerateNameByTemplateOrKeep')
(cd backend && go test ./internal/helpers/ -run 'TestReadNfoAsMovie|TestMovieMedia|TestLooksLikeMediaNum')
(cd backend && go test ./internal/scrape/ -run 'TestGenerateNewName|TestCreateMediaFromNfo|TestGenrateCategory')
(cd backend && go test ./internal/scrape/ -run 'TestMakeParentPath')
(cd backend && go test ./internal/requests/)
(cd backend && go test ./internal/helpers/ -run 'TestSanitizePathSegments|TestEnsureWithinDir')
```

上述测试覆盖模板变量替换与回退、层级清理、路径穿越防护、NFO 根节点分派、编码与数值容错、番号取值顺序，以及其他类型仅整理的端到端整理结果。115、OpenList 和百度网盘来源共用同一份名称生成结果，仅落盘方式不同，需要人工在对应网盘各验证一次整理结果。
