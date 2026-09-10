# NEU Job Finder (Go)

面向 `http://job.neu.edu.cn/campus/index/` 的本地岗位抓取与匹配工具。第一版坚持“公开信息、低频抓取、原文证据、人机分离”：抓取和筛选可以自动化，登录与自动投递不在范围内。

## 已实现

- 解码站点使用的 zlib/Base64 动态列表和公告正文。
- 使用包含起止边界的“发布日期起/止”在本地筛选公告，避免站点搜索入口异常。
- `keyword` 在解码后的公司、正文和岗位字段中执行本地 OR 匹配；单关键词同时用于源站查询。
- 从 `/campus/view/id/{ID}` 解析稳定公告 ID，用于去重与增量更新。
- 详情页提取：单位、岗位、薪资、地点、学历、专业、截止时间、企业投递邮箱、外部网申链接、邮件标题/命名要求。
- 岗位表格采用标签优先和值模式辅助解析；每个岗位保留原始行文本、整体提取质量（`confident`/`fallback`/`ambiguous`）和字段级诊断。
- 学校就业服务邮箱黑名单，避免误识别为企业投递邮箱。
- 网络错误与空响应自动重试；个别详情失败时保留已成功结果。
- 同步结果按有界批次写入本地 JSON，并保存可审计的 crawl run 记录；失败详情保留精确 ID 和原因。
- 可用上次 run 的失败 ID 定向重试，不会重新抓取已经成功且仍在缓存策略内的详情。
- 研究方向、技能、学历、毕业年份、专业、意向城市、意向岗位 **全部可选**。
- 无个人条件时不生成虚假匹配分；有条件时只使用已填写项评分。
- “严格过滤”开关：可隐藏学历、城市、明确毕业届别等硬条件不匹配岗位。
- 本地 JSON 原子持久化，无数据库/Node/第三方 Go 依赖。
- Excel (`.xlsx`)、Markdown、CSV 导出。
- Go 原生 server-rendered UI，适合本地快速运行与后续拆分。

## 运行

要求 Go 1.23+。

```bash
go test ./...
go run ./cmd/server
```

浏览器打开：

```text
http://localhost:8080
```

建议第一次同步使用默认的“最近 30 天至今天”，keyword 留空，点击“开始低频同步”。发布日期起止边界均包含在内；页面会实时显示发现、去重、区间筛选和详情处理进度。

## 命令行参数

```bash
go run ./cmd/server \
  -addr 127.0.0.1:8080 \
  -data data/store.json \
  -base-url http://job.neu.edu.cn \
  -delay 1200ms \
  -max-pages 100 \
  -detail-workers 3 \
  -max-connections 3
```

请不要把 `-delay` 设置得过低。所有列表、详情和重试请求共用一个全局 pacing gate；默认每个源站请求至少间隔约 1.2 秒，详情 worker 数不会乘大请求速率。`-max-connections` 留空时跟随 `-detail-workers`。

## 当前架构

```text
Browser
  ↓
Go net/http + html/template
  ├─ /            岗位列表 / 可选匹配条件
  ├─ /sync        低频增量抓取
  ├─ /api/jobs    JSON API
  ├─ /export.xlsx Excel
  ├─ /export.md   Markdown
  └─ /export.csv  CSV
       ↓
Crawler → bounded detail workers → Parser → Matcher → Local Store
       ↓
job.neu.edu.cn public pages
```

## 数据与匹配原则

### 去重

公告用网站详情页数字 ID 作为主键。岗位在公告内部生成 `{announcementID}:{row}` ID。

### 匹配

第一版不是大模型自由判断，而是：

1. 明确字段（学历、城市、毕业届别）进行可解释硬条件判断；
2. 研究方向、技能、专业、岗位偏好采用岗位级关键词覆盖率和常见技术同义词；
3. 只对用户填写的字段计分；
4. 每个岗位保留匹配证据、硬条件冲突和待核实项；
5. 原始详情页 URL 始终保留，便于人工复核。

后续可将 `internal/matcher` 替换为 embedding / reranker，而不改抓取和存储层。

### Boolean 查询兼容映射

岗位匹配支持 `Profile.Query`：`must`、`should`、`must_not` 都是 term group 的数组。一个 group 内的 term 是 OR；多个 `must` group 之间是 AND。`minimum_should_match` 表示至少命中的 `should` group 数量；`must_not` 任一命中即排除，不能被正向命中抵消。岗位证据仍遵循 position-level provenance 隔离。

例如 `A AND (B OR C)` 可以写成：

