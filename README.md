# jonus

本地 AI 音乐创作工作室：**给一句描述，出一首真歌**。

同一个引擎有两个入口——命令行和浏览器里的 Web UI。两者走的是同一套队列、持久化和进度事件，所以你在哪边都看得到同一首歌、同一个进度。

```
$ jonus generate "轻快的民谣，木吉他扫弦，午后阳光" --wait
```

---

## 快速开始

### 1. 构建

```bash
make build
```

需要 **Go 1.27+**，以及构建前端需要的 **Node.js 18+**。`make build` 会先编译 Svelte 前端，把它嵌进 Go 二进制，再编译出 `bin/jonus`。产物是单文件，没有运行时依赖。

网络受限的话，先把 Go 模块代理指到国内镜像：

```bash
make goenv          # 等价于 go env -w GOPROXY=https://goproxy.cn,direct
```

### 2. 先跑起来看看

不配任何模型也能用。默认的 `mock` 引擎在本地合成音频，让你在下载任何模型之前就能把整条链路——排队、进度、曲库、播放——走一遍。

```bash
./bin/jonus doctor                          # 看看这台机器准备好了没有
./bin/jonus generate "随便什么" --wait        # 出一段本地合成的音频
./bin/jonus serve --open                    # 浏览器打开 Web UI
```

mock 出的是一段编排简单、但真实可解码的音频，不是歌。它的用途是验证系统能跑通，不是听。

### 3. 出真歌

真歌来自托管在云的模型。你只需要配一个 API Key：

```bash
export JONUS_CLOUD_API_KEY=你的key
```

**设了它，引擎自动切成云端**，不用再加 `--engine cloud`。理由是：API Key 只会被人刻意设置，而它唯一的用途就是云引擎。一个配好了 key 的安装却回你一段合成音频，那读起来像 bug。

```bash
./bin/jonus doctor                                    # 验 key 和网关
./bin/jonus generate "轻快的民谣，木吉他扫弦" --wait      # 第一首真歌
```

