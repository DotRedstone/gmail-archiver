# gmail-archiver

轻量、稳健、低内存占用的 Gmail 附件实时归档与高校作业自动统计系统。

基于纯 Go 静态编译（无 CGO 依赖），通过 IMAP IDLE 协议长连接 7×24 小时监听新邮件，**自动解析标准 MIME 附件及腾讯 QQ 邮箱「超大附件/文件中转站」并流式落盘**。支持**名单（CSV）与作业规则（YAML）彻底解耦**、**重复提交自动递增更正并覆盖**，提供带分页、检索、断点续传及作业催收导出的 RESTful HTTP API，原生支持 NixOS 声明式 Systemd 守护进程与 QQ 机器人联动。

---

## 核心特性

- **极致轻量**：纯静态二进制文件（约 13MB），常驻后台内存仅 **10~20MB**（适合低配 VPS 7×24h 守护）。
- **IMAP IDLE 实时监听与断线自愈**：毫秒级捕获新邮件，支持长连接保活心跳与指数退避断线自动重连。
- **批量同步（Batch Fetch）**：初次同步与增量同步采用分批并发拉取，上百封历史邮件 10 秒内极速处理完毕。
- **QQ 邮箱超大附件原生解析**：国内高校学生大量使用 QQ 邮箱发送超大附件，系统自动从邮件 HTML 中提取腾讯中转站直链并免登录流式下载落地，彻底解决无 MIME 附件头的漏收问题。
- **花名册与作业规则解耦**：
  - 名单（CSV）按班级独立维护，多门课程随意复用；
  - 规则（YAML）按实验/大作业独立配置，支持截止时间、容错正则与统一规范命名模板。
- **后继提交更正覆盖机制**：
  - 同一学生重复交作业自动递增版本号（v1 $\to$ v2 $\to$ v3）；
  - 自动更新最新有效标识（`is_latest = true`），历史版本在数据库与磁盘中完整保留可追溯；
  - 一键打包导出时仅包含每位学生的最新有效版本，解压无多余重名文件。
- **丰富的 RESTful API**：
  - 健康检查与 IMAP 实时连接状态查询；
  - 附件列表模糊搜索、分页与 HTTP Range 断点续传流式下载；
  - 作业总览（应交/实交/迟交/提交率）；
  - 催交清单导出（直接输出未交学号与姓名）；
  - 学生提交历史与多版本时间线溯源；
  - 整班作业规范化重命名打包 Zip 一键下载。
- **Nix 原生支持**：提供根目录 `flake.nix`，支持 `nix build` 确定性构建与 NixOS 声明式 Systemd 部署。

---

## 目录结构

```text
.
├── cmd/
│   └── server/
│       └── main.go              # 服务启动入口、配置解析、优雅退出 (Graceful Shutdown)
├── internal/
│   ├── config/                  # 环境变量 / 命令行参数加载与校验
│   ├── imap/                    # IMAP 连接管理、IDLE 长连接循环、批量快速拉取与自愈
│   ├── parser/                  # 邮件 MIME 树解析、QQ 超大附件免登录下载、文件名解码
│   ├── storage/                 # 文件落盘存储引擎（按 YYYY/MM 目录分层归档，防目录穿越）
│   ├── db/                      # 嵌入式 SQLite 模型、自动迁移、版本更正覆盖机制
│   ├── roster/                  # 学生花名册解析引擎（独立 CSV、字段自动映射与索引）
│   ├── rule/                    # 作业规则正则提取引擎（独立 YAML、多源容错与规范化重命名）
│   └── api/                     # REST HTTP API 路由与处理器、统计汇总与打包导出
├── data/
│   ├── rosters/                 # 班级花名册库 (*.csv)
│   └── rules/                   # 课程作业规则库 (*.yaml)
├── docs/
│   └── QQ_BOT_INTEGRATION.md    # QQ 机器人对接指南（群内催交 / 自助查作业）
├── flake.nix                    # Nix 构建与 NixOS Systemd 部署模块
├── go.mod
├── go.sum
└── README.md
```

---

## 快速上手

### 1. 申请 Gmail 应用专用密码

