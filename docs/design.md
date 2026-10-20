# codex-timezone 设计

CPA 插件。在请求发往 Codex 上游之前，把请求里模型可见的日期和时区文本改成配置的时区。原理分析见 [codex-timezone-sync-principle.md](codex-timezone-sync-principle.md)，本实现只复用其原理，不含原版代码。

## 范围

- 只改请求体里的文本。不改 HTTP 头、工具返回、沙箱 `TZ`。
- 只处理 `ToFormat == codex`（不区分大小写）。其他格式原样放行。
- 时区由用户配置，不做出口 IP 探测。原版的探测器、状态文件、管理面都不在本实现内。

## 挂载点

能力 `request_normalizer`，方法 `request.normalize`。宿主在原生翻译之后调用（`sdk/translator/registry.go`），所以拿到的已经是 Codex 请求形态（顶层 `instructions` + `input[]`）。

宿主侧约定（读 v8.0.35 源码确认）：

- 响应 `Body` 为空 = 不改写，宿主继续用原请求。因此所有跳过路径都返回空 `PayloadResponse`，不返回错误。
- 插件与宿主同进程、无沙箱。宿主的 `fusePlugin` 只覆盖宿主侧适配器的 panic，不覆盖插件自己 Go 运行时里的 panic，后者按 Go 语义会终止整个进程（未实测）。所以 `cliproxyPluginCall` 里 recover 并转成错误信封，宿主随后按错误走 fail-open。
- `plugin.quiesce` 未实现时宿主按 `unknown_method` 降级为 debug 日志。这里仍显式返回 ok，少一条日志。

## 配置

`plugins.configs.codex-timezone.timezone`，IANA 名。

- 选择器 = `ConfigFields` 的 `enum` 字段，`EnumValues` 取 `zones.go` 的精选列表，管理中心渲染成下拉。
- 列表之外的合法 IANA 名可直接写 YAML，插件接受任意能被 `time.LoadLocation` 解析的名字。
- 留空 = 不改写（插件仍注册）。
- 名字先过 `[A-Za-z0-9_+/-]{1,80}`，因为它会写进 prompt；`Local` 被拒绝，否则会解析成宿主所在时区。
- 嵌入 `time/tzdata`，宿主没有系统 zoneinfo（如 Windows）也能解析。
- 可选 `country`（ISO 3166-1 alpha-2，写入时转大写）、`region`、`city`，只用于 `web_search` 工具的 `user_location`。Go 内嵌的 tzdata 不含 `zone.tab`，国家和地区无法由时区推出，所以不猜，只写配置了的值；`city` 留空取时区名最后一段（下划线换空格）。`region`、`city` 会进工具参数，先过 `[\p{L}\p{N} .,'()-]{1,80}`；非法值和非法时区一样让配置被拒绝。

**配置被拒绝时的宿主行为**：`plugin.reconfigure` 返回错误后，宿主把插件标记为未注册（`registered:false`、`effective_enabled:false`），配置文件里仍保留非法值，直到保存一个合法值才恢复。插件内部保留上一个有效时区，但宿主不会再调用它。这是在沙箱宿主里实测的结果，不是推断。

## 改写规则（`rewrite.go`）

日期、缩写、偏移每次请求用 tz 库按当前时刻现算，不缓存，所以跨午夜和夏令时切换都正确。

时间上下文块只在顶层 `input` 数组里识别（宿主翻译后的 Codex 请求就是这个形态）。识别规则对齐 codex-proxy-rs 的请求编码器，宁可漏改也不改到用户贴出来的示例文本。`assistant`、工具输出不碰；`tools` 只有下面的 `web_search` 一项会改。

1. **识别到时间上下文块**：只就地改块的直接子元素 `<timezone>`、`<current_date>`，块内其他元素和文字保留，缺的字段不追加，不注入任何文本。自闭合 `<timezone/>` 会展开成完整元素。识别条件（`timecontext.go`）：
   - 角色绑定块：`user` 认 `<environment_context>`；`developer` 认 `<codex_apps_client_time_context>`。Desktop 把客户端本地时区放在独立的 developer 消息里，只改 user 消息模型会同时看到两份时区。
   - `content` 是数组，片段 `type` 为 `input_text` 且 `text` 是字符串。
   - 整段文本去首尾空白后恰好是一个 `<tag>…</tag>`，能被 XML 解析，根元素名一致（区分大小写）。前后夹带文字、非法 XML、同一字符串里多个块、嵌套在其他元素里的字段都不改。多个块分散在不同片段或消息里则各自识别。
   - 消息带 `internal_chat_message_metadata_passthrough.content_item_kinds` 时它说了算：同下标的值必须等于该块的类别（`environments.environment_context` 或 `additional_content.codex_apps_client_time_context`）。显式标成 `user.text`、空数组、未知类别都不改。没带则只按角色和整块形态识别（自定义 Provider 会去掉这个标注）。
