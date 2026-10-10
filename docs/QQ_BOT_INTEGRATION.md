# QQ 机器人集成指南

本系统通过 **SnowLuma + AstrBot + OneBot v11** 提供课程作业机器人：邮件归档服务负责保存和核验作业，AstrBot 负责对话、权限与技能，SnowLuma 负责 QQ 登录及 OneBot 通信。

本文只描述可复用的部署结构；域名、主机名、QQ 号、群号、花名册和凭据均应保存在运行时配置或密钥管理系统中，不应写入仓库。

## 架构

```text
[学生邮件 / QQ 私聊 / 课程群]
              |
              v
       [ SnowLuma ] -- OneBot v11 WebSocket --> [ AstrBot ]
                                                     |
                                                     | HTTPS + API key
                                                     v
                                             [ gmail-archiver API ]
```

- SnowLuma 保存 QQ 会话，并向 AstrBot 发起 OneBot 反向 WebSocket 连接。
- AstrBot 运行 `astrbot_plugin_gmail_homework`，把课程数据操作交给 `gmail-archiver` API。
- 对外仅按需暴露管理界面；OneBot WebSocket 保持在容器网络或回环地址，避免直接暴露。

## 对话与权限边界

- 陌生人私聊必须先执行 `/绑定 <学号> <姓名>`，并由后端按花名册核验；绑定成功前绝不进入 LLM。
- 只有配置过的课程群在 `@机器人` 时才进入 LLM；其他群消息直接忽略，避免大群闲聊和误触发。
- LLM 是默认对话底座，可处理问候、日常问题、学习讨论和课程问题。绑定、上传、导出、权限管理和作业状态查询等确定性流程以指令/正则技能执行；它们不是日常对话的低配模式。
- 普通文本始终先进入 LLM；只有显式 `/命令`、附件上传及已开始的确认流程直接运行确定性技能。模型连接失败、空响应或不安全响应时，才回退为本地技能提示。
- “我是谁”之类的身份问题只在已绑定的私聊中由模型调用本人身份工具回答；群聊中不会查询或披露身份信息。
- 频率限制仍会阻断明显滥用。模型连接或响应异常时，机器人只返回本地技能提示，绝不把上游 JSON、密钥或报错原文发给用户。

## 运营统计与告警

管理员私聊发送 `/会话统计 [YYYY-MM-DD]` 可查看每日模型请求、未绑定拦截、群策略拦截、限流和模型异常计数。

统计库只保存时间、发送者/群的标识、处理方式和原因，不保存消息正文、姓名、学号或模型回复。高频触发、模型异常与单账号日调用量达到告警阈值时，机器人会向管理员私聊告警；达到阈值只告警，不会自动关闭正常 LLM 会话。

## 运行时配置

以下变量应由容器编排、systemd credential 或密钥管理工具注入，不能提交到仓库：

```text
GMAIL_ARCHIVER_API_BASE=https://<archiver-host>
GMAIL_ARCHIVER_API_KEY=<api-key>
GMAIL_ARCHIVER_SUPER_ADMIN_QQ=<owner-qq>
```

环境变量优先。无法由容器注入时，插件也只会从权限受限的运行时 `plugin_config.json` 读取 `api_base` 和 `super_admin_qq`；该文件至少包含课程群和助教配置，绝不能进入版本控制。可以按需加入以下会话告警设置：

```json
{
  "conversation": {
    "daily_llm_alert_threshold": 100,
    "alert_cooldown_seconds": 1800
  }
}
```

`daily_llm_alert_threshold` 仅用于通知管理员，不是调用额度。

## 维护要点

- 更新插件时保留 `plugin_config.json` 和 `conversation_stats.sqlite3`，否则会丢失班群/助教配置或历史统计。
- 重启 AstrBot 后检查 OneBot 已重新连接；无需因插件更新重启 SnowLuma。
- SnowLuma 登录态位于持久化卷。不要在日志、截图、文档或 Git 提交中公开二维码、会话文件、设备标识或 QQ 账号。
- 对外代理、TLS 证书和 DNS 由部署环境维护；本文不记录任何实际地址或端口映射。
