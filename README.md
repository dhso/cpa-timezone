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

填错的名字会让宿主把插件标为未注册，直到保存一个合法值，详见 [docs/design.md](docs/design.md)。

## 行为

- 请求里已有 `<environment_context>`：只改其中的 `<timezone>` 和 `<current_date>`。
- 没有：在 `instructions`（或 `messages` 头部）注入一段带 `[CODEX_TIMEZONE_V1]` 标记的说明，重复经过不会叠加。
- 日期和夏令时每次请求现算。
- 任何异常都放行原请求。

## 开发

```bash
make check   # go vet + go test
make build   # bin/codex-timezone.<so|dylib|dll>
```

需要 Go 1.26+ 和 C 编译器（cgo）。

## 许可

MIT。原理参考 [LonelyHerbivore/codex-timezone-sync](https://github.com/LonelyHerbivore/codex-timezone-sync) 的分析，未使用其代码。
