# Surge Geo Server

一个轻量的自托管 Geo 数据服务和规则转换工具。

服务会自动获取上游 Geosite 和 GeoIP DAT 数据，根据请求按需生成 Surge
规则集。网页使用 Mihomo 的 `GEOSITE` / `GEOIP` 写法维护本地规则，并将它们转换为
引用当前 Go 服务的 Surge `RULE-SET`；也可以查询域名或 IP 会命中哪一条本地规则。
它不会在仓库中生成和保存庞大的全量规则目录。

## 功能

- 按需生成 Surge Geosite 规则：`GET /geosite/{name[@filter]}`
- 按需生成 Surge GeoIP 规则：`GET /geoip/{name}`
- 支持 `strict` 和 `balanced` 两种 Geosite 正则降级模式
- 使用 Mihomo `GEOSITE` / `GEOIP` 规则并生成 Surge `RULE-SET` 配置
- 浏览器本地保存规则，不写入服务器运行配置
- Geosite、GeoIP 数据源和正则模式支持校验后热载入
- 使用可选的管理 Token 保护远程配置热载入
- 查询域名匹配的 Geosite 集合和具体规则
- 查询 IP 匹配的 GeoIP 集合和 CIDR
- 使用 `ETag` 和 `Last-Modified` 检查上游更新
- 将下载的数据持久化到 `/data`
- 为规则响应提供 `ETag` 和缓存响应头
- 查询页面直接嵌入 Go 可执行文件，无需单独部署前端
- 页面采用轻量单栏布局，可查询策略、编辑规则和修改运行数据源
- 支持 Docker、本地运行以及反向代理部署

## 快速开始

### Docker Compose

仓库中的 `compose.yaml` 会从当前源码构建 `linux/amd64` 镜像。

如果服务只在本机使用，可以直接启动：

```bash
docker compose up -d --build
```

启动后访问：

```text
http://127.0.0.1:8080
```

Compose 默认只监听本机地址，不会直接暴露到公网。需要远程使用时，可以通过
Caddy、Nginx 等服务反向代理，并配置自己的域名和 HTTPS。

通过反向代理使用“应用配置”前，需要设置管理 Token。先生成一个强随机值：

```bash
openssl rand -hex 32
```

在项目根目录创建不会提交到 Git 的 `.env`：

```dotenv
ADMIN_TOKEN=将这里替换为上一步生成的随机值
```

Compose 默认只监听宿主机的 `127.0.0.1:8080`，适合同一台服务器上的 Caddy 或
Nginx 反向代理。如需修改监听地址或端口，直接调整 `compose.yaml` 中的 `ports`。

然后构建镜像并启动或重建容器：

```bash
docker compose up -d --build
```

`compose.yaml` 会自动读取 `.env` 中的 `ADMIN_TOKEN`。修改 Token 后必须重建容器；
Token 不属于可热载入的运行配置。

查看运行状态和日志：

```bash
docker compose ps
docker compose logs -f surge-geo-server
```

以后更新代码：

```bash
git pull
docker compose up -d --build
```

停止服务：

```bash
docker compose down
```

Geo 数据和运行配置通过 `./data:/data` 保存在 Compose 文件旁的 `data` 目录中，停止、
删除或重建容器都不会删除。主要文件为：

```text
data/runtime.json
data/sources/geosite.dat
data/sources/geosite.dat.meta.json
data/sources/geoip.dat
data/sources/geoip.dat.meta.json
```

部署前可以先创建目录：

```bash
mkdir -p data/sources
```

部署版 Compose 使用容器 root 读写 bind mount，避免宿主机 UID 与镜像内 UID 不一致
导致 `runtime.json: permission denied`。容器只监听宿主机回环地址，但 root 仍可读写
挂载的 `data` 目录；如果自行改回非 root 运行，需要同步调整该目录的所有权和权限。