2. **没识别到任何块**（含上述各种不改的情形）：按形态注入带 `[CODEX_TIMEZONE_V1]` 标记的上下文。

   | 形态 | 动作 |
   | --- | --- |
   | `instructions` 是字符串 | 末尾追加（空串则直接替换） |
   | `instructions` 为 `null`，或缺失且 `input` 是数组 | 设为上下文 |
   | 缺失且 `messages` 是数组 | 头部插入 `developer` 消息 |
   | `instructions` 是其他类型 | 不动，避免与未知指令源冲突 |

3. **`web_search` 位置**：`tools` 里 `type` 为 `web_search` 或以 `web_search_` 开头的工具，`user_location` 整体替换为 `{"type":"approximate","timezone":…}`，再加上配置了的 `country`、`region`、`city`（`city` 缺省取时区名最后一段）。整体替换，所以原请求里的旧国家、城市不会残留。其他工具不动。
4. **幂等**：`instructions`、`system` 或 `system`/`developer`/`user` 消息的文本里已含标记则不再注入；已识别的块已是目标值、`user_location` 已是目标值则视为无变化。无变化与跳过都返回空 Body。

用 `sjson` 按路径原地改字段，其余字节（键顺序、其他字段）保持不变。

## 已知限制

- 模型是否采信注入文本是行为假设，没有实测数据。
- 块的字段集、`content_item_kinds` 的取值只依据原版和 codex-proxy-rs 的测试夹具，以及用户贴出的片段，未用真实 Codex 客户端抓包验证。
- 只扫描 `input`。块若出现在 `instructions`、`system`、`messages` 里，不会被改写，而是按"没识别到"注入标记文本，模型可能同时看到旧时区和新时区。
- `web_search` 的 `user_location` 是否被宿主原样转发、上游是否采信，没验证；`country`、`region` 只能手填。
- 管理中心对 50 项 enum 的渲染效果未验证（沙箱里关了面板，只验证了 `GET /plugins` 返回 `config_fields`）。
- `instructions` 注入依赖宿主 Codex 执行器保留非空 `instructions`；已读源码确认非 SIWC 路径只会补空串，SIWC 路径只删空串。
- 被改写的字符串经 `sjson` 重新编码，其中的 `<`、`>`、`&` 会变成 `<` 等转义。JSON 语义不变，只有被改写字符串的字节会变，其余字段字节不变。
- 性能（本机 arm64 实测）：4MB 请求体约 28 到 46 ms。若几百条消息都带环境块，耗时随改写次数平方增长（200 条、4MB 约 258 ms、1.6 GB 分配），因为 `sjson` 每改一处都复制整个 body。真实 Codex 通常只有少量环境块，暂不处理；需要时改成按 gjson 偏移一次性拼接。

## 构建与发布

- 本机：`make build` 产出 `bin/codex-timezone.<so|dylib|dll>`；`make check` = vet + test。
- 版本：`main.version` 默认 `0.1.0`，发布时由 `-ldflags "-X main.version=<tag 去 v>"` 覆盖。宿主要求版本以数字开头且不带 `v`。
- CI：`.github/workflows/ci.yml` 跑 `make check` + `make build`。
- 发布：`release.yml` 在 `v*` tag 上于各原生 runner 构建（cgo 不便交叉编译），打包为 `codex-timezone_<version>_<goos>_<goarch>.zip`（库在 zip 根）并生成 `checksums.txt`，用 `gh release create` 发布。Action 均固定到提交 SHA。
- 商店安装：根目录 `registry.json`（`schema_version: 1`，`github-release` 类型）是第三方商店源，用户在 `plugins.store-sources` 配置其 raw URL。宿主据 `repository` 取最新 Release，版本取自 tag，所以 registry 不写 `version`，发版无需改它。宿主要求 zip 名为 `<id>_<version>_<goos>_<goarch>.zip`、库文件名为 `<id>.<ext>` 且在 zip 根、Release 里有 `checksums.txt`（读 v8.0.35 `internal/pluginstore` 确认），`release.yml` 已按此打包。`registry.json` 的 `id` 必须等于 `pluginID`，`TestRegistryMatchesPlugin` 守护这一点。收录进官方商店需向 `router-for-me/CLIProxyAPI-Plugins-Store` 提交同样的条目，未做。
- SDK 依赖固定 `CLIProxyAPI/v8 v8.0.35`。官方示例 `go.mod` 仍写 `/v7`，但源码 import 与根模块都是 `/v8`，以后者为准。