由于 Gmail 开启两步验证后禁止使用主密码登录 IMAP，需生成独立的 16 位应用密码：
1. 打开 [Google 账号中心 - 安全性](https://myaccount.google.com/security)；
2. 确保已开启 **两步验证 (2-Step Verification)**；
3. 在安全性页面搜索框输入 **应用专用密码 (App Passwords)**；
4. 创建一个名称为 `gmail-archiver` 的密码，复制生成的 16 位字符（例如：`abcd efgh ijkl mnop`）。

### 2. 本地运行

支持通过命令行参数或环境变量启动：

```bash
# 整理并拉取依赖
go mod download

# 启动服务
go run cmd/server/main.go \
  -imap-user "your_email@gmail.com" \
  -imap-password "your_16_digit_app_password" \
  -http-port 8080 \
  -data-dir "./data"
```

或使用预编译二进制：
```bash
go build -o bin/gmail-archiver cmd/server/main.go
./bin/gmail-archiver -imap-user "your_email@gmail.com" -imap-password "your_password"
```

### 3. 配置参数一览

| 环境变量 | 命令行参数 | 默认值 | 说明 |
| :--- | :--- | :--- | :--- |
| `IMAP_SERVER` | `-imap-server` | `imap.gmail.com:993` | IMAP 服务器地址 |
| `IMAP_USER` | `-imap-user` | *(必填)* | Gmail 邮箱地址 |
| `IMAP_PASSWORD` | `-imap-password` | *(必填)* | Google 16 位应用专用密码 |
| `DATA_DIR` | `-data-dir` | `./data` | 数据主目录（包含附件与 SQLite 数据库） |
| `HTTP_PORT` | `-http-port` | `8080` | REST API 监听端口 |
| `API_KEY` | `-api-key` | *(空)* | 可选的 API 访问秘钥（请求需携带 `X-API-Key`） |

---

## 名单与作业规则配置教程

`gmail-archiver` 将“谁在上课”和“收什么作业”完全解耦，后续每收一次新作业或新实验，**只需新建一个 YAML 文件**，无需修改任何代码。

### 1. 配置学生名单（CSV）

将班级花名册放入 `data/rosters/` 目录下（如 `data/rosters/2024_cs_5.csv`）。

**CSV 格式规范**：
- 支持标准表头：`学号,姓名,班级,性别`（列顺序可任意，系统会自动根据表头名称自适应映射）。
- 示例内容：
  ```csv
  学号,姓名,班级,性别
  240809010501,支全振,2024级计算机科学与技术5班,男
  240809010502,马祥宇,2024级计算机科学与技术5班,男
  240809010506,王鹏宇,2024级计算机科学与技术5班,男
  ```

### 2. 配置作业规则（YAML）

在 `data/rules/` 目录下为具体作业创建规则文件，例如 `parallel_computing_lab1.yaml`：

```yaml
id: "parallel_computing_lab1"             # 作业唯一标识（对应 API 中的 {id}）
name: "并行计算实验1"                      # 作业名称
deadline: "2026-09-23T18:00:00+08:00"     # 截止时间（符合 RFC 3339 格式）

# 关联名单文件 (相对 data/ 路径，可关联多个班级)
rosters:
  - "rosters/2024_cs_5.csv"
  - "rosters/2024_green_compute_1.csv"

# 命名与正文提取正则 (推荐采用容错模式)
patterns:
  # 主题匹配：兼容 "实验1"、"实验一"、各类连接符 (- _ — + 空格) 及纯姓名
  subject_regex: "^(?:并行计算[-_—+\\s]*)?(?:实验[1一]?|24\\d)?[-_—+\\s]*(?:(?P<class>[\\w\\p{Han}]+)[-_—+\\s]+)?(?:(?P<student_id>\\d{12})[-_—+\\s]*)?(?P<name>[\\p{Han}\\w]+)$"

  # 附件匹配：必须是压缩包，兼容中文数字及学生漏写分隔符情况
  attachment_regex: "^(?:并行计算[-_—+\\s]*)?实验[1一][-_—+\\s]*(?:(?P<class>[\\w\\p{Han}]+)[-_—+\\s]+)?(?:(?P<student_id>\\d{12})[-_—+\\s]*)?(?P<name>[\\p{Han}\\w]+)?\\.(?P<ext>zip|rar|7z|tar\\.gz)$"

  # 邮件正文提取：学生附件名未带学号时，从正文补充提取学号与班级
  body_regexes:
    - "姓名[：:]\\s*(?P<name>[\\p{Han}\\w]+)"
    - "学号[：:]\\s*(?P<student_id>\\d{12})"
    - "班级[：:]\\s*(?P<class>[\\w\\p{Han}]+)"

# 一键导出规范化重命名模板（解压后格式绝对工整）
target_filename: "实验1-{class}-{student_id}-{name}.{ext}"
```

> [!TIP]
> **容错建议**：大学生提交邮件时常把阿拉伯数字写成中文数字（`实验1` 写成 `实验一`）、破折号用全角（`—`）或加号（`+`）。上面的正则表达式已经过 190+ 封真实学生邮件验证，建议作为标准模板复用。

---

## 在线接口文档与实战 API

### 1. 在线接口文档与下载控制台 (Web UI)

服务内置了交互式 Web 接口文档与导出控制台，**直接在浏览器中访问根路径即可查看**：
- 🌐 访问地址：`http://localhost:8080/` 或 `http://localhost:8080/docs`
- 🔑 **动态 Token 注入**：控制台顶部提供 Token 调试栏，填入 Token 后，页面内所有**一键下载按钮**与 cURL 命令均会自动拼接鉴权参数，方便老师或助教在浏览器中一键点击直链下载。

---

### 2. Token 权限鉴权（支持浏览器直链点击）

若启动时通过 `-api-key "your_secret_token"` 或环境变量 `API_KEY` 配置了权限令牌，系统支持以下**任意一种**方式鉴权：

1. **URL Query 参数（推荐浏览器点击直链或微信/QQ发送）**：
   - `?token=your_secret_token`
   - 或 `?api_key=your_secret_token`
2. **HTTP Header**：
   - `X-API-Key: your_secret_token`
3. **Bearer Token**：
   - `Authorization: Bearer your_secret_token`

---

### 3. 多维作业下载与打包（覆盖高校教学 4 大典型场景）

#### 场景 1：单次作业全员打包 (Zip)
一次性下载某一次作业（如实验1）的全班学生最新有效作业，自动规范重命名并打包为一个 Zip：
```bash
curl -s "http://localhost:8080/api/assignments/parallel_computing_lab1/export?token=your_token" -o 并行计算实验1_全员作业.zip
```

#### 场景 2：单次作业指定学生单独下载
单人复查某次作业时，直接下载该学生提交的最新文件（以规范文件名流式下载）：
```bash
curl -s "http://localhost:8080/api/assignments/parallel_computing_lab1/submissions/240809010501/download?token=your_token" -O
```

#### 场景 3：整学期全部作业全员总打包 (Zip)
一键打包下载截止目前所有已开设作业（实验1、实验2、实验3...）的全部学生有效文件，按作业目录自动分类打包为一个 Zip：
```bash
curl -s "http://localhost:8080/api/assignments/export/all?token=your_token" -o 整学期全量作业归档.zip
```

#### 场景 4：单人纵向全学期所有作业总打包 (Zip)
期末复查某位学生平时成绩时，纵向提取该学生截止目前提交的全部课程作业，打包为 `{学号}_{姓名}_全部作业.zip`：
```bash
curl -s "http://localhost:8080/api/students/240809010501/export?token=your_token" -o 240809010501_支全振_全部作业.zip
```

---

### 4. 统计分析与催收 API

#### 查看作业整体进度（应交/实交/提交率）
```bash
curl -s "http://localhost:8080/api/assignments/parallel_computing_lab1/status?token=your_token" | jq .
```
**输出示例**：
```json
{
  "assignment_id": "parallel_computing_lab1",
  "assignment_name": "并行计算实验1",
  "deadline": "2026-09-23T10:00:00Z",
  "total_expected": 47,
  "submitted_count": 46,
  "missing_count": 1,
  "late_count": 4,
  "submission_rate": "97.9%"
}
```

#### 一键提取未交名单（催交神器）
```bash
# 获取未交完整 JSON
curl -s "http://localhost:8080/api/assignments/parallel_computing_lab1/missing?token=your_token" | jq .

# 单行提取未交学生姓名与学号，直接复制发到 QQ/微信群催交
curl -s "http://localhost:8080/api/assignments/parallel_computing_lab1/missing?token=your_token" | \
  jq -r '.missing_list[] | "\(.name) (\(.student_id))"'
```

#### 查看单个学生提交历史（版本更正核验与时间线）
```bash
curl -s "http://localhost:8080/api/assignments/parallel_computing_lab1/submissions/240809010501/history?token=your_token" | jq .
```
可查看到该学生历次提交的时间、对应文件名及当前唯一有效的版本（`is_latest = true`）。

---

## QQ 机器人无缝接入

服务原生设计的 REST API 非常容易与 QQ 群机器人打通：
- 在班级群发送 `/查作业` 实时通报交件率；
- 在班级群发送 `/催交` 自动列出未交名单；
- 学生私聊机器人发送 `/我的作业` 查询收件状态。

完整选型分析（基于 **NapCatQQ + OneBot v11** 的无头 QQ 小号方案）与开箱即用的 Python 联动脚本，详见文档：
👉 [QQ 机器人对接指南 (docs/QQ_BOT_INTEGRATION.md)](file:///home/dot/Projects/gmail-archiver/docs/QQ_BOT_INTEGRATION.md)

---

## 生产环境部署（NixOS / Systemd）

### 1. Nix 确定性构建
```bash
nix build
./result/bin/gmail-archiver -h
```

### 2. NixOS 声明式部署
在 NixOS 配置中引入本模块：
```nix
{
  services.gmail-archiver = {
    enable = true;
    imapUser = "your_email@gmail.com";
    passwordFile = "/run/secrets/gmail_app_password"; # 建议使用 sops-nix 注入凭据
    httpPort = 8080;
    dataDir = "/var/lib/gmail-archiver";
  };
}
```
Systemd 模块已内置 `DynamicUser`、权限沙箱隔离、自动重启与状态守护。

---

## 质量与测试

运行完整自动化测试套件：
```bash
go test -v ./...
```
包含 SQLite 并发写入、更正提交多版本递增覆盖、MIME RFC 2047 解码以及 QQ 超大附件接口模拟测试。
