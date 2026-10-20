# Codex Timezone Sync 原理分析与复现指南

> 分析对象：<https://github.com/LonelyHerbivore/codex-timezone-sync>（v0.5.1，3 个 commit）
> 分析方式：只读源码（`plugin.c`、`timezone_normalize.inc`、`refresh_bridge.inc`、`settings.inc`、`model_monitor.inc`、`refresh_egress.py`、`test_plugin.py`），**没有编译，也没有运行**。
> 文中标注「推断」的地方是我从代码和测试反推的，不是仓库明说的。

## 0. 先说结论

它**不修改系统时区**，也不改 HTTP 头、浏览器指纹或 OAuth 登录地。它做的事只有一件：

> 在 Codex 请求发往上游之前，把请求 JSON 里「模型能看到的日期和时区文本」改成代理出口 IP 所在地的值。

这样模型自报的"今天几号 / 我在哪个时区"和你的出口 IP 地理位置一致，而不是和你本机时钟一致。

拆开看是三个独立的小部件：

| 部件 | 职责 | 原版实现 |
| --- | --- | --- |
| 出口探测器 | 经代理查出口 IP → IANA 时区 → 算出日期/缩写/偏移，落盘 | `refresh_egress.py` |
| 请求改写器 | 读状态，改写请求体里的时区和日期文本 | `plugin.c` + `timezone_normalize.inc` |
| 管理面（可选） | 开关、统计、失败日志、模型一致性监控、手动刷新 | `*.inc` + `dashboard.html` |

**复现的核心是前两个，加起来逻辑不到 200 行（用带 JSON 库的语言）。** 原版 2500 行里大部分是 C 语言下手写 JSON 解析、base64、跨平台兼容和管理面。

## 1. 整体架构

```text
 Codex 客户端
     │  请求（OpenAI Responses / Chat 格式）
     ▼
┌──────────────────────────────────────────────┐
│ CLIProxyAPI (CPA) 宿主                       │
│   格式转换 → ToFormat="codex"                │
│        │                                     │
│        ▼  request.intercept_after            │
│  ┌───────────────────────────────┐           │
│  │ 插件 .so/.dylib/.dll (C ABI v1)│          │
│  │  1. 读状态文件（≤2h 才可信）   │◀── 状态文件│
│  │  2. 改写 Body 里的时区/日期    │    ▲      │
│  └───────────────────────────────┘    │      │
└───────────────────────────────────────│──────┘
                                        │ 原子写入
                          ┌─────────────┴─────────────┐
                          │ refresh_egress.py (宿主侧) │
                          │  经同一个代理访问           │
                          │  ipinfo.io/json            │
                          └─────────────┬─────────────┘
                                        ▼
                                 代理 (Mihomo :7890)
```

关键设计：**探测器和改写器通过一个小文本文件解耦**，改写器在热路径上只做本地文件读取，不碰网络。

## 2. 数据流（一次请求）

1. 定时任务（建议 ≤30 分钟）运行探测器，状态文件更新。
2. Codex 请求到达 CPA，CPA 把它转成 Codex 上游格式，调用插件的 `request.intercept_after`。
3. 插件检查 `ToFormat == "codex"`，不是就原样放行。
4. 插件 base64 解码 `Body`，读取状态文件，校验新鲜度和字段格式。
5. 改写（见第 4 节），重新 base64 编码，返回 `{"ok":true,"result":{"Body":"..."}}`。
6. 任何一步失败，返回 `{"ok":true,"result":{}}`，宿主用原请求继续（fail-open）。

## 3. 出口探测器（`refresh_egress.py`）

回答一个问题：我现在的出口 IP 在哪个时区。

1. 选代理：优先读环境变量 `CODEX_TZ_PROXY`；没设就 `docker inspect` 名为 `mihomo` 的容器在 `cpa-proxy-net` 网络里的 IP，拼成 `http://<ip>:7890`。后者是作者自己的部署假设，复现时应去掉。
2. 经该代理请求 `https://ipinfo.io/json`，12 秒超时。**必须和 Codex 走同一个代理**，否则测到的是另一条出口。
3. 校验：`ip` 过 `ipaddress.ip_address`；`timezone` 过正则 `[A-Za-z0-9_+/-]{1,79}`。
4. 用 `ZoneInfo(zone)` 取此刻的本地日期、缩写（`PDT`）、偏移（`-07:00`）。
5. 原子写状态文件：同目录 `mkstemp` → 写 → `fsync` → `chmod 644` → `os.replace`。失败时旧文件原封不动（last-good-state）。

脚本自述「从不请求 OpenAI，也不读 OAuth 凭据」，只访问地理 IP 服务。

### 状态文件格式

两行 ASCII：

