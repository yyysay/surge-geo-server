# Surge Geo Server

一个轻量的自托管 Surge Geo 规则服务。

服务会自动获取上游 Geosite 和 GeoIP DAT 数据，根据请求按需生成 Surge
规则集，同时提供网页查询功能，用于检查某个域名或 IP 地址会匹配哪些规则集。
它不会在仓库中生成和保存庞大的全量规则目录。

## 功能

- 按需生成 Surge Geosite 规则：`GET /geosite/{name[@filter]}`
- 按需生成 Surge GeoIP 规则：`GET /geoip/{name}`
- 查询域名匹配的 Geosite 集合和具体规则
- 查询 IP 匹配的 GeoIP 集合和 CIDR
- 使用 `ETag` 和 `Last-Modified` 检查上游更新
- 将下载的数据持久化到 `/data`
- 为规则响应提供 `ETag` 和缓存响应头
- 查询页面直接嵌入 Go 可执行文件，无需单独部署前端
- 页面显示当前数据源，并可生成自定义启动参数和 Docker 环境变量
- 支持 Docker、本地运行以及反向代理部署

## 快速开始

### Docker Compose

```bash
docker compose up -d --build
```

启动后访问：

```text
http://127.0.0.1:8080
```

Compose 默认只监听本机地址，不会直接暴露到公网。需要远程使用时，可以通过
Caddy、Nginx 等服务反向代理，并配置自己的域名和 HTTPS。

停止服务：

```bash
docker compose down
```

规则数据保存在名为 `surge-geo-data` 的 Docker Volume 中。停止或重建容器不会
删除数据；只有显式删除该 Volume 时，缓存数据才会被移除。

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
| `regexp` | 暂时跳过 |

Surge 不支持 `DOMAIN-REGEX`，而 `URL-REGEX` 匹配的是完整 URL，两者并不等价。
因此当前版本不会直接将正则规则改写为 `URL-REGEX`。被跳过的规则数量会通过
`X-Surge-Geo-Skipped-Regex` 响应头返回。

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

### 查看当前数据源

```http
GET /api/config
```

返回服务启动时实际采用的 Geosite 和 GeoIP 地址。首页会自动读取这两个地址；修改
输入框后，可以生成对应的 `-geosite-url`、`-geoip-url` 参数及 Docker 环境变量。

出于安全考虑，公开页面不会直接修改服务器配置。自定义数据源需要写入启动参数或
环境变量，并重启服务后生效，这样可以避免未鉴权的远程地址输入形成 SSRF 风险。

## 本地开发

要求 Go 1.26 或更高版本。

```bash
go run ./cmd/server \
  -listen 127.0.0.1:8080 \
  -data-dir ./data
```

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

默认数据源：

- Geosite：[v2fly/domain-list-community](https://github.com/v2fly/domain-list-community)
- GeoIP：[MetaCubeX/meta-rules-dat](https://github.com/MetaCubeX/meta-rules-dat)

服务启动时会检查并下载数据。后续按照更新间隔发送条件请求；上游没有变化时不会
重复下载，也不会重新加载规则索引。如果上游暂时不可用，但本地已有数据，服务会
继续使用缓存启动。

## 项目设计

```text
上游 DAT 数据
    ↓ 条件更新
内存规则索引 + 持久化源文件
    ├── /geosite/*       动态生成域名规则
    ├── /geoip/*         动态生成 IP 规则
    ├── /api/lookup/*    查询规则归属
    └── /                查询页面
```

项目只在收到请求时渲染对应规则，生成结果直接通过 HTTP 返回。相比将全部规则转换
后提交到 Git 仓库，这种方式减少了无用文件、提交记录和客户端学习成本。

## 许可证与第三方说明

本项目使用 MIT 许可证，第三方代码与数据来源见 [NOTICE.md](NOTICE.md)。使用者仍需
自行遵守所选规则数据源的许可证及使用条款。

## 后续计划

- 独立实现 `strict` 和 `balanced` 正则降级模式
- 解析 DLC 原始源文件，在查询结果中显示 `include:` 来源链
- 为管理类刷新接口增加鉴权和缓存控制
- 通过 GitHub Actions 发布 amd64/arm64 Docker 镜像
