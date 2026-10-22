# QQ 机器人接口契约

课程插件已迁移至独立仓库 `astrbot-plugin-gmail-homework`。本仓库只维护归档 API，不再包含 AstrBot 插件源码或它的运行时配置。

| 项目 | 固定基线 |
| --- | --- |
| 插件仓库 | `astrbot-plugin-gmail-homework`，当前仅本地独立仓库，未发布远端 |
| 插件版本 | `2.0.0` |
| 插件提交 | `38b2dd8c4762d5fa2b45faff2d9ecefe4e151300` |
| 后端安全接口基线 | `f34a7eece6a37e8800f95cb16d3724dc536c274b` |
| 后端 Go 1.26.9 构建基线 | `7c4f159e7958283a59fa834b676cb884c0080a9a` |
| 接口修订 | `qq-homework-v2` |

机器可读引用见 [qq-bot-plugin.json](qq-bot-plugin.json)。发布或安装来源变更时，先审查固定提交，再更新引用；不能据此假定现网已经升级。

## 组件职责

```text
QQ -> SnowLuma / OneBot v11 -> AstrBot + 独立插件 -> HTTPS -> gmail-archiver
```

- OneBot 与 AstrBot：平台连接认证、事件来源、会话隔离、模型配置、日志与登录卷。
- 插件：可信平台及机器人匹配、管理员/助教权限、私聊身份申请与人工绑定批准、私聊/群聊隔离、LLM 工具权限、文件接收与导出副本。
- 服务：管理级 API 鉴权、花名册校验、身份映射、邮件归档、统计和导出。
- 部署仓库：端点、QQ/群号、模型与 API key、容器镜像、共享目录、反向代理、防火墙和恢复备份。

API key 是整个服务的管理凭据，服务不认证 QQ 终端用户。不得向学生、群聊、LLM 或 OneBot 文件下载 URL 提供该 key。仅接受 `X-API-Key` 或 Bearer 请求头；未配置时拒绝 `/api/*`。

## 使用的接口

| 方法与路径 | 必需语义 |
| --- | --- |
| `GET /api/assignments` | 返回含 `id`、`name`、`deadline` 的作业数组 |
| `GET /api/assignments/{id}/status` | 汇总统计，不包含名单 |
| `GET /api/assignments/{id}/missing` | `missing_list`，仅管理私聊工具使用 |
| `GET /api/assignments/{id}/submissions` | `submissions`；插件须约束本人或管理员查询 |
| `GET /api/assignments/{id}/submissions/{student_id}/history` | 个人版本历史 |
| `GET /api/roster` | 插件内部匹配私聊申请及管理私聊读取；不得将名单返回申请人 |
| `GET /api/bindings` | 仅管理私聊流程读取 |
| `GET /api/bindings/{qq_id}` | 当前账号映射；不存在时返回错误 |
| `POST /api/bindings` | JSON 含 `qq_id`、`student_id`、`student_name`、`force: true`；姓名核验后返回 `success` 与 `binding` |
| `DELETE /api/bindings/{qq_id}` | 仅人工管理操作 |
| `POST /api/assignments` | 明确规则 ID、名称、名单相对路径与解析规则，不推测班级 |
| `POST /api/assignments/{id}/upload` | multipart 文件及已核验绑定字段，单请求最多 100 MiB |
| `GET /api/assignments/{id}/export`、`GET /api/assignments/export/all` | 归档流，仅管理员私聊下载 |
| `GET /api/students/{student_id}/export` | 个人全学期归档 |
| `GET /api/assignments/{id}/plagiarism` | 仅管理私聊查重 |

`force: true` 是受信管理客户端的批准标志，不是身份凭据。旧客户端的自助绑定及查询参数 token 不兼容；应同步升级后端与插件。

未绑定学生私聊发送含学号姓名的文件、`/绑定 <学号> <姓名>` 或加好友后的第一条私聊文字时，插件可按花名册生成待审核申请，不暂存或上传文件，也不暴露匹配结果。助教用 `/待审核` 查看申请，独立核验 QQ 账号归属后才用 `/指定绑定` 批准。好友申请附言是否到达 AstrBot 插件尚未验证；当前实现处理私聊消息。历史绑定需人工复核，不能因接口升级就认定旧记录可信。

## 部署交接

独立插件的 README.md 是运行配置和共享目录的主要说明。本仓库不保存真实地址、QQ/群号、名单或凭据。运行前必须验证可信平台实例、OneBot token、HTTPS、同路径共享目录和实际框架版本。

当前仅完成源码迁移和合成测试。Go 工具链漏洞、历史隐私、兼容性验收及回退门槛统一见 [安全审查](SECURITY_REVIEW.md)。尚不应自动部署、覆盖运行时配置或重建登录容器。

## 固定版本迁移与回退

1. 先记录现网后端二进制、插件代码、AstrBot/SnowLuma 镜像版本与运行时目录的归属，并在受限位置备份后端数据、`plugin_config.json`、统计库、绑定申请库和旧插件。QQ 登录卷只由原部署系统保管。
2. 在隔离环境使用本仓库的构建基线提交或其后代构建后端，检查 `go version -m <二进制>` 中的编译器版本至少为 Go 1.26.9。按上表固定提交导出独立插件源码，以 `astrbot_plugin_gmail_homework` 目录名安装；不要从已删除的 `plugins/` 路径复制旧代码。
3. 按独立插件的 README 配置可信平台、机器人、收件与共享传输目录、课程名单。先用合成事件验证管理员和普通用户在私聊及群聊的允许/拒绝路径、身份申请与助教审核、导出、文件上传、模型工具和失败恢复；再核对 OneBot 上传动作在副本删除前已经读取文件。
4. 后端旧版允许查询参数 token 与自助绑定，新版不允许。后端与插件须作为同一次维护切换处理；单独替换其中一个可能使旧客户端失效。切换后核对实际运行二进制和插件版本、反向代理访问策略、历史绑定及课程数据，不以构建成功代替运行验收。
5. 若兼容性验收失败，停止课程插件并成对恢复备份的后端与插件以及原运行时配置；不要重建 QQ 登录卷。旧版含已知权限漏洞，恢复期间需限制外部访问并安排修复。若只需撤销源码修改，可在本地从保留的 `b1df923` 恢复旧实现，但这不会回滚线上数据或消除旧漏洞。