```text
America/Los_Angeles|2026-10-11|PDT|-07:00
66.92.223.4
```

第一行 `zone|date|abbr|offset`，第二行是出口 IP。**新鲜度看文件 mtime，不看内容里的日期。**

### 按需刷新通道（只服务于面板的"立即检测"按钮）

- Unix：`refresh_egress.py --serve` 监听 Unix socket（权限 0600）。插件连上后发 `REFRESH\n`，helper 跑一次 refresh 并回 `OK\n` 或 `ERR\n`。插件 25 秒超时，成功后重读状态文件。
- Windows：Python 没有 `AF_UNIX`，改用文件邮箱。插件写 `<sock>.request`（内容是 16 位十六进制 nonce），helper 每 0.2 秒轮询，处理完写 `<sock>.<nonce>.reply`；helper 存活期间持续 `touch <sock>.ready`。

源码注释说明这样设计是为了让「网络凭据不穿过 CPA 公开的插件资源路由」。如果你的探测和改写在同一进程里，这一层整个可以删掉。

## 4. 请求改写器（`plugin.c` + `timezone_normalize.inc`）

### 4.1 前置检查，任何一步失败都放行原请求

- 请求信封的 `ToFormat` 必须是 `codex`。
- 启用开关 `enabled` 为真（每次请求现读配置文件，保存后立即生效）。
- `Body` 能 base64 解码，且是 JSON 对象，不超过 12 MiB、15 万个 token。
- 状态文件存在，mtime 距今 ≤ 7200 秒（且不在未来 60 秒以外），第一行四个字段都通过字符白名单。

两小时的阈值是刻意的：代理节点切换后，旧状态会让插件"自信地"声明一个错误的时区，所以过期就不改，宁可不改也不改错。

### 4.2 日期不直接用文件里的，而是现算

`derive_local_date` 取「当前 UTC 时间 + 状态文件里的偏移」得到本地日期。这样两次探测之间跨过午夜，日期也不会滞后一天（对应测试 `test_state_date_is_not_stale_after_midnight`）。

注意它用的是**固定偏移**，没有时区库。两次探测之间发生夏令时切换，最多会有 1 小时偏差（我从代码推断，没有实测）。

### 4.3 注入的文本

```text
[CPAMC_TIMEZONE_V1]
Current date: 2026-10-11
Current timezone: America/Los_Angeles (PDT, UTC-07:00).
Use this date/timezone for environment context unless the user explicitly specifies another timezone.
```

首行的 `[CPAMC_TIMEZONE_V1]` 是幂等标记。

### 4.4 改写决策树（`rewrite_payload`）

先看请求里有没有已经存在的 `<environment_context>` 块（Codex 会把它放进对话，测试夹具里可见 `cwd`、`current_date`、`timezone` 等字段）：

1. **有环境块** → 只就地改块里的 `<timezone>` 和 `<current_date>`，**不再注入标记文本**。
2. **没有环境块**，按请求形态注入：

| 请求形态 | 动作 |
| --- | --- |
| `instructions` 是字符串，且不含标记 | 在字符串末尾追加 `\n\n` + 上下文 |
| `instructions` 是 `null` | 把 `null` 换成上下文字符串 |
| `instructions` 是其他类型 | 什么都不做（避免重复指令） |
| 无 `instructions`，`input` 是数组（Responses 格式） | 在顶层对象开头插入 `"instructions":"…",` |
| 无 `instructions`，`messages` 是数组（Chat 格式） | 在数组头部插入一条 `developer` 消息；数组非空才补逗号 |

### 4.5 环境块的就地改写

扫描范围（只扫这些位置，其余原样保留）：

- 顶层 `instructions`、`system`：字符串，或 `type` 为 `text`/`input_text` 的内容数组。
- 顶层 `input`：字符串；或消息数组。
- 顶层 `messages`：消息数组。
- 消息只处理 `role` 为 `system`、`developer`、`user` 的；`assistant` 和 `function_call_output` 不碰。

测试 `test_does_not_modify_assistant_or_tool_output` 专门保证：工具输出或助手回复里恰好出现的 `<environment_context>` 不会被改。

对每个命中的字符串：

1. JSON 反转义成原文（处理 `\uXXXX`、代理对，拒绝 `\u0000`）。
2. 找每个 `<environment_context ...>...</environment_context>`，标签名大小写不敏感，支持多个块。
3. 块内对 `timezone`、`current_date` 两个字段：
   - 有 `<f>…</f>`：替换内容，同名多处全换。
   - 只有自闭合 `<f/>`：展开成完整元素。
   - 没有：在 `</environment_context` 前插入 `<f>值</f>\n`。
4. 重新 JSON 转义，**按字节偏移拼回原始请求体**；多处修改按位置从后往前处理，避免偏移失效。

