# gmail-archiver

轻量、稳定、低内存占用的 Gmail 附件实时归档与 REST API 查询服务。

基于 Go 纯静态编译（无 CGO 依赖），通过 IMAP IDLE 协议长连接 7×24 小时监听新邮件，自动提取邮件附件并安全落盘归档，同时使用嵌入式 SQLite 记录元数据，提供带分页、关键词检索、断点续传（HTTP Range）的 RESTful HTTP API。原生提供 Nix Flake 构建与开箱即用的 NixOS 守护进程模块。

---

## 特性亮点

- **极致轻量**：纯 Go 静态二进制文件（约 13MB），无动态链接库依赖，常驻运行内存仅约 10~20MB。
- **IMAP IDLE 实时监听与断线自愈**：毫秒级响应新邮件事件；内置定期保活刷新（15~20分钟）与指数退避断线重连机制，网络抖动自动恢复。
- **断点续收 / 增量同步**：记录已同步邮件的 UID，服务重启或维护期间不漏收、不重收。
- **MIME 多编码自动解析**：完整支持 RFC 2047（UTF-8, GBK, Big5 等）编码文件名解码与 RFC 2231 参数解析，正确识别附件与带文件名的 inline 资源。
- **SHA-256 去重与安全落盘**：文件内容计算哈希，去重存储；物理路径按 `attachments/YYYY/MM/{hash}_{filename}` 分层存放；内置严格的路径清洗与防目录穿越校验。
- **RESTful HTTP API**：
  - `/health`：服务存活与 IMAP 实时连接状态检测。
  - `/api/attachments`：支持文件名/主题模糊检索、发件人过滤、时间范围过滤及分页。
  - `/api/attachments/{id}/download`：文件流式下载，原生支持 HTTP Range 断点续传，符合 RFC 5987/6266 标准的 UTF-8 编码响应头。
  - **API Key 认证**：支持可选的 `X-API-Key` 请求头或 `Authorization: Bearer <token>` 保护。
- **Nix 原生支持**：提供根目录 `flake.nix`，支持 `nix build` 确定性打包，并暴露 `nixosModules.default` 支持在 NixOS 上声明式部署及 Systemd 安全加固。

---

## 目录结构

```text
.
├── cmd/
│   └── server/
│       └── main.go              # 服务启动入口、配置解析、优雅退出 (Graceful Shutdown)
├── internal/
│   ├── config/                  # 环境变量 / 命令行参数加载与校验
│   ├── imap/                    # IMAP 连接管理、IDLE 长连接循环、心跳与断线重连
│   ├── parser/                  # 邮件 MIME 树解析、附件提取、文件名解码
│   ├── storage/                 # 文件落盘存储引擎（按 YYYY/MM 目录分层归档，防目录穿越）
│   ├── db/                      # 嵌入式 SQLite 模型、自动迁移 (Migration)、CRUD 查询
│   └── api/                     # REST HTTP API 路由与处理器、下载流式传输与鉴权
├── flake.nix                    # Nix 构建与 NixOS Systemd 部署模块
├── flake.lock
├── go.mod
├── go.sum
└── README.md
```

---

## 配置参数

支持通过**环境变量**或**命令行参数**配置：

| 环境变量 | 命令行参数 | 默认值 | 说明 |
| :--- | :--- | :--- | :--- |
| `IMAP_SERVER` | `-imap-server` | `imap.gmail.com:993` | IMAP 服务器地址 |
| `IMAP_USER` | `-imap-user` | *(必填)* | Gmail 邮箱地址 |
| `IMAP_PASSWORD` | `-imap-password` | *(必填)* | Google「应用专用密码」(App Password) |
| `DATA_DIR` | `-data-dir` | `./data` | 数据存储目录（存放 SQLite 数据库与附件） |
| `HTTP_PORT` | `-http-port` | `8080` | HTTP API 监听端口 |
| `API_KEY` | `-api-key` | *(空)* | 可选的 API 访问秘钥 |

---

## 快速开始

### 1. 准备 Gmail 应用专用密码

