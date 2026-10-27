# 剧栈（CCTV Subscription Downloader）

剧栈是一个用于搜索、订阅和自动下载央视网剧集的自托管工具。它提供带海报的 Web 管理界面，能够定时检查订阅更新、选择源站提供的最高画质、使用纯 Go 播放器兼容层解密 CCTV TS 分片，并通过 FFmpeg 无损封装为 MP4。

> 请仅下载你有权保存的内容，并遵守央视网服务条款及所在地法律法规。

## 主要功能

- 搜索央视网剧集，也可以直接粘贴 `tv.cctv.com` 剧集链接
- 搜索结果展示海报、简介、年份、地区、类型和集数
- 添加订阅时可选择是否下载已有剧集
- 定时轮询订阅，发现新剧集后自动创建下载任务
- 支持多级分类，每个分类可以配置独立保存路径
- 自动按“分类路径 / 剧集名称 / 单集名称.mp4”归档
- 下载任务支持进度查看、取消、失败重试、全部暂停和全部恢复
- 可设置订阅轮询时间、并发下载数和下载后完整性检测
- 自动选择像素数最高的分辨率；分辨率相同时选择码率最高的流
- SQLite 单文件持久化，无需 MySQL、PostgreSQL 或其他外部数据库
- Go 直接运行央视官方播放器 WASM 解密逻辑，不启动 Chromium 或 Node.js 解密进程
- FFmpeg 使用 `-c copy` 封装 MP4，避免重新编码造成的高 CPU 占用和画质损失

## 技术架构

| 模块 | 技术 | 作用 |
| --- | --- | --- |
| API 与任务调度 | Go 1.22+ | 搜索、订阅轮询、任务队列、下载与分类管理 |
| CCTV 解密 | Go + wazero | 加载官方播放器 WASM，解密加密 TS 分片 |
| 数据库 | SQLite | 保存设置、分类、订阅、剧集和下载任务 |
| 视频处理 | FFmpeg | 将解密后的 MPEG-TS 无损封装为 MP4，可选全片解码检测 |
| 管理界面 | React + TypeScript + Vinext | 搜索、订阅、任务和设置界面 |
| 部署入口 | Docker Compose + Nginx | 统一代理 Web 页面与 `/api` 请求 |

Node.js 和 TypeScript 只用于管理界面的构建与运行。CCTV 下载、轮询和解密均由 Go 完成，不依赖 Chromium，也不会调用 Node.js 解密脚本。

## 下载流程

1. 解析剧集页面并获得播放器候选地址。
2. 优先尝试 `hls_h5e_url`，然后回退到 `hls_enc2_url`、`hls_enc_url` 和普通 HLS。
3. 分析主播放列表并选择最高分辨率、最高码率的媒体流。
4. 下载 TS 分片；如播放列表使用标准 AES-128，先进行 AES 解密。
5. Go 加载并缓存央视官方 `cctv.worker.js` 中的 WASM，通过 wazero 执行播放器兼容解密。
6. 将解密后的 TS 写入临时文件。
7. FFmpeg 以 `-c copy` 无损封装为 MP4，并添加 `faststart` 标记。
8. 如果设置中开启了“下载后完整性检测”，再对整部视频进行一次解码验证。

完整性检测会显著增加 CPU 占用，默认关闭。无损封装本身不会重新编码视频。

## 快速开始：Docker Compose

推荐使用 Docker Compose 部署。宿主机只需要 Docker 和 Docker Compose，不需要单独安装 Go、Node.js 或 FFmpeg。

```bash
docker compose up -d --build
```

启动后访问：

```text
http://服务器地址:3010
```

查看运行状态和日志：

```bash
docker compose ps
docker compose logs -f api web gateway
```

停止服务：

```bash
docker compose down
```

`docker compose down` 不会删除项目下的 SQLite 数据或宿主机中的视频文件。

## NAS 部署

当前 [compose.yaml](./compose.yaml) 使用以下下载目录映射：

```yaml
/vol2/1000/电影电视:/downloads
```

因此，在管理界面创建分类时必须填写容器内路径，而不是宿主机路径。例如：