### 4.6 为什么手写 JSON 令牌化再拼接，而不是解析后重序列化

`parse_json` 只生成「开始/结束偏移 + 类型」的令牌，改写靠 `splice` 在原字节上替换。结果是除被改的字符串外，其余字节（键顺序、空白、数字写法、Unicode 转义）完全不变。这是我的推断：作者想把对上游请求的扰动压到最小。在 C 里这是必选项（没有 JSON 库），在有 JSON 库的语言里这是可选项，见第 8 节。

## 5. 管理面（可选，和时区改写无关）

原版把以下功能都塞进了同一个动态库。复现时可以全部砍掉，只留第 3、4 节。

| 功能 | 做法 |
| --- | --- |
| 开关 | `enabled/logging/debug` 三个布尔值，存 `key=value` 文本，`mkstemp` + `rename` 原子写，每次请求现读。PUT 时拒绝未知键和非布尔值 |
| 统计 | 成功/失败两个持久计数器 |
| 活动日志 | 记录 `rewritten`/`skipped`、时间、改前时区、改后时区，不存请求正文 |
| 失败日志 | **仅在改写失败时**把解码后的完整请求体存盘（权限 0600），面板可查看。README 自己提醒这可能含 prompt 和密钥 |
| 模型一致性监控 | 在响应、SSE 分片、WebSocket 事件里读 `model` 字段，与请求的 `RequestedModel` 比对，得出 matched/mismatched/unknown。只读，纯粹是另一个功能 |
| 自检 | 拿一个 `{"input":[]}` 做干跑，确认能产出标记和当前时区 |
| 面板 | 单文件 HTML，构建时由 `embed_ui.py` 转成 C 字节数组嵌进库里 |

模型一致性监控的测试里有一条值得借鉴：`test_model_monitor_never_reads_fake_model_from_tool_output`。只在响应对象的固定位置（`response.model`、`message.model`，或带 `type`/`object` 的顶层 `model`）取值，不从工具输出文本里找，防止被内容伪造。

## 6. 安全与健壮性设计

值得保留的做法：

- **fail-open**：状态缺失、过期、请求体异常、编码失败，一律返回空结果让宿主用原请求。
- **字符白名单**：时区、缩写、偏移、IP 在读入和写出时都按字符集校验，防止状态文件被污染后注入到 prompt 里。
- **幂等**：标记 `[CPAMC_TIMEZONE_V1]`，加上"环境块内容已等于目标值就不动"，同一请求重复经过不会叠加。
- **角色白名单**：只改 `system`/`developer`/`user`，不碰 `assistant` 和工具输出。
- **文件安全**：日志用 `O_NOFOLLOW` 防符号链接读取，状态和配置原子替换。
- **面板不放凭据**：源码注释写明 CPA 的菜单资源路由不鉴权；管理 API 由宿主用 management key 保护。

## 7. 我读源码时发现的问题（推断，未运行验证）

复现时建议避开：

1. **"已经是目标值"被记成失败。** `normalize_existing_environments` 在环境块已匹配时返回 2，`rewrite_payload` 对 2 和对错误一样返回 `NULL`。`intercept` 里只有请求含 `[CPAMC_TIMEZONE_V1]` 才跳过失败记录，而环境块路径从不写这个标记。推断后果：本机时区本来就等于出口时区时，每个请求都会计一次失败，并把完整请求体写进失败日志。测试 `test_replace_existing_environment_and_date` 只断言第二次调用返回空，没检查计数器，所以没暴露。**重构时改写结果应该是三态：`Changed` / `AlreadyOk` / `Skipped(reason)`。**
2. **夏令时偏差。** 日期用固定 UTC 偏移推算，两次探测之间跨夏令时切换会差一小时，跨午夜附近日期可能错一天。有 tz 数据库的语言直接用 `ZoneInfo`/`chrono-tz` 即可，探测只需要存时区名。
3. **令牌化器偏宽松。** `parse_json` 以 `t`/`f`/`n`/数字开头就当原始值吃到分隔符，不校验字面量。有 JSON 库就不存在这个问题。
4. **探测依赖第三方。** `ipinfo.io` 不可用就整条链路停摆，并且 2 小时后进入不改写状态。可以做多源兜底。
5. **覆盖面有限。** 只改文本层的日期/时区。HTTP 头、工具返回的时间、沙箱里的 `TZ` 环境变量都不在范围内。README 也明确说不改这些。

## 8. 重构成自己的实现

### 8.1 许可：只复现原理，不要抄代码

仓库没有 LICENSE 文件，README 写的是「当前仓库没有明确授予代码再分发许可」。所以应当按本文的原理自己重写，不要复制 `.c`/`.inc`/`.py` 的内容。原理（读文件、改环境块、fail-open）不受版权约束，具体代码受约束。

