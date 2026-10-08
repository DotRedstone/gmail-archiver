# QQ 机器人对接指南 (QQ Bot Integration)

本服务提供标准化 RESTful API，天然契合与 QQ 机器人联动，实现：
- **群内实时催收**：群内发送指令自动统计提交率并输出未交名单。
- **学生自助查重/查收**：学生私聊机器人确认“我的作业是否收到、当前第几个版本”。
- **临期自动播报**：截止时间前定时提醒未交同学。

---

## 一、 方案选型与架构设计

### 官方机器人 vs QQ 小号方案

| 维度 | 方案 A：QQ 开放平台官方机器人 | 方案 B（强烈推荐）：QQ 小号 + NapCatQQ |
| :--- | :--- | :--- |
| **群聊权限** | 普通班级群/课程群**极难申请**，需企业认证与严格白名单审核 | 只要小号进群即可，无任何门槛 |
| **消息类型** | 受限严重，不支持自由 Markdown / 富文本 | 支持普通文本、@未交学生、表情、文件等 |
| **学生交互** | 只能使用预设指令模板 | 支持群聊指令、私聊查询、定时群广播 |
| **部署难度** | 需配置 Webhook 域名证书与回调验签 | 基于 **OneBot v11** 协议，开箱即用 |

> **结论**：用于班级收作业，**方案 B（QQ 小号 + NapCatQQ）是最成熟、低成本、高自由度的选择**。

---

## 二、 整体联动架构

```text
[ 学生发送邮件 (含QQ超大附件) ]
              ↓ IMAP IDLE (秒级监听)
    ┌─────────────────────────┐
    │     gmail-archiver      │ :8080 (REST API)
    └─────────────────────────┘
              ▲
              │ HTTP GET (/api/assignments/...)
              ▼
    ┌─────────────────────────┐
    │     QQ 机器人适配器      │ (轻量 Python / Go 脚本)
    └─────────────────────────┘
              ▲
              │ OneBot v11 (HTTP API / WebSocket)
              ▼
    ┌─────────────────────────┐
    │   NapCatQQ (无头客户端)  │ (登录 QQ 小号)
    └─────────────────────────┘
              ↕
        [ 课程 QQ 群 / 私聊 ]
```

---

## 三、 快速部署步骤

### 步骤 1：部署 NapCatQQ（QQ 小号无头端）

推荐使用 Docker 或原生 Shell 部署 NapCatQQ（内存仅约 100MB）：

```bash
# 使用 Docker 一键运行 NapCatQQ
docker run -d \
  --name napcat \
  --restart always \
  -p 3000:3000 \
  -p 3001:3001 \
  -e ACCOUNT=你的QQ小号 \
  -v ./napcat_config:/app/napcat/config \
  mlikiowa/napcat-docker:latest
```

启动后，访问 `http://localhost:6099/webui`（或查看日志），用手机 QQ 扫描二维码登录小号即可保持常驻。
NapCat 默认在 `http://127.0.0.1:3000` 暴露 OneBot v11 HTTP API。

---

### 步骤 2：运行机器人联动脚本

以下提供一个基于 Python 3（无需第三方依赖，仅使用内置标准库）的开箱即用机器人适配脚本：

创建脚本 `bot_adapter.py`：