| 分类 | 管理界面填写的保存路径 | 宿主机实际目录 |
| --- | --- | --- |
| 电视剧 | `/downloads/电视剧` | `/vol2/1000/电影电视/电视剧` |
| 儿童 | `/downloads/电视剧/儿童` | `/vol2/1000/电影电视/电视剧/儿童` |
| 纪录片 | `/downloads/纪录片` | `/vol2/1000/电影电视/纪录片` |

部署到当前 NAS 的基本步骤：

```bash
ssh root@10.0.0.2
cd /path/to/cctv-sub
docker compose up -d --build
```

完成后访问 `http://10.0.0.2:3010`。

如果 API 日志出现“无法创建分类保存路径”或“权限不足”，请确认 `/vol2/1000/电影电视` 存在，并且 Docker 对该目录具有读写权限。

## 本地开发

### 环境要求

- Go 1.22 或更高版本
- Node.js 22.13 或更高版本
- npm
- FFmpeg（可从命令行执行 `ffmpeg`）

### 启动后端

```bash
go run ./cmd/cctv-sub
```

后端默认监听 `http://127.0.0.1:8080`，数据保存到 `./data/cctv-sub.db`，默认分类路径为 `./downloads`。

### 启动管理界面

另开一个终端：

```bash
npm ci
npm run dev
```

打开 `http://localhost:3000`。开发模式下，界面默认请求 `http://127.0.0.1:8080`。

如需使用其他 API 地址：

```bash
NEXT_PUBLIC_API_URL=http://192.168.1.10:8080 npm run dev
```

## 后端启动参数

```text
-addr       API 监听地址，默认 127.0.0.1:8080
-data       数据目录，默认 ./data
-database   SQLite 文件路径，默认 <data>/cctv-sub.db
-downloads  首次创建默认分类时使用的下载路径，默认 ./downloads
```

示例：

```bash
go run ./cmd/cctv-sub \
  -addr 0.0.0.0:8080 \
  -data ./data \
  -database ./data/cctv-sub.db \
  -downloads ./downloads
```

保存路径已经迁移到分类中，因此 `-downloads` 只决定首次初始化时“默认分类”的路径；之后应在管理界面的分类设置中修改。

## 环境变量

| 变量 | 说明 |
| --- | --- |
| `NEXT_PUBLIC_API_URL` | 管理界面的 API 基础地址；生产 Docker 部署留空并通过 Nginx 使用同源 `/api` |
| `CCTV_PLAYER_CACHE` | 央视播放器 Worker/WASM 缓存目录 |
| `CCTV_WASM_SOURCE_PATH` | 可选，本地 `cctv.worker.js` 路径；不设置时自动从央视播放器地址下载并缓存 |
| `CCTV_FFMPEG_PATH` | 可选，指定 FFmpeg 可执行文件的绝对路径 |
| `CCTV_FFPROBE_PATH` | 仅完整集成测试使用，可选指定 ffprobe 路径 |

Docker 部署默认将播放器缓存保存到 `/data/cctv-player-cache`，与 SQLite 数据一起持久化。

## 界面使用

### 1. 创建保存分类

进入“设置”，先创建一个或多个分类。分类支持无限层级，每个分类都有独立保存路径。子分类不会自动继承父分类路径，应填写它自己的实际路径。

### 2. 搜索并添加订阅

在“搜索”页面输入剧名或粘贴央视网剧集链接，从带海报的结果中选择剧集。添加订阅时选择保存分类，并决定是否立即下载当前已有剧集。

### 3. 管理订阅

订阅可以暂停、恢复、手动检查更新、更换保存分类或删除。自动下载开启且订阅状态为正常时，新发现的剧集会进入下载队列。

### 4. 管理下载

下载页可以查看等待中、下载中、已暂停、已完成、失败和已取消任务。失败任务可以重试；“全部暂停”会停止正在运行的任务并暂停队列，“全部恢复”会重新排队。

### 5. 调整设置

- 轮询间隔：1–10080 分钟，默认 30 分钟
- 同时下载剧集数：1–10，默认 2
- 单集分片并发数：1–16，默认 4（Go 协程并行下载同一集的 TS 分片，随后按原顺序解密、封装）
- 下载后完整性检测：默认关闭