可以自行下载 DAT 到 `data/sources`。如果启动时无法访问远程数据源，只要
`geosite.dat` 和 `geoip.dat` 都存在且能够成功解析，服务就会直接使用本地文件启动，
不要求预先存在 `.meta.json`。网络恢复后，定时刷新仍会根据配置的数据源 URL 更新文件
并维护相邻的 `.meta.json`；网页规则保存在当前浏览器，不进入 `data` 目录。

## 浏览器规则

网页规则只支持 Mihomo 的 Geodata 写法，不需要也不支持 `MATCH`：

```yaml
GEOSITE,google,手动选择
GEOSITE,cn,DIRECT
GEOIP,CN,DIRECT,no-resolve
```

规则输入后立即保存在浏览器本地。点击“生成 Surge 配置”时，页面根据当前 Go 服务的地址
自动生成订阅 URL。例如页面位于 `https://rules.example.com` 时，上面的规则会生成：

```ini
RULE-SET,https://rules.example.com/geosite/google,手动选择
RULE-SET,https://rules.example.com/geosite/cn,DIRECT
RULE-SET,https://rules.example.com/geoip/cn,DIRECT,no-resolve
```

只有 GeoIP 配置会自动补充 `no-resolve`，Geosite 不需要。页面只提供复制，不下载静态
规则文件；`RULE-SET` 始终引用 Go 服务的动态端点，上游更新后不需要重新生成配置。
查询时规则文本只作为本次请求的匹配条件使用，不会写入 `runtime.json`，也不会触发
服务器热载入。

规则只存在当前浏览器的 `localStorage` 中，服务端不提供 `/api/rules/*`。清除站点数据、
更换浏览器或更换访问域名后，需要重新填写规则。

## Surge 使用示例

假设服务部署在 `https://rules.example.com`：

```ini
[Rule]
RULE-SET,https://rules.example.com/geosite/openai,Proxy
RULE-SET,https://rules.example.com/geosite/apple@cn,DIRECT
RULE-SET,https://rules.example.com/geoip/google,Proxy,no-resolve
RULE-SET,https://rules.example.com/geoip/cn,DIRECT,no-resolve
```

直接访问规则内容：

```text
https://rules.example.com/geosite/openai
https://rules.example.com/geosite/apple@cn
https://rules.example.com/geoip/google
https://rules.example.com/geoip/cn
```

## API

### 获取 Geosite 列表

```http
GET /geosite
```

返回所有 Geosite 集合及其可用标签。

### 获取 Geosite 规则

```http
GET /geosite/openai
GET /geosite/apple@cn
```

转换关系：

| Geosite 类型 | Surge 类型 |
| --- | --- |
| `full` | `DOMAIN` |
| `domain` | `DOMAIN-SUFFIX` |
| `keyword` | `DOMAIN-KEYWORD` |
| `regexp` | 根据正则降级模式转换为 `DOMAIN`、`DOMAIN-KEYWORD` 或 `DOMAIN-WILDCARD` |

Surge 不支持 `DOMAIN-REGEX`，而 `URL-REGEX` 匹配的是完整 URL，两者并不等价，
因此服务不会直接改写为 `URL-REGEX`。可以通过 `-regex-mode` 或 `REGEX_MODE`
选择降级策略：

- `strict`（默认）：只转换能够用 Surge 域名规则精确表达的正则，其余跳过。
- `balanced`：优先精确转换；遇到无法精确表达的重复、字符范围或断言时，生成匹配
  范围更宽的 `DOMAIN-WILDCARD` 规则，以降低漏匹配概率；若转换结果没有保留足够的
  字面量约束，则仍会跳过，避免生成接近全匹配的规则。

规则响应会通过 `X-Surge-Geo-Regex-Mode` 返回当前模式，并分别通过
`X-Surge-Geo-Exact-Regex`、`X-Surge-Geo-Degraded-Regex` 和
`X-Surge-Geo-Skipped-Regex` 返回三类正则的数量。由于 `balanced` 可能扩大匹配范围，
路由规则建议先使用默认的 `strict`，确认生成结果后再切换。