Key 从哪来、怎么开模型权限，见下面的[云引擎](#云引擎出真歌)。

---

## 引擎

jonus 把生成能力放在一个可插拔的接口后面。选哪个引擎只影响"歌从哪来"，排队、进度、曲库、播放这些行为完全一样。

| 引擎 | 名称 | 需要什么 | 出什么 |
|---|---|---|---|
| 本地合成 | `mock` | 什么都不需要 | 合成的测试音频（默认） |
| 云端 | `cloud` | 一个 API Key | **带人声的真歌** |
| 本地模型 | `ace-step` | Python 3.12 + 约 10GB 权重 | 真歌（**尚未接线**，见下） |

选择方式，优先级从低到高：

1. 默认 `mock`
2. `JONUS_CLOUD_API_KEY` 已设置 → 自动 `cloud`
3. `JONUS_ENGINE=mock|cloud|ace-step`
4. `--engine mock|cloud|ace-step`

> **关于 `ace-step`**：架构里为它留了位置（`internal/engine/ace` + Python sidecar），但当前版本没有接线，选它会直接报错并提示改用 `mock`。现在要出真歌请用 `cloud`。

---

## 云引擎（出真歌）

### 选的是谁

云引擎经**腾讯云 TokenHub** 转发到两家音乐模型：

| provider | 默认模型 | 特点 |
|---|---|---|
| `minimax` | `minimax-music-v3.0` | **自己写词自己唱**；不给歌词也能出带人声的歌 |
| `mureka` | `mureka-music-v9` | 更便宜；不会自动写词 |

拿一个腾讯云账号、一个 Key 就能同时用这两家。接口是**同步调用**——一次请求直接返回成品，不用轮询任务。

### 开通步骤

没有"购买"按钮，TokenHub 是**后付费**：开通即用，按量扣费。

1. 注册腾讯云账号并完成**实名认证**
2. 开通 TokenHub —— <https://console.cloud.tencent.com/tokenhub/>
3. **在「在线推理 → 语音模型」处开启音乐模型的后付费** —— <https://console.cloud.tencent.com/tokenhub/inference?serviceType=SPEECH>
   ⚠️ 这步最容易被漏掉，不做的话请求会被直接拒绝
4. 创建 API Key —— <https://console.cloud.tencent.com/tokenhub/apikey>
   **建完立刻复制存好，之后再也看不到明文**

价格（2026-11 官方模型价格页）：

| 模型 | 单价 | 每首用量 | 折合 |
|---|---|---|---|
| `minimax-music-v3.0` | 10 元/百万 tokens | 100,000 tokens | **约 1 元/首** |
| `minimax-music-v2.6` | 10 元/百万 tokens | 100,000 tokens | 约 1 元/首 |
| `mureka-music-v9` | 10 元/百万 tokens | 33,000 tokens | **约 0.33 元/首** |

音乐模型**不在新人免费体验包里**（免费包只覆盖语言、视频、3D、多模态理解），所以第一首就开始计费。

### 配置

只有 API Key 是必填的，其余都有默认值：

| 环境变量 | 默认值 | 说明 |
|---|---|---|
| `JONUS_CLOUD_API_KEY` | — | **必填**。设置后自动启用云引擎 |
| `JONUS_CLOUD_PROVIDER` | `minimax` | 换成 `mureka` 更便宜 |
| `JONUS_CLOUD_MODEL` | 各家默认 | 例如降级到 `minimax-music-v2.6` |
| `JONUS_CLOUD_BASE_URL` | `https://tokenhub.tencentmaas.com` | **不支持跨地域**，广州开的别填新加坡 |
| `JONUS_CLOUD_TIMEOUT` | `10m` | 一次生成的总超时 |

`--model` 参数可以按单次请求覆盖模型，不影响环境变量。

### 它是怎么出歌的

`prompt` 描述风格、情绪、场景；不给歌词时，适配器会自动打开 MiniMax 的 `lyrics_optimizer`，让模型**自己写词、自己唱**。这正是"输入文本、输出真歌"的实现方式。

想要纯音乐：`--instrumental`。
想自己写词：`--lyrics` 或 `--lyrics-file`，建议带 `[Verse]` / `[Chorus]` 结构标记——腾讯文档说这能明显改善歌曲结构的完整度。

### 三个必须知道的限制

**时长不可控。** 两家都按歌词长度决定歌曲时长，API 里没有时长参数。CLI 的 `--duration` 会被忽略（结果的 `meta.duration_note` 里会注明），Web UI 也会自动隐藏那个滑块（通过 `/api/engine/status` 返回的 `controls_duration: false`）。

**不可复现。** 两家都不接受 seed，所以结果里的 `seed` 恒为 0——回填你请求的 seed 会假装它有能力复现，而它没有。

**下载链接 12 小时过期。** 适配器拿到链接就立刻下载并落盘，所以这对你无感；但如果你自己去抓 `hex` 格式之外的链接，记得及时取。

---

## 命令行

```text
jonus [--data-dir DIR] [--engine NAME] [--log-level LEVEL] <command>
```

`--data-dir`、`--engine`、`--log-level` 是全局参数，写在哪层都行。

### `jonus generate [prompt]`

从一句描述生成一首歌。任务会入队后立即返回，除非加 `--wait`。

| 参数 | 说明 |
|---|---|
| `--style` | 追加到 prompt 的风格标签，例如 `"acoustic, warm"` |
| `--lyrics` / `--lyrics-file` | 歌词，二选一；可含 `[Verse]` / `[Chorus]` |
| `--instrumental` | 无人声 |
| `--duration` | 请求时长，例如 `60s`（仅支持该能力的引擎有效） |
| `--lang` | 语言提示，例如 `zh` / `en` |
| `--title` | 曲名（默认从 prompt 推导） |
| `--seed` | 随机种子，`0` 表示随机 |
| `--model` | 引擎特有的模型名 |
| `--kind` | 操作类型：`text2music` / `cover` / `repaint` / `lego` / `extract` / `complete` |
| `--output` | 完成后把音频复制到这个路径 |
| `--wait` | 跟进直到任务结束（消费与 Web UI 同一份事件流） |
| `--json` | 输出机器可读的 JSON |

`--wait` 会在本进程里跑队列，所以**不需要**另外开 `jonus serve`。

```bash
jonus generate "city pop，夜晚开车，女声" --wait --output ~/Desktop/night.mp3
jonus generate --lyrics-file lyrics.txt --style "国风, 古筝" --wait
jonus generate "lofi 学习背景音" --instrumental --wait
```

### `jonus serve`

启动 Web UI 和 HTTP API。默认 `127.0.0.1:7777`。

| 参数 | 说明 |
|---|---|
| `--port` | 监听端口（默认 7777） |
| `--open` | 启动后自动打开浏览器 |
| `--dev` | 开发模式，允许 Vite dev server（:5173）跨源调用 API |

服务端**在引擎就绪之前就已经起来**了，所以慢模型不会挡住你访问曲库。

### `jonus jobs`

| 命令 | 作用 |
|---|---|
| `jobs list [--limit N] [--json]` | 列出任务 |
| `jobs show <id> [--follow] [--json]` | 查看单个任务；`--follow` 持续跟进 |
| `jobs cancel <id>` | 取消 |
| `jobs retry <id>` | 以新任务重跑，沿用原参数 |
| `jobs delete <id> [--force]` | 删除任务、曲目和文件 |

### `jonus library`

| 命令 | 作用 |
|---|---|
| `library list [--job ID] [--limit N] [--stems] [--json]` | 列出曲目 |
| `library play <track-id>` | 用系统播放器播放 |
| `library export <track-id> <path>` | 把曲目原样复制出来 |

### `jonus doctor [--strict]`

环境体检。**只检查当前引擎真正需要的东西**——用云引擎时不会来烦你装 Python。

- `mock`：数据目录可写、端口可用
- `cloud`：key 是否存在（**只报有无，绝不打印 key 本身**）、网关是否可达、输出目录可写
- `ace-step`：uv / python3.12 / HF 镜像 / sidecar 端口

`--strict` 让任一检查失败时以非零码退出，适合 CI。

### `jonus version [--short]`

---

## Web UI

```bash
./bin/jonus serve --open
```

页面是单文件 Go 二进制里嵌着的 Svelte 应用，三栏布局：

- **创作** —— 描述、风格标签（可点选）、歌词、纯音乐开关。引擎不控制时长时，时长滑块会被一句说明替代
- **任务队列** —— 实时进度（SSE 推送）、取消、重试、删除
- **曲库** —— 试听、拖动定位（`http.ServeContent` 自带 Range 支持）

前后端是同一套 API，所以 CLI 和 Web 看到的进度一模一样——不是两套实现碰巧一致，而是同一条事件流。

### 前端开发

```bash
make dev     # Vite HMR 在 :5173，API 在 :7777
```

Vite 把 `/api` 代理到后端。生产构建通过 `//go:embed all:dist` 嵌进二进制（`internal/webui/dist`）。

---

## HTTP API

全部在 `/api` 下。

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/health` | 存活探测 |
| GET | `/api/engine/status` | 引擎状态与能力（含 `controls_duration`） |
| GET | `/api/models` | 引擎支持的模型（若实现 `ModelLister`） |
| GET | `/api/jobs` | 任务列表 |
| POST | `/api/jobs` | 创建任务 |
| GET | `/api/jobs/{id}` | 任务详情 + 曲目 |
| DELETE | `/api/jobs/{id}` | 删除任务及文件 |
| POST | `/api/jobs/{id}/cancel` | 取消 |
| POST | `/api/jobs/{id}/retry` | 重跑 |
| GET | `/api/jobs/{id}/events` | **SSE** 单任务进度 |
| GET | `/api/events` | **SSE** 全局队列流 |
| GET | `/api/tracks` | 曲库列表 |
| GET | `/api/tracks/{id}` | 曲目详情 |
| GET | `/api/tracks/{id}/audio` | 音频流（支持 Range / seek） |

创建任务的请求体：

```json
{
  "prompt": "轻快的民谣，木吉他扫弦",
  "style": "民谣, 温暖",
  "lyrics": "",
  "instrumental": false,
  "duration_seconds": 60,
  "language": "zh",
  "seed": 0,
  "model": ""
}
```

`prompt` 和 `lyrics` 至少要有一个。

未知的 `/api/*` 路径返回 **JSON 404**，不会掉进 SPA 兜底返回一段 HTML。

```bash
# 看 SSE 事件流
curl -N localhost:7777/api/jobs/<id>/events

# 验证 Range 支持
curl -r 0-99 -i localhost:7777/api/tracks/<id>/audio
```

---

## 配置

配置分三层，后者覆盖前者：**内置默认值 → 环境变量 → 命令行参数**。

| 环境变量 | 默认值 | 说明 |
|---|---|---|
| `JONUS_DATA_DIR` | 见下 | 数据库与音频的存放位置 |
| `JONUS_HTTP_ADDR` | `127.0.0.1:7777` | 监听地址 |
| `JONUS_ENGINE` | `mock` | `mock` / `cloud` / `ace-step` |
| `JONUS_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |
| `JONUS_CLOUD_*` | 见[云引擎](#配置) | 云端参数 |

**数据目录默认位置**：

- macOS：`~/Library/Application Support/jonus`
- Linux：`$XDG_CONFIG_HOME/jonus`（通常 `~/.config/jonus`）
- Windows：`%APPDATA%\jonus`

目录结构：

```
jonus/
├── jonus.db          # SQLite：任务、曲目、事件日志
├── tracks/<job>/     # 生成的音频，按任务分目录
└── peaks/            # 波形缓存
```

数据库用 WAL 模式，单写者连接。**不需要** Redis 或任何外部服务。

---

## 构建与开发

```bash
make help       # 列出所有目标
make build      # 前端 + 二进制
make build-go   # 只编译 Go，复用已嵌入的前端
make run        # 构建并启动
make dev        # 后端 + Vite HMR
make test       # go test ./...
make vet        # go vet ./...
make fmt        # gofmt -s -w .
make lint       # golangci-lint（需自行安装）
make doctor     # 环境体检
make clean      # 清掉 bin/ 和嵌入的前端产物
```

嵌入资源有个坑值得记：`go:embed` 在 pattern 匹配不到文件时是**编译期错误**，所以 `internal/webui/dist/index.html` 必须有个占位文件；同时 Vite 的 `build.outDir` 指向 `../internal/webui/dist`，因为 `go:embed` 够不到本包目录之外的路径。

---

## 项目结构

```
jonus/
├── cmd/jonus/main.go       # 仅入口：signal ctx + cli.Execute()
├── internal/
│   ├── cli/                # cobra 命令树 —— 薄适配层，零业务逻辑
│   ├── app/                # 服务层 + composition root，CLI 与 HTTP 共用
│   ├── engine/             # Engine 接口（可插拔缝）
│   │   ├── mock/           # 本地合成，无需模型
│   │   └── cloud/          # 托管 API 适配器（minimax / mureka）
│   ├── job/                # 领域模型 + 持久化
│   │   └── sqlite/         # sqlx 实现 + goose 迁移
│   ├── queue/              # worker pool + 进度 hub
│   ├── audio/              # WAV 编解码
│   ├── storage/            # 磁盘产物布局
│   ├── httpapi/            # HTTP 传输层 + SSE
│   ├── webui/              # go:embed 的前端
│   └── config/
└── web/                    # Svelte 5 + Vite 源码
```

边界原则：**`cli` 和 `httpapi` 是两个传输层，都只调 `app`**。任何"CLI 和 Web 可能不一致"的逻辑都必须放进 `app`——这是它们不会漂移的唯一原因。

### 引擎接口

```go
type Engine interface {
    Name() string
    Capabilities() Capabilities
    Generate(ctx context.Context, req Request, progress ProgressFunc) (Result, error)
}
```

只有三个方法。可选能力用类型断言发现，不塞进主接口：

```go
type ModelLister interface { ListModels(ctx context.Context) ([]string, error) }
type Cleaner     interface { Close() error }
```

实现方不需要 import 这个包来声明"我实现了它"——Go 的结构化满足就够了。

`ProgressFunc` 的契约：不得阻塞、可为 nil、`Generate` 返回后不得再调用、取消走 `ctx` 而不是回调。它的 `Report` 方法对 nil 安全并把 fraction 夹到 `[0,1]`，所以引擎内部不用重复这两层防御。

---

## 已知限制

- **`ace-step` 未接线**。接口和包结构留好了，但装配时会直接报错。要本地离线出歌得先把 Python sidecar 补上。
- **云端时长不可控**、**不可复现**——见[三个必须知道的限制](#三个必须知道的限制)。
- **波形 peaks 端点尚未实现**。`peaks/` 目录已在布局里预留，但计算和 `/api/tracks/{id}/peaks` 还没做。
- **`cover` / `repaint` / `lego` / `extract` / `complete` 这几种 kind，只有 `mock` 引擎实现了**。云引擎只支持 `text2music`，其余的会明确报错。
- **尚未用到 `ffmpeg`**。云引擎拿到什么格式就落什么格式（mp3/wav），噪声 `doctor` 会提示 ffmpeg 缺失（标注用途是转码和解码），但代码里目前还没有调用它——转码是后续阶段的事。

---

## 测试

```bash
go test ./...
go test -race ./...
```

云端适配器的测试**完全离线**：用 `httptest` 起一个假网关，同时提供 API 路由和音频下载，断言请求体、路由选择、响应解析、错误透传、进度单调性和落盘结果。没有任何测试打真实厂商接口，所以测试不会花钱。