```python
#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
[QQ Bot Adapter for gmail-archiver]
基于 OneBot v11 HTTP API 与 gmail-archiver REST API
"""

import json
import urllib.request
import urllib.parse
from http.server import HTTPServer, BaseHTTPRequestHandler

ARCHIVER_BASE = "http://127.0.0.1:8080"
ONEBOT_API_URL = "http://127.0.0.1:3000/send_msg"
DEFAULT_ASSIGNMENT = "parallel_computing_lab1"

def http_get(url):
    req = urllib.request.Request(url, headers={"User-Agent": "QQBot"})
    with urllib.request.urlopen(req, timeout=5) as resp:
        return json.loads(resp.read().decode("utf-8"))

def send_msg(target_type, target_id, message):
    payload = {
        "message_type": target_type,  # "group" or "private"
        "user_id" if target_type == "private" else "group_id": target_id,
        "message": message
    }
    req = urllib.request.Request(
        ONEBOT_API_URL,
        data=json.dumps(payload).encode("utf-8"),
        headers={"Content-Type": "application/json"}
    )
    try:
        with urllib.request.urlopen(req, timeout=5) as resp:
            pass
    except Exception as e:
        print(f"[Error] 发送消息失败: {e}")

class EventHandler(BaseHTTPRequestHandler):
    def do_POST(self):
        content_len = int(self.headers.get("Content-Length", 0))
        post_body = self.rfile.read(content_len)
        self.send_response(200)
        self.end_headers()

        event = json.loads(post_body.decode("utf-8"))
        post_type = event.get("post_type")
        if post_type != "message":
            return

        msg_type = event.get("message_type") # "group" / "private"
        sender_id = event.get("user_id")
        group_id = event.get("group_id")
        raw_msg = event.get("raw_message", "").strip()

        target_type = "group" if msg_type == "group" else "private"
        target_id = group_id if msg_type == "group" else sender_id

        # 指令 1: 查进度 / 查作业
        if raw_msg in ["!查作业", "/查作业", "!进度", "/进度"]:
            try:
                data = http_get(f"{ARCHIVER_BASE}/api/assignments/{DEFAULT_ASSIGNMENT}/status")
                reply = (
                    f"📊【{data['assignment_name']}】提交统计\n"
                    f"———————————————\n"
                    f"✅ 实交人数：{data['submitted_count']} / {data['total_expected']}\n"
                    f"📈 提交比例：{data['submission_rate']}\n"
                    f"⚠️ 迟交人数：{data['late_count']} 人\n"
                    f"⏳ 截止时间：{data['deadline'][:16].replace('T', ' ')}"
                )
                send_msg(target_type, target_id, reply)
            except Exception as e:
                send_msg(target_type, target_id, f"查询失败: {e}")

        # 指令 2: 查未交名单
        elif raw_msg in ["!未交", "/未交", "!催交", "/催交"]:
            try:
                data = http_get(f"{ARCHIVER_BASE}/api/assignments/{DEFAULT_ASSIGNMENT}/missing")
                missing_list = data.get("missing_list", [])
                if not missing_list:
                    reply = "🎉 太棒了！全班作业已全部交齐！"
                else:
                    lines = [f"📢【{data['assignment_name']}】未交名单 ({data['missing_count']}人)："]
                    for idx, st in enumerate(missing_list, 1):
                        lines.append(f"{idx}. {st['name']} ({st['student_id']})")
                    lines.append("\n请以上同学抓紧时间整理并发送邮件！")
                    reply = "\n".join(lines)
                send_msg(target_type, target_id, reply)
            except Exception as e:
                send_msg(target_type, target_id, f"查询未交名单失败: {e}")

if __name__ == "__main__":
    print("[Bot] QQ 机器人事件监听服务启动中 :5700 ...")
    server = HTTPServer(("0.0.0.0", 5700), EventHandler)
    server.serve_forever()
```

---

## 四、 核心功能与交互体验

1. **群内输入 `/查作业`**：
   ```text
   📊【并行计算实验1】提交统计
   ———————————————
   ✅ 实交人数：46 / 47
   📈 提交比例：97.9%
   ⚠️ 迟交人数：4 人
   ⏳ 截止时间：2026-09-23 18:00
   ```

2. **群内或私聊输入 `/未交`**：
   ```text
   📢【并行计算实验1】未交名单 (1人)：
   1. 明航宇 (240809010505)

   请以上同学抓紧时间整理并发送邮件！
   ```

3. **进阶扩展**：
   - **自动@提醒**：如果名单 CSV 中补充了学生的 QQ 号，可在机器人回复中拼接 `[CQ:at,qq=123456]`，直接在群内精准艾特未交同学。
   - **定时催交**：使用系统的 Linux Crontab，每天晚上固定调用发送接口，解放双手。