同时下载剧集数决定可并行处理的剧集数量，单集分片并发数决定每集有多少路 HTTP 分片请求；两项设置独立。建议先将剧集数设为 1–2、单集分片并发设为 4–8，若被央视源站限速或受到 CPU/WASM 解密速度限制，增加并发不一定更快。并发设置对之后启动的任务生效，不影响已保存的视频与 SQLite 数据。

## 文件保存规则

每一部剧集都会自动建立独立文件夹：

```text
<分类保存路径>/
└── <剧集名称>/
    ├── <单集名称>.mp4
    ├── <单集名称>.mp4
    └── ...
```

程序会清理文件名中的非法字符。下载过程中使用 `.part` 临时文件，成功后再重命名为 `.mp4`；失败或取消时会清理临时文件。

## 数据与备份

应用数据默认位于：

```text
data/cctv-sub.db
```

SQLite 使用 WAL 模式，运行时可能同时存在：

```text
data/cctv-sub.db
data/cctv-sub.db-wal
data/cctv-sub.db-shm
```

建议停止 API 容器后备份整个 `data/` 目录：

```bash
docker compose stop api
tar -czf cctv-sub-data-backup.tar.gz data/
docker compose start api
```

旧版本的 `data/state.json` 会在首次启动且数据库为空时自动导入 SQLite。确认迁移成功并完成备份后，可以自行移除旧 JSON 文件。

## 构建与测试

运行全部常规测试：

```bash
go test ./...
npm ci
npm test
```

也可以使用 Makefile：

```bash
make test
make build
```

`npm test` 会先构建生产版界面，再验证服务端能够正确渲染应用。

### CCTV 在线集成测试

在线测试会请求央视服务，结果可能受网络、源站接口和节目有效期影响。

```bash
CCTV_INTEGRATION=1 \
CCTV_WASM_SOURCE_PATH=/path/to/cctv.worker.js \
CCTV_FFMPEG_PATH=/usr/bin/ffmpeg \
CCTV_FFPROBE_PATH=/usr/bin/ffprobe \
CCTV_FULL_EPISODE_URL='https://tv.cctv.com/...' \
go test ./internal/service \
  -run TestCCTVOfficialPlayerFullEpisodeIntegration \
  -count=1 -v
```

如需保留测试生成的视频，额外设置：

```bash
CCTV_KEEP_INTEGRATION_OUTPUT=1
```

## 健康检查

Docker 容器会请求：

```text
GET /api/health
```

正常响应会包含：

```json
{
  "ok": true,
  "ffmpeg": true,
  "playerDecryptor": true,
  "decryptorRuntime": "go-wasm",
  "database": true
}
```

如果 `ok` 为 `false`，优先检查 FFmpeg 是否可执行，以及 SQLite 数据目录是否可写。

## 常见问题

### 为什么仍然存在 Node.js 和 TypeScript 文件？

它们用于 React 管理界面。后端下载容器是独立的 Go 程序，解密时不会启动 Node.js 或 Chromium。Docker 会在构建 Web 镜像时自动安装前端依赖，宿主机无需安装 Node.js。

### 为什么第一次下载时 CPU 会短暂升高？

第一次解密需要加载并编译央视官方播放器 WASM，编译完成后会在进程内复用。提高并发数也会增加分片处理、网络和磁盘压力。

### 为什么开启完整性检测后 CPU 很高？

完整性检测会让 FFmpeg 解码整个视频来发现损坏画面，而不是简单读取文件。低功耗 NAS 建议关闭该选项。关闭后 FFmpeg 只执行 `-c copy` 无损封装。

### 为什么生成的 MP4 花屏？

不要直接拼接加密 TS。当前下载链路会先执行标准 HLS AES-128 解密（如果存在），再调用央视播放器 WASM 兼容解密，最后才封装 MP4。可查看任务错误和 API 日志判断是否在解密阶段失败。

### 为什么分类路径保存失败？

Docker 内只能访问已经挂载的目录。当前下载卷挂载点是 `/downloads`，所以分类路径应使用 `/downloads` 或其子目录，而不是 `/vol2/...` 形式的宿主机路径。