```json
{
  "must": [["冶金", "钢铁"], ["人工智能", "机器学习"]],
  "must_not": [["销售", "行政"]]
}
```

为了兼容第一版，旧的 `roles`、`skills`、`research`、`major` 字段会分别映射为可选 `should` group，`minimum_should_match=0`，因此不会把旧 Profile 变成强制过滤条件。学历、城市和毕业年份仍由独立 hard-condition 逻辑处理。

### 结果控制与排名

布尔 eligibility 通过后才进入评分。语义字段按命中证据饱和计分，未命中的可选语义字段只保留很小的排名影响，避免填写更多技能后把明确相关岗位机械稀释。`min_score` 在评分后过滤，`top_n` 在排序后截取；两者都不会删除或减少本地原始公告。

排名顺序固定为：`score DESC`、源站 `PublishedDate DESC`、岗位稳定 ID 升序。`LastSeenAt` 只表示本地抓取时间，不参与岗位新鲜度排名。

## 已知边界

- 网站压缩方式或 HTML 若改版，`internal/crawler` 的解码与解析器需要更新。
- 图片/附件中的招聘条件目前不 OCR；建议后续作为独立 extractor 接入。
- 外部网申页不会继续自动抓取，避免把第一版变成不受控的跨站爬虫。
- 企业邮箱通过“先过滤学校服务邮箱，再取正文候选邮箱”的启发式提取；最终投递前应人工核对原文。
- 公告根据列表中的发布时间本地过滤；请求区间是包含起止日期的闭区间，详情页发布日期还会进行第二次校验。只有整页条目都有日期且全部早于“发布日期起”时才会停止；混合旧、无日期、新条目的页面会继续扫描，避免漏掉同页的有效公告。
- 抓取关键词与岗位 Boolean 匹配分开处理：单个关键词会作为 `keyword` 传给源站；多个以空格、逗号、分号、顿号或 `|` 分隔的关键词不会拼成源站表达式，而是在详情的公司、正文和岗位字段中执行本地 OR 匹配。
- 每次同步的进度包含 `EntriesSeen`、`UniqueIDs`、`InRangeIDs`、`DuplicateIDs`、`UndatedIDs`、`FilteredIDs`、`FailedIDs` 和 `AcceptedIDs`，用于核对发现与结果数量。
- 缺失、乱序或多余的岗位元数据不会仅按列序静默填入；无法确认的字段保持为空并标记为 `ambiguous`。matcher 会跳过这类字段的 verified 证据，同时保留原始详情 URL 和岗位行文本供人工核对。

## 下一阶段建议

- SQLite 存储与迁移（数据量变大后）。
- 公告附件/PDF/图片识别队列。
- 外部网申链接白名单式二跳提取。
- 增量游标与更长周期的运行归档。
- 保存多个匹配 Profile。
- 每日定时同步后只推送“新增且高匹配”的岗位。

## 纯命令行增量同步

无需启动 Web 页面：

```bash
go run ./cmd/sync
```

默认抓取“今天往前 30 天至今天”；也可指定包含边界的起止日期：

```bash
go run ./cmd/sync -start 2026-09-03 -end 2026-09-30 -keyword 冶金
```

`-keyword` 只有单个词时会传给源站；多个关键词在本地 OR 匹配。该命令适合后续接入 Windows Task Scheduler 或 cron。

出现部分详情失败时，可用 `-retry-ids` 传入失败公告 ID（逗号或空格分隔）进行定向重试：

```bash
go run ./cmd/sync -start 2026-09-03 -end 2026-09-30 -retry-ids 561451,561249
```

每次同步会生成 `run_id`，并在 `data/store.json` 的 `runs` 数组中保存请求日期/关键词、发现与详情计数、失败 ID/原因、终态和取消标记。旧版只有 `announcements` 的 JSON 文件仍可直接读取；公告和终态 run 会通过原子批次保存。

## 详情增量策略

默认详情刷新窗口为 7 天。新发现的公告一定抓取；已缓存且源站发布日期较旧的公告默认跳过；近期公告在距离上次成功抓取超过刷新窗口后再次抓取；没有可靠发布日期的缓存项按同一窗口周期检查。`-force-refresh`（Web 中的“强制刷新已缓存详情”）会显式重新抓取当前范围内的所有缓存公告。

刷新不会用抓取时间替代源站 `PublishedDate`，本地 store 会保留 `FirstSeenAt`。命令行输出和 Web 同步流会记录 `new`、`refreshed`、`skipped_cached`、`details_attempted`、`details_succeeded` 和失败明细。Web 流使用有界批次 collector，避免每条公告都重写整个 JSON 文件。
