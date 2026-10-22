# gmail-archiver

Gmail 附件归档与课程作业统计服务。Go 服务通过 IMAP 接收邮件，把附件保存到本地存储和 SQLite，并根据 CSV 花名册与 YAML 规则生成作业状态、历史版本和下载归档。

QQ 对话、身份权限和 LLM 工具由独立 AstrBot 插件维护。组件边界与版本见 [QQ 对接契约](docs/QQ_BOT_INTEGRATION.md)，安全变化与部署门槛见 [安全审查](docs/SECURITY_REVIEW.md)。
v3 外部身份接口与旧绑定隔离方式见 [后端契约](docs/QQ_HOMEWORK_V3_API.md)；机器人指令和回复草案位于独立插件项目。

## 开发与验证

```sh
go test ./...
go test -race ./internal/api ./internal/db ./internal/rule ./internal/parser
go build ./cmd/server
```

`cmd/e2e_test` 使用合成花名册和邮件；测试不会连接真实邮箱。`data/` 与运行时凭据不进入版本控制。`go.mod` 至少要求 Go 1.26.9；Flake 对锁定的 Nixpkgs 单独固定 Go 1.26.9。源码版本不代表现网二进制版本，部署前应检查 `go version -m`。

## 配置

| 环境变量 | 参数 | 默认值 / 用途 |
| --- | --- | --- |
| `IMAP_SERVER` | `-imap-server` | `imap.gmail.com:993` |
| `IMAP_USER` | `-imap-user` | 邮箱账号，运行时注入 |
| `IMAP_PASSWORD` | `-imap-password` | 应用密码，运行时注入 |
| `DATA_DIR` | `-data-dir` | `./data` |
| `HTTP_PORT` | `-http-port` | `8080` |
| `API_KEY` | `-api-key` | 管理级 API key；未配置时 `/api/*` 返回 503 |

优先使用部署系统的凭据注入，避免把密码写入命令行。HTTP 服务应位于受限网络和 HTTPS 反向代理之后；不要直接公开监听端口。公共 `/health` 和 `/docs` 只用于健康检查与文档。

## 花名册与规则

例如 `data/rosters/example.csv`：

```csv
学号,姓名,班级
990000000001,示例甲,示例班A
990000000002,示例乙,示例班A
```

例如 `data/rules/example_lab.yaml`：

```yaml
id: example_lab
name: 示例实验
rosters: [rosters/example.csv]
deadline: '2030-01-01T18:00:00+08:00'
patterns:
  subject_regex: '^示例实验-(?P<student_id>\d{12})-(?P<name>[\p{Han}]+)$'
target_filename: '作业-{class}-{student_id}-{name}.{ext}'
```

班级以花名册为准，不再根据学号前缀或班名中的数字推测。API 创建规则必须显式提供名单路径，规则 ID 仅允许 ASCII 字母、数字、下划线和短横线，名单路径必须留在数据目录中。

## HTTP API

请求使用 `X-API-Key` 或 `Authorization: Bearer`。URL 查询参数 `token` 和 `api_key` 不再接受；内置下载控制台通过请求头下载，不保存令牌。

```sh
curl --fail -H "X-API-Key: $API_KEY" \
  http://localhost:8080/api/assignments
curl --fail -H "X-API-Key: $API_KEY" \
  http://localhost:8080/api/assignments/example_lab/status
curl --fail -H "X-API-Key: $API_KEY" \
  http://localhost:8080/api/assignments/example_lab/export -o homework.zip
```

| 接口 | 用途 |
| --- | --- |
| `GET /api/assignments` | 作业列表 |
| `GET /api/assignments/{id}/status` | 汇总统计 |
| `GET /api/assignments/{id}/missing` | 未交名单，仅供受信管理员客户端 |
| `GET /api/assignments/{id}/submissions` | 提交列表 |
| `GET /api/assignments/{id}/submissions/{student_id}/history` | 个人历史 |
| `GET /api/assignments/{id}/export` | 作业归档 |
| `GET /api/assignments/export/all` | 全部归档 |
| `POST /api/assignments/{id}/upload` | 受信客户端上传 |
| `GET/POST/DELETE /api/bindings[/{qq_id}]` | 身份映射 |
| `POST /api/assignments` | 创建作业规则 |

API key 持有者拥有全课程权限。服务不把请求中的 QQ 号当作已认证终端用户；客户端必须独立验证平台来源、角色和本人身份。创建绑定需 `force: true`，该字段仅表示受信管理客户端已人工批准，不是新的认证凭据。

邮件超大附件只下载 QQ 域名的 HTTPS 地址，并在连接时拒绝私有地址；不会使用环境 HTTP 代理。新增 CDN、代理或文件协议支持必须重新审查网络边界。

## NixOS

`flake.nix` 提供软件包和 `services.gmail-archiver` 模块。模块与宿主锁文件、反向代理、凭据注入由部署仓库维护。变更后先构建并核对实际 Go 版本，再单独安排带回退的部署；测试通过不代表新二进制已运行。

需要在受限环境构建而不传递本仓库旧 Git 历史和测试数据时，可运行 `python3 scripts/build_release_source.py /tmp/gmail-archiver-source.tar.gz`。源码包仅含服务端运行源码、Go 依赖声明和 Flake 文件；仍须在部署端核对包哈希、构建结果与运行版本，不能用它替代生产数据库备份。