### 获取 GeoIP 列表

```http
GET /geoip
```

### 获取 GeoIP 规则

```http
GET /geoip/google
GET /geoip/cn
```

IPv4 和 IPv6 会分别转换为：

```text
IP-CIDR,8.8.8.0/24,no-resolve
IP-CIDR6,2001:4860::/32,no-resolve
```

### 查询域名归属

```http
GET /api/lookup/domain?value=api.openai.com
```

查询会按照原始 Geosite 语义检查精确域名、域名后缀、关键词和正则规则。一个域名
可能同时属于多个集合，例如 `openai`、`category-ai-chat-!cn` 和
`geolocation-!cn`。

### 按浏览器规则查询

网页使用此接口按规则顺序匹配域名或 IP：

```http
POST /query
Content-Type: application/x-www-form-urlencoded

value=google.com&rules=GEOSITE%2Cgoogle%2CProxy
```

`rules` 只接受 `GEOSITE` 和 `GEOIP`，返回第一条命中规则的策略、规则匹配记录和 Geo
归属。规则只用于本次请求，不会在服务端保存。

### 查询 Geosite 原始数据（调试接口）

```http
GET /api/geosite/openai
GET /api/geosite/google@cn
```

该接口仅供调试，返回 DAT 中保存的原始 `domain`、`full`、`keyword`、`regexp` 规则
及属性，不进行 Surge 格式转换；网页不提供原始分组浏览器。

### 查询 IP 归属

```http
GET /api/lookup/ip?value=8.8.8.8
```

返回该地址匹配的 GeoIP 集合及对应 CIDR。

### 查看运行状态

```http
GET /api/status
GET /healthz
```

### 查看运行配置

```http
GET /api/config
```

返回当前 Geosite、GeoIP 地址、正则模式、配置版本和载入时间。读取配置不需要 Token。

### 热载入运行配置

可热载入的内容只有 Geosite 地址、GeoIP 地址和正则模式。网页规则保存在浏览器本地，
管理 Token、监听地址和刷新间隔都是启动配置，不参与热载入。

网页操作方式：

1. 展开页面底部的“配置”。
2. 修改 Geosite、GeoIP 或 Regex。
3. 如果服务配置了 `ADMIN_TOKEN`，在 Token 输入框中填写原始 Token。
4. 点击“应用配置”。

Token 只用于本次请求，不会写入 `localStorage` 或 `runtime.json`，刷新页面后需要重新
填写。网页会发送完整候选配置，等价于：

```http
PUT /api/config
Content-Type: application/json
Authorization: Bearer <ADMIN_TOKEN>

{
  "geosite_url": "https://example.com/geosite.dat",
  "geoip_url": "https://example.com/geoip.dat",
  "regex_mode": "strict"
}
```

服务会先下载变化的数据源并解析两个 DAT，随后持久化 `runtime.json` 并一次性发布
新快照。任一步失败都会返回 `422`，当前运行版本不变。
也可以直接编辑 `runtime.json`，服务默认每 2 秒检查一次并采用相同流程热载入。

鉴权规则：

- 未配置 `ADMIN_TOKEN`：只允许 Host 和客户端地址都是回环地址、且没有 `Forwarded`
  或 `X-Forwarded-For` 的本机直连请求。
- 已配置 `ADMIN_TOKEN`：所有 `PUT /api/config` 都必须携带完全匹配的
  `Authorization: Bearer <ADMIN_TOKEN>`，包括本机请求。
- 正确 Token 可以通过反向代理使用；远程部署必须启用 HTTPS，避免 Token 明文传输。
- Token 只保护 `PUT /api/config`。规则订阅、查询、状态和读取配置仍是公开接口。

使用 curl 测试：

```bash
curl -X PUT https://rules.example.com/api/config \
  -H 'Authorization: Bearer 你的 ADMIN_TOKEN' \
  -H 'Content-Type: application/json' \
  --data '{
    "geosite_url": "https://example.com/geosite.dat",
    "geoip_url": "https://example.com/geoip.dat",
    "regex_mode": "strict"
  }'
```

