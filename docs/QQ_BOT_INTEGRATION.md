# QQ 机器人对接指南 (QQ Bot Integration)

本系统支持通过 **NapCatQQ + AstrBot** 搭建 QQ 机器人，实现免人工干预的“邮件自动归档 -> 群内实时催交 -> 学生自助查收 -> 助教一键导出文件”完整闭环。

---

## 一、 系统架构

```text
[ 学生发送邮件 (含QQ超大附件) ]
              ↓ IMAP IDLE (秒级监听)
    ┌─────────────────────────┐
    │     gmail-archiver      │ (新加坡 Hopper :8080)
    │  https://gmail.bdot.in  │ (RESTful API & Web 控制台)
    └─────────────────────────┘
              ▲
              │ 强加密 HTTPS (跨洋安全通信)
              ▼
    ┌─────────────────────────┐
    │     AstrBot 机器人框架   │ (阿里云杭州 Repeater :6185)
    │  https://astrbot.bdot.in│ (Python 插件: astrbot_plugin_gmail_homework)
    └─────────────────────────┘
              ▲
              │ 反向 WebSocket (OneBot v11)
              ▼
    ┌─────────────────────────┐
    │    NapCatQQ 无头客户端  │ (阿里云杭州 Repeater :6099)
    │  https://napcat.bdot.in │ (QQ 协议栈，已持久化登录凭证)
    └─────────────────────────┘
              ↕
        [ 课程 QQ 群 / 私聊 ]
```

---

## 二、 核心功能与交互体验

### 1. 助教多轮交互导出作业（核心亮点）
助教在班级群或私聊中发送：
```text
/导出作业
```
机器人调用 API 实时列出可用作业菜单：
```text
📋【请选择要导出的作业归档】
━━━━━━━━━━━━━━━
1️⃣ 并行计算实验1
    • 作业标识：parallel_computing_lab1
    • 应交人数：47 人 / 截止 2026-09-23 10:00
0️⃣ 整学期全量作业归档（打包所有实验）
━━━━━━━━━━━━━━━
💡 请直接回复对应【数字序号】（如：1 或 0）
（回复 取消 可退出本次导出，60 秒内有效）
```
助教回复 `1`：
- 若在**群聊**中：机器人直接将 Zip 压缩包上传到**当前群的群文件空间**，全员可自由下载；
- 若在**私聊**中：机器人直接作为**私聊离线文件**发送给助教。

### 2. 班级群催交与学生查收
- **`/查作业`**：实时生成提交统计看板（交件率、迟交人数、截止时间）。
- **`/未交`**：生成未交名单（姓名、学号、班级），自动过滤助教本人。
- **`/查收 张三`**：学生核验个人作业是否收到、是否有效。

---

## 三、 阿里云 Repeater 部署与维护

### 1. 容器运行架构
运行在阿里云 Hangzhou（`repeater`），配置严格的内存限制，保护主机 Vaultwarden 服务：
- `astrbot`：内存上限 280MB，端口 6185（WebUI），通过自建 bridge 网络 `bot-net` 与 NapCat 互联；
- `napcat`：内存上限 320MB，端口 6099（WebUI），反向连接 `ws://astrbot:6199/ws`。

### 2. 宿主机服务维护脚本
在 `repeater` 的 `/opt/docker/scripts/` 中提供标准化运维命令：
- 查看机器人日志：`docker logs -f astrbot`
- 查看 QQ 端日志：`docker logs -f napcat`
- 重启套件：`docker restart astrbot napcat`

### 3. 数据持久化目录
- AstrBot 配置与插件：`/var/lib/astrbot/data`
- NapCat 凭据与 QQ 缓存：`/var/lib/napcat/qq` 与 `/var/lib/napcat/config`
