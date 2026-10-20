# codex-timezone

CLIProxyAPI 插件。把发往 Codex 的请求里的日期和时区文本改成你指定的时区，让模型自报的"今天几号 / 所在时区"与之一致。只改请求体文本，不改系统时区、HTTP 头或工具返回。

## 安装

### 从插件商店安装

1. 把本仓库的 registry 加为第三方商店源（官方源始终保留）：

```yaml
plugins:
  enabled: true
  store-sources:
    - "https://raw.githubusercontent.com/dhso/cpa-timezone/main/registry.json"
```

2. 在管理中心的插件商店搜索 `codex-timezone` 并安装。宿主必须是 CGO 构建。
3. 在 CPA 配置里启用并选择时区：

```yaml
plugins:
  configs:
    codex-timezone:
      enabled: true
      priority: 1
      timezone: "Asia/Tokyo"
```

4. 访问 `GET /v0/management/plugins`，确认 `registered` 和 `effective_enabled` 都是 `true`。

### 手动安装

从 [Releases](https://github.com/dhso/cpa-timezone/releases) 下载对应平台的 zip，解压出库文件放进 `plugins/<GOOS>/<GOARCH>/` 或 `plugins/`，其余步骤同上。

## 选择时区

在管理中心的插件配置里，`timezone` 是一个下拉，包含约 50 个常用 IANA 时区。列表里没有的合法 IANA 名（如 `Asia/Kathmandu`）可以直接写进 YAML。留空则不改写。

`country`（两位国家码，如 `JP`）、`region`、`city` 是可选项，只写进 `web_search` 的 `user_location`。时区推不出国家和地区，所以不会替你猜；`city` 留空取时区名最后一段。

填错的名字会让宿主把插件标为未注册，直到保存一个合法值，详见 [docs/design.md](docs/design.md)。

## 行为

- `input` 里 user 消息的 `<environment_context>` 和 developer 消息的 `<codex_apps_client_time_context>`（Desktop 的客户端时区）：只改其中的 `<timezone>` 和 `<current_date>`。只认整段就是一个完整块的文本，正文里引用的示例、非法 XML、被标成普通文本的内容都不改。
- 没认出任何块：在 `instructions` 注入一段带 `[CODEX_TIMEZONE_V1]` 标记的说明，重复经过不会叠加。
- `web_search` 工具的 `user_location` 改成配置的时区和位置。
- 模型调用 `date` 之类的工具查询本机时，工具结果仍是真实系统时区，插件不会改客户端操作系统。
- 日期和夏令时每次请求现算。
- 任何异常都放行原请求。

## 开发

```bash
make check   # go vet + go test
make build   # bin/codex-timezone.<so|dylib|dll>
```

需要 Go 1.26+ 和 C 编译器（cgo）。

## 许可

MIT。原理参考 [LonelyHerbivore/codex-timezone-sync](https://github.com/LonelyHerbivore/codex-timezone-sync) 的分析，未使用其代码。时间上下文的识别规则参考 [zyycn/codex-proxy-rs](https://github.com/zyycn/codex-proxy-rs)（Apache-2.0），见 [NOTICE](NOTICE)。