无 Token 或错误 Token 返回 `403`；JSON 格式错误返回 `400`；候选数据源下载、解析或
校验失败返回 `422`，且不会替换当前运行快照。

## 本地开发

要求 Go 1.26 或更高版本。

```bash
go run ./cmd/server \
  -listen 127.0.0.1:8080 \
  -data-dir ./data \
  -admin-token '替换为强随机值'
```

仅在本机直连调试时可以省略 `-admin-token`。也可以使用环境变量
`ADMIN_TOKEN='替换为强随机值'`。

运行测试和静态检查：

```bash
go test ./...
go vet ./...
```

## 配置

所有配置均可通过命令行参数或环境变量指定：

| 命令行参数 | 环境变量 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-listen` | `LISTEN` | `:8080` | HTTP 监听地址 |
| `-data-dir` | `DATA_DIR` | `./data` | 数据和缓存目录 |
| `-refresh` | `REFRESH_INTERVAL` | `6h` | 上游检查间隔 |
| `-geosite-url` | `GEOSITE_URL` | v2fly 最新版 `dlc.dat` | Geosite 数据地址 |
| `-geoip-url` | `GEOIP_URL` | MetaCubeX 最新版 `geoip-lite.dat` | GeoIP 数据地址 |
| `-regex-mode` | `REGEX_MODE` | `strict` | 正则降级模式：`strict` 或 `balanced` |
| `-config-file` | `RUNTIME_CONFIG` | `<DATA_DIR>/runtime.json` | 可热载入的持久化运行配置 |
| `-config-watch` | `CONFIG_WATCH_INTERVAL` | `2s` | 运行配置检查间隔 |
| `-admin-token` | `ADMIN_TOKEN` | 空 | 保护 `PUT /api/config` 的 Bearer Token；修改后需重启 |

默认数据源：

- Geosite：[v2fly/domain-list-community](https://github.com/v2fly/domain-list-community)
- GeoIP：[MetaCubeX/meta-rules-dat](https://github.com/MetaCubeX/meta-rules-dat)

Geosite、GeoIP 和正则模式的启动参数或环境变量只用于创建第一份 `runtime.json`；
该文件存在后会成为这三项运行配置的持久化来源，修改对应环境变量不会覆盖已经热载入
的配置。`ADMIN_TOKEN` 不写入 `runtime.json`，每次启动都从参数或环境变量读取。
服务启动时会校验全部配置并检查和下载数据。后续按照更新间隔发送条件请求；
上游没有变化时不会重复下载，也不会重新加载规则索引。如果上游暂时不可用，但本地
已有数据，服务会继续使用缓存启动。

## 项目设计

```text
上游 DAT 数据
    ↓ 条件更新
不可变运行快照（数据索引 + 配置版本）
    ├── PUT /api/config  校验后原子热载入
    ├── /geosite/*       动态生成域名规则
    ├── /geoip/*         动态生成 IP 规则
    ├── /api/geosite/*   查询原始 Geosite 数据
    ├── /api/lookup/*    查询规则归属
    └── /                浏览器本地规则、规则匹配和生成 Surge 配置
```

项目只在收到请求时渲染对应规则，生成结果直接通过 HTTP 返回。相比将全部规则转换
后提交到 Git 仓库，这种方式减少了无用文件、提交记录和客户端学习成本。

## 许可证与第三方说明

本项目使用 MIT 许可证，第三方代码与数据来源见 [NOTICE.md](NOTICE.md)。使用者仍需
自行遵守所选规则数据源的许可证及使用条款。

## 后续计划

- 规则顺序诊断：显示被前序规则遮蔽的匹配和集合重叠
- 浏览器本地命中统计和批量测试，不上传查询历史
- 通过 GitHub Actions 发布 amd64/arm64 Docker 镜像
