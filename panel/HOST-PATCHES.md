# 合并进 workbuddy2api 时对面板源码的改动

本目录（`panel/`）由 `git subtree` 引入自
[`287775856/workbuddy2api-gui`](https://github.com/287775856/workbuddy2api-gui)。

上次同步：面板上游 `9413e70`（2026-09-18，实为 2026-09-19 拉取）。
当前须保留的改动为第 1–3 条与第 5 条；第 4 条已撤销（原前提失效，见该节）。

为了让它与网关**同进程、同端口**运行，合并时改动了下面几处面板源码。
这些文件上游也会改，因此**每次 `git subtree pull` 后都需要重新确认**。
（纯新增的文件不在此列——它们不冲突，见文末。）

拉取上游后建议先跑：

```bash
go build ./... workbuddy2api-gui/... && go test ./... workbuddy2api-gui/...
```

## 必须保留的改动

### 1. `internal/authstore/preserve.go` + `store.go`（数据丢失修复，**最高优先级**）

**症状**：在面板点一次账号「刷新」，该账号凭证里的 `auth.realm` 与顶层
`device_token` 会被从磁盘上抹掉。

**原因**：面板的 `Store.Save` 是从自己的 `Account` struct 重建整份文档再覆盖文件，
而该 struct 不建模这两个键；网关的 `SaveAtomic`（`internal/auth/auth.go`）会写它们。

**影响**：`realm` 可由网关从 `domain` 反推自愈（`BackfillRealm`）；
`device_token` **不可反推**，丢失即永久失去该账号的 `X-Device-Token` 风控头。

**修法**：新增 `preserve.go`（`readExistingDoc` + `mergeMissingKeys`），在 `Save`
写盘前把磁盘上未建模的键合并进来。采用「保留未知键」而非「补两个硬编码字段」，
以便上游今后新增键时同样不丢。

**回归测试**：`internal/authstore/preserve_test.go`（`TestSavePreservesUnmodeledKeys`）。

### 2. `internal/ops/loginflow.go`（提示语）

`PollLogin` 落盘后的文案原为「请手动重启网关使其加载」等分支。合并后网关有目录
热加载（`cmd/server/reload.go`），数秒内自动入池，故改为
「凭证已保存，网关将在数秒内自动加载（无需重启）」。
重启分支保留，供面板被单独部署 / 网关为旧版本时回退使用。

### 3. `internal/api/server.go`（两处提示语）

- `handleAccountDelete`：`"凭证文件已删除，重启网关后该账号移出账号池"`
  → `"…网关将在数秒内将该账号移出账号池（无需重启）"`。
- `handleConfigPut`：`"…（系统页可一键重启）"` → `"…需重启进程才会生效"`。
  配置确实**不能**热重载（网关只在启动时读 `config.json`，无 SIGHUP / 文件监听），
  故保留「需重启」的准确说法。

### 4. `web/src/App.tsx`（**已撤销**：统计页现已启用）

原先新增 `STATS_ENABLED = false` 常量过滤导航项与路由，理由是「该页依赖网关的
`/v1/stats` 端点，而本上游只注册了 4 条路由」。

**该前提已失效**：上游 PR #161 在面板引入后 34 分钟就补上了 `/v1/stats`
（`internal/server/handler.go`，提交 `733d348`）。2026-09-19 同步面板上游
`9413e70` 时一并撤销此开关，`App.tsx` 已与上游逐字一致。

> 注意区分两种能力：`/v1/stats` 的**累计快照**（按模型聚合）上游已实现，统计页
> 的这部分可用；而面板 `9413e70` 新增的「时间趋势」卡片依赖 `/v1/stats` 的
> **时间维度参数**（`range`/`from`/`to`/`interval`/`model` 与响应的 `range`、
> `series_buckets` 字段），上游网关（`b08f518`）**尚未实现**。故趋势图当前会停在
> 「正在加载趋势数据」（面板作者预留的降级分支），其余功能不受影响。网关侧补齐
> 后该卡片自动开始工作，无需再改面板。

### 5. `internal/webui/dist/index.html`（前端产物）

上游仓库里这份占位 `index.html` 引用的 `assets/index-*.js` 被 gitignore，
**从干净 clone 构建会渲染空白页**（`webui.Handler` 只在 index.html *不可读*时
才回退到「前端未构建」提示页，而这里是可读但资源缺失）。

合并后的 `Dockerfile` 第一个阶段（`node:20-alpine`）会重新构建并覆盖它，
故这份文件的内容不重要；**但不要删除它**——`//go:embed all:dist` 需要目录非空。

`9413e70` 同步时上游更新了此文件引用的 hash（`index-BQ3iX5L-` → `index-BEIjNl4j`）。
已在 `panel/web/` 跑 `npm run build` 重新生成，产物 hash 与引用一致；源码模式下
（不经 Dockerfile）也能正常渲染。

## 纯新增、不会冲突的文件

| 文件 | 作用 |
|---|---|
| `host/host.go` | 面板装配包。**刻意不含 `internal` 段**，宿主导入合法（Go 的 internal 规则不允许根 module 直接 import `workbuddy2api-gui/internal/*`）。 |
| `internal/authstore/preserve.go` | 见上。 |
| `internal/authstore/preserve_test.go` | 见上。 |
| `HOST-PATCHES.md` | 本文件。 |

宿主的对应文件在仓库根：`cmd/server/panel.go`（路由合并）、`cmd/server/reload.go`
（账号热加载）。它们不属于 subtree，不受 pull 影响。
