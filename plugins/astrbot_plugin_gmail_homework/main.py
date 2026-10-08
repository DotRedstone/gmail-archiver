import json
import time
import urllib.request
import urllib.parse
from astrbot.api.event import filter, AstrMessageEvent
from astrbot.api.star import register, Star

TA_STUDENT_ID = "240809010505"  # 明航宇（助教本人豁免）
TA_NAME = "明航宇"
API_BASE = "https://gmail.bdot.in"
DEFAULT_ASSIGNMENT = "parallel_computing_lab1"
ADMIN_QQ_LIST = ["1689491386"]  # 助教与管理员 QQ

# 会话状态缓存：(sender_id, group_id) -> {time: float, options: dict}
PENDING_EXPORT_SESSIONS = {}
SESSION_TIMEOUT = 60  # 状态有效时间 60 秒

def is_admin(event: AstrMessageEvent) -> bool:
    sender_id = str(event.get_sender_id())
    if sender_id in ADMIN_QQ_LIST:
        return True
    sender_obj = getattr(event.message_obj, "sender", None)
    if sender_obj and getattr(sender_obj, "role", "") in ["owner", "admin"]:
        return True
    return False

def api_get(endpoint):
    url = f"{API_BASE}{endpoint}"
    req = urllib.request.Request(url, headers={"User-Agent": "AstrBot"})
    with urllib.request.urlopen(req, timeout=10) as resp:
        return json.loads(resp.read().decode("utf-8"))

async def upload_file_action(event: AstrMessageEvent, download_url: str, filename: str, display_name: str):
    bot = getattr(event, "bot", None)
    if not bot:
        yield event.plain_result("❌ 内部错误：未能获取底层协议客户端。")
        return

    group_id = event.get_group_id()
    sender_id = event.get_sender_id()
    target_desc = f"群 {group_id} 的群文件" if group_id else "私聊会话"

    yield event.plain_result(f"⏳ 正在从云端打包【{display_name}】并上传至{target_desc}，请稍候...")

    try:
        if group_id:
            await bot.call_action(
                "upload_group_file",
                group_id=str(group_id),
                file=download_url,
                name=filename,
            )
            yield event.plain_result(f"✅ 作业归档【{filename}】已成功上传至本群群文件！全员均可下载。")
        else:
            await bot.call_action(
                "upload_private_file",
                user_id=str(sender_id),
                file=download_url,
                name=filename,
            )
            yield event.plain_result(f"✅ 作业归档【{filename}】已作为私聊文件发送给你！")
    except Exception as e:
        yield event.plain_result(f"❌ 上传文件失败: {e}\n💡 备用下载直链：{download_url}")