### 如何降低 CPU 和内存占用？

- 将并发下载数设置为 1 或 2
- 关闭下载后完整性检测
- 避免频繁重建 Docker 镜像
- 保持播放器缓存目录持久化，避免重复下载和初始化资源

## 项目结构

```text
.
├── app/                 React 管理界面
├── build/               Web 构建辅助插件
├── cmd/cctv-sub/        Go 程序入口
├── data/                SQLite 数据与播放器缓存（运行时生成）
├── deploy/              Nginx 配置
├── internal/
│   ├── cctv/            CCTV 搜索、剧集和播放地址解析
│   ├── cctvwasm/        纯 Go/WASM 播放器解密兼容层
│   ├── model/           数据模型
│   ├── server/          HTTP API
│   ├── service/         订阅、队列、下载和封装流程
│   └── store/           SQLite 持久化
├── public/              Web 静态资源
├── tests/               前端渲染测试
├── worker/              Vinext Web Worker 入口
├── compose.yaml         Docker Compose 部署配置
└── Dockerfile           API 与 Web 多阶段镜像
```

## 更新部署

更新源码后重新构建：

```bash
docker compose up -d --build
docker compose ps
```

更新前建议先备份 `data/`。视频保存在宿主机挂载目录中，不会因为重新构建容器而删除。

## Windows / Debian 单文件运行版

在 [GitHub Actions 构建页面](https://github.com/hwyc888/cctv/actions/workflows/build-binaries.yml) 选择最新一次成功的 `Build Windows and Debian binaries`，下载 `cctv-sub-windows-debian` 构建产物 ZIP，解压后可获得：

- `cctv-sub-windows-amd64.zip`：Windows 10/11（x64），内含 `cctv-sub.exe`。
- `cctv-sub-linux-amd64.tar.gz`：Debian / Ubuntu x86-64 可执行程序。
- `cctv-sub-linux-arm64.tar.gz`：Debian / Ubuntu ARM64 可执行程序。
- `cctv-sub_..._amd64.deb` 和 `cctv-sub_..._arm64.deb`：Debian 安装包。

这几个可执行文件使用 `npm run build:standalone` 构建 Web 管理界面，再通过 `go build -tags standalone` 将其**嵌入 Go 程序**。运行时不需要 Node.js、npm 或额外的 Web 服务，API 与浏览器管理界面共用一个地址。

**Windows：** 先安装 [FFmpeg](https://ffmpeg.org/download.html) 并将 `ffmpeg.exe` 所在目录加入 PATH。解压后从 PowerShell 进入该目录，执行：

```powershell
.\cctv-sub.exe -addr 127.0.0.1:8080 -data .\data -downloads .\downloads
```

然后打开 http://127.0.0.1:8080 。

**Debian / Ubuntu：** 推荐用安装包，自动安装系统依赖 FFmpeg：

```bash
sudo apt install ./cctv-sub_1.0.0+git*_amd64.deb
mkdir -p "$HOME/cctv-data" "$HOME/Videos/CCTV"
cctv-sub -addr 127.0.0.1:8080 -data "$HOME/cctv-data" -downloads "$HOME/Videos/CCTV"
```

ARM64 设备请将安装包名称中的 `amd64` 换成 `arm64`。然后在浏览器打开 http://127.0.0.1:8080 。如果使用 `.tar.gz` 解压的二进制，请先运行 `sudo apt install ffmpeg ca-certificates`，再运行 `./cctv-sub`。

两种系统都默认只监听本机；需要局域网访问时可改为 `-addr 0.0.0.0:8080`，但这会使服务对局域网开放，建议只在可信网络使用。数据库和视频存放在启动时指定的目录中，更新程序不会主动删除这些数据。

也可以在源码根目录自行构建：

```bash
npm ci
npm run build:standalone
go build -tags standalone -o cctv-sub ./cmd/cctv-sub
```

该命令为当前操作系统生成二进制。原先的 `go build ./cmd/cctv-sub` 仍只构建独立 API 后端，Docker Compose 部署方式不变。