1. 进入 [Google 账号中心 - 安全性](https://myaccount.google.com/security)；
2. 确保已开启 **两步验证 (2-Step Verification)**；
3. 在安全性页面搜索 **应用专用密码 (App Passwords)**；
4. 创建一个名为 `gmail-archiver` 的密码，获得 16 位专用密码（例如：`abcd efgh ijkl mnop`）。

### 2. 使用 Go 本地运行

```bash
# 整理并拉取依赖
go mod download

# 启动服务
IMAP_USER="your_email@gmail.com" \
IMAP_PASSWORD="your-app-password" \
HTTP_PORT=8080 \
DATA_DIR="./data" \
go run cmd/server/main.go
```

或使用命令行参数：

```bash
go run cmd/server/main.go \
  -imap-user "your_email@gmail.com" \
  -imap-password "your-app-password" \
  -http-port 8080 \
  -data-dir "./data"
```

### 3. 使用 Nix 构建静态二进制

```bash
# 构建二进制
nix build

# 二进制位于 ./result/bin/gmail-archiver
./result/bin/gmail-archiver -h
```

---

## RESTful API 接口说明

### 1. 健康检查

- **请求**：`GET /health`
- **鉴权**：公开接口，无需 API Key
- **响应示例**：
  ```json
  {
    "status": "ok",
    "uptime": "1h23m45s",
    "imap": {
      "connected": true,
      "state": "idle",
      "mailbox": "INBOX",
      "last_sync_time": "2026-10-08T10:15:30Z",
      "processed_count": 12
    }
  }
  ```

### 2. 检索附件列表

- **请求**：`GET /api/attachments`
- **鉴权**：若配置了 `API_KEY`，需携带请求头 `X-API-Key: <key>` 或 `Authorization: Bearer <key>`
- **Query 参数**：
  - `keyword`：按文件名或邮件主题模糊匹配
  - `sender`：按发件人模糊匹配
  - `from_date`：起始时间（支持 `YYYY-MM-DD` 或 RFC 3339 格式）
  - `to_date`：截止时间
  - `page`：页码（默认 `1`）
  - `limit`：每页条数（默认 `20`，最大 `100`）
- **响应示例**：
  ```json
  {
    "data": [
      {
        "id": 1,
        "message_id": "<d3f9b2@mail.gmail.com>",
        "sender": "Alice <alice@example.com>",
        "subject": "十月份月度财务报表",
        "received_at": "2026-10-08T09:30:00Z",
        "filename": "财务报表.xlsx",
        "file_size": 15420,
        "sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
        "mime_type": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
        "storage_path": "attachments/2026/10/e3b0c442..._财务报表.xlsx",
        "created_at": "2026-10-08T09:30:15Z"
      }
    ],
    "pagination": {
      "page": 1,
      "limit": 20,
      "total": 1,
      "total_pages": 1
    }
  }
  ```

### 3. 下载附件

- **请求**：`GET /api/attachments/{id}/download`
- **特性**：
  - 支持标准 HTTP Range 头分块/断点续传；
  - 自动设置 `Content-Disposition: attachment; filename="..."; filename*=UTF-8''...` 保证各种浏览器下的中文文件名正常解析；
  - 严格防路径穿越安全校验。

---

## NixOS 声明式部署指南

在宿主机的 `flake.nix` 中引入本仓库：

```nix
{
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    gmail-archiver.url = "github:dot/gmail-archiver";
    # sops-nix.url = "github:Mic92/sops-nix";
  };

  outputs = { self, nixpkgs, gmail-archiver, ... }: {
    nixosConfigurations.my-server = nixpkgs.lib.nixosSystem {
      system = "x86_64-linux";
      modules = [
        gmail-archiver.nixosModules.default
        {
          services.gmail-archiver = {
            enable = true;
            imapUser = "your_email@gmail.com";
            # 使用 sops-nix 注入密码文件：
            passwordFile = "/run/secrets/gmail_app_password";
            # 可选配置 API Key：
            # apiKeyFile = "/run/secrets/gmail_archiver_api_key";
            httpPort = 8080;
            dataDir = "/var/lib/gmail-archiver";
          };
        }
      ];
    };
  };
}
```

启用后，NixOS 将自动创建并管理 Systemd 守护进程：
- 采用 `DynamicUser = true` 与独立的隔离运行环境；
- 自动管理 `/var/lib/gmail-archiver` 数据目录权限；
- 支持开机自启与异常自动重启。

---

## 本地测试与验证

执行完整的自动化单元测试与集成测试：

```bash
go test -v ./...
```