@register("gmail_homework", "DotRedstone", "Gmail 自动收作业与催交插件", "1.2.3")
class HomeworkPlugin(Star):
    def __init__(self, context):
        super().__init__(context)

    @filter.command("帮助", alias={"作业帮助"})
    async def help_cmd(self, event: AstrMessageEvent):
        """显示作业助手指令菜单"""
        msg = (
            "📖【作业助手指令列表】\n"
            "━━━━━━━━━━━━━━━\n"
            "🔹 学生与常规指令：\n"
            "1️⃣ /查作业 —— 查看当前作业提交进度与统计\n"
            "2️⃣ /未交 —— 查看当前未交作业的学生名单\n"
            "3️⃣ /查收 <姓名或学号> —— 自助查询个人作业是否成功接收归档\n"
            "    例：/查收 张三 或 /查收 24080901xxxx\n\n"
            "👑 助教/管理员专属指令：\n"
            "4️⃣ /导出作业 —— 列出所有可选作业菜单，回复序号打包发送 QQ 文件\n"
            "    (群聊直接发群文件，私聊发离线文件)\n"
            "5️⃣ /导出整学期 —— 一键打包整学期全部作业发送文件"
        )
        yield event.plain_result(msg)

    @filter.command("查作业")
    async def status_cmd(self, event: AstrMessageEvent):
        """查询当前作业提交总体进度"""
        try:
            data = api_get(f"/api/assignments/{DEFAULT_ASSIGNMENT}/status")
            msg = (
                f"📊【{data['assignment_name']}】提交统计\n"
                f"━━━━━━━━━━━━━━━\n"
                f"✅ 实交人数：{data['submitted_count']} / {data['total_expected']}\n"
                f"📈 提交比例：{data['submission_rate']}\n"
                f"⚠️ 迟交人数：{data['late_count']} 人\n"
                f"⏳ 截止时间：{data['deadline'][:16].replace('T', ' ')}"
            )
            yield event.plain_result(msg)
        except Exception as e:
            yield event.plain_result(f"❌ 查询作业状态失败: {e}")

    @filter.command("未交")
    async def missing_cmd(self, event: AstrMessageEvent):
        """查询未交学生名单"""
        try:
            data = api_get(f"/api/assignments/{DEFAULT_ASSIGNMENT}/missing")
            missing = data.get("missing_list") or []
            real_missing = [s for s in missing if s.get("student_id") != TA_STUDENT_ID and s.get("name") != TA_NAME]
            
            if not real_missing:
                yield event.plain_result(f"🎉 太棒了！【{data['assignment_name']}】除助教本人外，全班同学已全部提交完毕！")
                return

            lines = [f"📢【{data['assignment_name']}】未交名单（共 {len(real_missing)} 人）："]
            for idx, s in enumerate(real_missing, 1):
                lines.append(f"{idx}. {s['name']}（{s['student_id']}，{s['class_name']}）")
            lines.append("\n💡 请以上同学抓紧整理源码与实验报告并发送至邮箱！")
            yield event.plain_result("\n".join(lines))
        except Exception as e:
            yield event.plain_result(f"❌ 查询未交名单失败: {e}")

    @filter.command("查收")
    async def check_student(self, event: AstrMessageEvent, query: str = ""):
        """自助查询个人作业是否收到：/查收 张三 或 /查收 24080901xxxx"""
        query = query.strip()
        if not query:
            yield event.plain_result("💡 用法：/查收 <姓名或学号>，例如：/查收 张三")
            return

        try:
            data = api_get(f"/api/assignments/{DEFAULT_ASSIGNMENT}/missing")
            missing = data.get("missing_list") or []
            is_missing = any(s.get("student_id") == query or s.get("name") == query for s in missing)
            
            if query in [TA_STUDENT_ID, TA_NAME]:
                yield event.plain_result(f"👑 {TA_NAME} 为课程助教，无需提交作业。")
                return

            if is_missing:
                yield event.plain_result(f"⚠️ 未检索到 [{query}] 的有效作业提交，请确认邮件主题与附件命名规范是否正确。")
            else:
                yield event.plain_result(f"✅ [{query}] 的作业已成功接收并归档！")
        except Exception as e:
            yield event.plain_result(f"❌ 查询失败: {e}")

    @filter.command("导出作业", alias={"下载作业"})
    async def export_cmd(self, event: AstrMessageEvent, param: str = ""):
        """管理员导出作业归档（支持多轮序号选择或带参快捷导出）"""
        if not is_admin(event):
            yield event.plain_result("❌ 权限不足：作业归档导出仅限课程助教或管理员执行。")
            return

        param = param.strip()

        # 获取当前所有作业列表
        try:
            assignments = api_get("/api/assignments")
        except Exception as e:
            yield event.plain_result(f"❌ 获取作业列表失败: {e}")
            return

        # 构建选项字典：key -> (url, filename, display_name)
        options = {}
        for idx, a in enumerate(assignments, 1):
            options[str(idx)] = (
                f"{API_BASE}/api/assignments/{a['id']}/export",
                f"{a['name']}_全员作业.zip",
                a['name']
            )
            options[a['id']] = options[str(idx)]

        options["0"] = (
            f"{API_BASE}/api/assignments/export/all",
            "整学期全量作业归档.zip",
            "整学期全量作业"
        )
        options["all"] = options["0"]
        options["全部"] = options["0"]

        # 模式 1：带参直接导出
        if param and param in options:
            url, filename, display_name = options[param]
            async for res in upload_file_action(event, url, filename, display_name):
                yield res
            return

        # 模式 2：两步交互模式
        session_key = (str(event.get_sender_id()), str(event.get_group_id() or ""))
        PENDING_EXPORT_SESSIONS[session_key] = {
            "time": time.time(),
            "options": options,
        }

        menu_lines = [
            "📋【请选择要导出的作业归档】",
            "━━━━━━━━━━━━━━━"
        ]
        for idx, a in enumerate(assignments, 1):
            deadline_str = a.get('deadline', '')[:16].replace('T', ' ')
            menu_lines.append(f"{idx}️⃣ {a['name']}")
            menu_lines.append(f"    • 作业标识：{a['id']}")
            menu_lines.append(f"    • 应交人数：{a.get('total_expected', 0)} 人 / 截止 {deadline_str}")

        menu_lines.append("0️⃣ 整学期全量作业归档（打包所有实验）")
        menu_lines.append("━━━━━━━━━━━━━━━")
        menu_lines.append("💡 请直接回复对应【数字序号】（如：1 或 0）")
        menu_lines.append("（回复 取消 可退出本次导出，60 秒内有效）")

        yield event.plain_result("\n".join(menu_lines))

    @filter.command("导出整学期", alias={"导出全部作业"})
    async def export_all_direct_cmd(self, event: AstrMessageEvent):
        """管理员一键导出整学期全量作业归档压缩包"""
        if not is_admin(event):
            yield event.plain_result("❌ 权限不足：整学期归档导出仅限课程助教或管理员执行。")
            return

        url = f"{API_BASE}/api/assignments/export/all"
        filename = "整学期全量作业归档.zip"
        async for res in upload_file_action(event, url, filename, "整学期全量作业"):
            yield res

    @filter.platform_adapter_type(filter.PlatformAdapterType.ALL, priority=1)
    async def on_reply_message(self, event: AstrMessageEvent):
        """监听用户回复以完成多轮选择交互"""
        sender_id = str(event.get_sender_id())
        group_id = str(event.get_group_id() or "")
        session_key = (sender_id, group_id)

        session_info = PENDING_EXPORT_SESSIONS.get(session_key)
        if not session_info:
            return

        if time.time() - session_info["time"] > SESSION_TIMEOUT:
            del PENDING_EXPORT_SESSIONS[session_key]
            return

        text = event.message_str.strip()

        # 取消导出
        if text.lower() in ["取消", "退出", "q", "quit", "cancel"]:
            del PENDING_EXPORT_SESSIONS[session_key]
            yield event.plain_result("❎ 已取消本次作业导出。")
            event.stop_event()
            return

        # 命中有效数字序号或选项
        options = session_info.get("options", {})
        if text in options:
            del PENDING_EXPORT_SESSIONS[session_key]
            url, filename, display_name = options[text]
            # 执行下载和上传逻辑
            async for res in upload_file_action(event, url, filename, display_name):
                yield res
            event.stop_event()
            return

        # 用户输入了纯数字但不在选项范围内
        if text.isdigit():
            yield event.plain_result(f"⚠️ 未找到序号 [{text}] 对应的作业，请回复有效序号（如 1 或 0），或回复 取消 退出。")
            event.stop_event()
            return

        # 用户输入了其他指令（如以 / 开头），清除当前会话状态，放行其他指令
        if text.startswith("/") or text.startswith("!"):
            del PENDING_EXPORT_SESSIONS[session_key]
            return