### 8.2 先决定挂载点（我还没查你们项目）

原版能工作，是因为 CPA 提供了 `request.intercept_after` 钩子，并且已经把请求转成 Codex 格式。你们的实现取决于请求从哪里出去：

| 场景 | 挂载点 | 说明 |
| --- | --- | --- |
| 你们自己发 Codex 请求 | 请求构造处直接带上正确的环境块 | 最简单，根本不需要"改写"，生成时就用出口时区 |
| 经本地代理转发 | 代理层加一个请求中间件 | 和原版同构 |
| 继续用 CPA | 写 CPA 插件 | 必须实现 C ABI v1，语言受限于能导出 C 符号 |

我没有读 EurekaDesktop 里与 Codex 相关的代码，这一步需要你确认实际走哪条路径。

### 8.3 推荐的模块划分

```text
egress     探测：代理 → IP 服务 → 时区名            （异步，带缓存）
clock      计算：时区名 + 当前 UTC → 日期/缩写/偏移  （用 tz 库，每次现算）
rewrite    纯函数：(body: Value, ctx: TzContext) → Outcome
policy     开关、新鲜度阈值、fail-open 决策
audit      可选：计数、活动日志（不存正文）
```

和原版相比的三个主要改动：

1. **状态里只存时区名和 IP，不存日期和偏移。** 日期、缩写、偏移由 `clock` 每次用 tz 库现算，天然解决跨午夜和夏令时问题（第 7 节问题 2）。
2. **探测和改写同进程时，用内存缓存加时间戳**，不需要状态文件、Unix socket、Windows 邮箱。需要跨进程再落盘。
3. **`rewrite` 返回三态**：`Changed(body)`、`AlreadyOk`、`Skipped(reason)`，只有 `Skipped` 才算失败（第 7 节问题 1）。

### 8.4 `rewrite` 的最小行为规格

输入：已解码的请求 JSON 和 `TzContext { zone, date, abbr, offset }`。

1. 遍历 `instructions`、`system`、`input`、`messages`，仅处理 `system`/`developer`/`user` 的文本内容。
2. 找 `<environment_context>` 块（大小写不敏感，允许多个）。有则改 `<timezone>`、`<current_date>`，缺则补；全部已正确则返回 `AlreadyOk`。
3. 一个块都没有则按第 4.4 节的表注入带标记的上下文。
4. 不改 `tools`、`assistant` 消息、工具输出。

用 Rust 的话，`serde_json::Value` 解析后直接改字符串节点即可，不需要手写令牌化。有一点要注意：默认的 `Value` 会重排键顺序，需要开 `serde_json` 的 `preserve_order` feature，否则每次请求的字节都会变。另外 `<` 这类转义在解析时会还原、序列化时不一定再转回，和原版"保留其余字节"的目标略有差异。是否在意字节级保真，取决于上游有没有对请求体做缓存键或签名，这点需要你确认。

### 8.5 测试清单（照 `test_plugin.py` 的断言思路自己写）

- 非 Codex 目标格式原样放行。
- 状态缺失、过期、字段含非法字符、请求体不是 JSON：全部放行且不 panic。
- `instructions` 分别为字符串、`null`、缺失（配 `input` 数组）、缺失（配 `messages` 数组）、空 `messages`。
- 追加后原有 `input` 和非 ASCII 文本不变。
- 改写后再改写一次：结果为 `AlreadyOk`，且**不计失败**。
- 多个环境块；块里缺字段；自闭合字段；标签大小写；JSON 转义的尖括号。
- `assistant` 消息和工具输出里的环境块保持不变。
- 状态里的日期是旧的，输出仍是当前出口本地日期。
- 开关关闭时完全不改。
- 失败日志（如果做）只在 `Skipped` 时写入，且权限收紧。

### 8.6 建议的实施顺序

1. 先做 `clock` 和 `rewrite` 两个纯函数，加上面的单测。它们没有 IO，最容易做对。
2. 再做 `egress`，带超时和多源兜底，返回时区名。
3. 最后接挂载点，加开关和 fail-open。
4. 管理面板、统计、模型一致性监控按需再加，它们和时区功能互相独立。

## 9. 这份分析的边界

- 只读了源码和测试，**没有编译或运行**原插件，所以第 7 节的问题是推断，不是复现出来的结果。
- 没有读 `dashboard.html`、`failures.inc` 的细节、`events.inc`、`stats.inc`，因为它们属于管理面，不影响时区改写原理。
- 没有验证 Codex 实际发送的请求里 `<environment_context>` 的完整字段集，上文只依据测试夹具。
- 「模型会采信这段文字」是一个行为假设，原仓库没有给出实测数据。
