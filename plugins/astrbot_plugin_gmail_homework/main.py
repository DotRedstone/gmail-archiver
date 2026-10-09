import json
import time
import urllib.request
import urllib.parse
from astrbot.api.event import filter, AstrMessageEvent
from astrbot.api.star import register, Star

TA_STUDENT_ID = "240809010505"  # 明航宇（助教本人豁免）
TA_NAME = "明航宇"
API_BASE = "https://gmail.bdot.in"
ADMIN_QQ_LIST = ["1689491386"]  # 助教与管理员 QQ

# 交互会话缓存：(sender_id, group_id) -> {"time": float, "type": str, "options": dict}
PENDING_SESSIONS = {}
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

def get_assignments():
    """动态获取全部已配置的作业列表"""
    try:
        return api_get("/api/assignments")
    except Exception:
        return []

def match_assignment(query: str, assignments: list):
    """根据用户输入的序号、ID 或关键词匹配作业"""
    if not query or not assignments:
        return None
    query = query.strip()
    
    # 1. 数字序号匹配（1, 2, ...）
    if query.isdigit():
        idx = int(query)
        if 1 <= idx <= len(assignments):
            return assignments[idx - 1]

    # 2. 精确匹配 id 或 name
    for a in assignments:
        if query == a.get("id") or query == a.get("name"):
            return a

    # 3. 模糊包含匹配（如 "实验1", "lab1", "并行计算"）
    q_lower = query.lower()
    for a in assignments:
        if q_lower in a.get("id", "").lower() or q_lower in a.get("name", "").lower():
            return a

    return None

def format_file_size(size_bytes: int) -> str:
    """人性化格式化文件大小"""
    if size_bytes < 1024:
        return f"{size_bytes} B"
    elif size_bytes < 1024 * 1024:
        return f"{size_bytes / 1024:.1f} KB"
    else:
        return f"{size_bytes / (1024 * 1024):.2f} MB"

def render_status_card(data: dict) -> str:
    """生成单次作业统计详情卡片"""
    deadline = data.get("deadline", "")[:16].replace("T", " ")
    return (
        f"📊【{data['assignment_name']}】提交统计\n"
        f"━━━━━━━━━━━━━━━\n"
        f"✅ 实交人数：{data['submitted_count']} / {data['total_expected']}\n"
        f"📈 提交比例：{data['submission_rate']}\n"
        f"⚠️ 迟交人数：{data['late_count']} 人\n"
        f"⏳ 截止时间：{deadline}"
    )

def render_missing_list(data: dict) -> str:
    """生成单次作业未交学生名单"""
    missing = data.get("missing_list") or []
    real_missing = [s for s in missing if s.get("student_id") != TA_STUDENT_ID and s.get("name") != TA_NAME]
    
    if not real_missing:
        return f"🎉 太棒了！【{data['assignment_name']}】除助教本人外，全班同学已全部提交完毕！"

    lines = [f"📢【{data['assignment_name']}】未交名单（共 {len(real_missing)} 人）："]
    for idx, s in enumerate(real_missing, 1):
        lines.append(f"{idx}. {s['name']}（{s['student_id']}，{s['class_name']}）")
    lines.append("\n💡 请以上同学抓紧整理源码与实验报告并发送至邮箱！")
    return "\n".join(lines)

async def upload_file_action(event: AstrMessageEvent, download_url: str, filename: str, display_name: str):
    """通用文件直传操作"""
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

@register("gmail_homework", "DotRedstone", "Gmail 自动收作业与催交插件", "1.3.0")
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
            "1️⃣ /查作业 [序号或名称] —— 查看作业提交概览或指定作业统计\n"
            "    例：/查作业 或 /查作业 2\n"
            "2️⃣ /未交 [序号或名称] —— 查看未交名单（支持选作业或直接查）\n"
            "    例：/未交 或 /未交 2\n"
            "3️⃣ /查收 <姓名或学号> [作业序号] —— 自助查询个人作业是否成功接收归档\n"
            "    例：/查收 张三（查每一次作业）或 /查收 张三 1（查指定作业）\n\n"
            "👑 助教/管理员专属指令：\n"
            "4️⃣ /导出作业 [序号] —— 选择任意作业打包发送 QQ 文件\n"
            "    (群聊发群文件，私聊发离线文件)\n"
            "5️⃣ /导出整学期 —— 一键打包整学期全部作业发送文件"
        )
        yield event.plain_result(msg)

    @filter.command("查作业")
    async def status_cmd(self, event: AstrMessageEvent, param: str = ""):
        """查询作业提交总体进度：/查作业 或 /查作业 2"""
        assignments = get_assignments()
        if not assignments:
            yield event.plain_result("❌ 获取作业列表失败或当前未配置任何作业。")
            return

        param = param.strip()

        # 模式 1：用户指定了具体作业（如 /查作业 2 或 /查作业 实验1）
        if param:
            target = match_assignment(param, assignments)
            if not target:
                opts = "、".join([f"{i}.{a['name']}" for i, a in enumerate(assignments, 1)])
                yield event.plain_result(f"⚠️ 未找到与 [{param}] 匹配的作业。\n当前可选作业：{opts}")
                return
            try:
                data = api_get(f"/api/assignments/{target['id']}/status")
                yield event.plain_result(render_status_card(data))
            except Exception as e:
                yield event.plain_result(f"❌ 查询作业状态失败: {e}")
            return

        # 模式 2：未指定作业
        # 若仅有 1 个作业，直接显示详细卡片
        if len(assignments) == 1:
            try:
                data = api_get(f"/api/assignments/{assignments[0]['id']}/status")
                yield event.plain_result(render_status_card(data))
            except Exception as e:
                yield event.plain_result(f"❌ 查询作业状态失败: {e}")
            return

        # 若有多个作业，显示全作业概览列表并开启交互会话
        lines = [
            "📊【各次作业提交总体概览】",
            "━━━━━━━━━━━━━━━"
        ]
        status_options = {}
        for idx, a in enumerate(assignments, 1):
            status_options[str(idx)] = a
            try:
                s_data = api_get(f"/api/assignments/{a['id']}/status")
                dl = s_data.get("deadline", "")[:16].replace("T", " ")
                lines.append(f"{idx}️⃣ {a['name']}：{s_data['submitted_count']}/{s_data['total_expected']} 已交 ({s_data['submission_rate']})")
                lines.append(f"    • 迟交 {s_data['late_count']} 人 / 截止 {dl}")
            except Exception:
                lines.append(f"{idx}️⃣ {a['name']}（详情获取失败）")

        lines.append("━━━━━━━━━━━━━━━")
        lines.append("💡 请回复对应【数字序号】（如：1 或 2）查看具体作业详情卡片")
        lines.append("（回复 取消 退出，60 秒内有效）")

        session_key = (str(event.get_sender_id()), str(event.get_group_id() or ""))
        PENDING_SESSIONS[session_key] = {
            "time": time.time(),
            "type": "status",
            "options": status_options,
        }
        yield event.plain_result("\n".join(lines))

    @filter.command("未交")
    async def missing_cmd(self, event: AstrMessageEvent, param: str = ""):
        """查询未交学生名单：/未交 或 /未交 2"""
        assignments = get_assignments()
        if not assignments:
            yield event.plain_result("❌ 获取作业列表失败或当前未配置任何作业。")
            return

        param = param.strip()

        # 模式 1：指定了作业
        if param:
            target = match_assignment(param, assignments)
            if not target:
                opts = "、".join([f"{i}.{a['name']}" for i, a in enumerate(assignments, 1)])
                yield event.plain_result(f"⚠️ 未找到与 [{param}] 匹配的作业。\n当前可选作业：{opts}")
                return
            try:
                data = api_get(f"/api/assignments/{target['id']}/missing")
                yield event.plain_result(render_missing_list(data))
            except Exception as e:
                yield event.plain_result(f"❌ 查询未交名单失败: {e}")
            return

        # 模式 2：未指定作业
        # 若仅有 1 个作业，直接显示
        if len(assignments) == 1:
            try:
                data = api_get(f"/api/assignments/{assignments[0]['id']}/missing")
                yield event.plain_result(render_missing_list(data))
            except Exception as e:
                yield event.plain_result(f"❌ 查询未交名单失败: {e}")
            return

        # 若有多个作业，列出选择菜单
        lines = [
            "📋【请选择要查询未交名单的作业】",
            "━━━━━━━━━━━━━━━"
        ]
        missing_options = {}
        for idx, a in enumerate(assignments, 1):
            missing_options[str(idx)] = a
            dl = a.get("deadline", "")[:16].replace("T", " ")
            lines.append(f"{idx}️⃣ {a['name']}（截止时间：{dl}）")
        lines.append("━━━━━━━━━━━━━━━")
        lines.append("💡 请直接回复对应【数字序号】（如：1 或 2），或使用 /未交 2")
        lines.append("（回复 取消 退出，60 秒内有效）")

        session_key = (str(event.get_sender_id()), str(event.get_group_id() or ""))
        PENDING_SESSIONS[session_key] = {
            "time": time.time(),
            "type": "missing",
            "options": missing_options,
        }
        yield event.plain_result("\n".join(lines))

    @filter.command("查收")
    async def check_student(self, event: AstrMessageEvent, query: str = ""):
        """自助查询个人作业是否收到：/查收 张三 或 /查收 24080901xxxx [序号]"""
        query = query.strip()
        if not query:
            yield event.plain_result("💡 用法：/查收 <姓名或学号> [作业序号]，例如：/查收 张三 或 /查收 张三 2")
            return

        assignments = get_assignments()
        if not assignments:
            yield event.plain_result("❌ 获取作业列表失败或当前未配置任何作业。")
            return

        # 解析参数：支持 "/查收 张三" 或 "/查收 张三 1" 或 "/查收 1 张三"
        parts = query.split()
        student_query = ""
        target_assignment = None

        if len(parts) == 1:
            student_query = parts[0]
        else:
            p0, p1 = parts[0], parts[1]
            a0 = match_assignment(p0, assignments)
            a1 = match_assignment(p1, assignments)
            if a0 and not a1:
                target_assignment = a0
                student_query = p1
            elif a1 and not a0:
                target_assignment = a1
                student_query = p0
            else:
                student_query = p0
                target_assignment = match_assignment(p1, assignments)

        if student_query in [TA_STUDENT_ID, TA_NAME]:
            yield event.plain_result(f"👑 {TA_NAME} 为课程助教，无需提交作业。")
            return

        # 如果指定了特定作业
        if target_assignment:
            check_list = [target_assignment]
            is_single = True
        else:
            check_list = assignments
            is_single = False

        results = []
        found_any_record = False

        for a in check_list:
            aid = a["id"]
            a_name = a["name"]
            dl = a.get("deadline", "")[:16].replace("T", " ")
            
            try:
                sub_data = api_get(f"/api/assignments/{aid}/submissions")
                matched_subs = [
                    s for s in sub_data.get("submissions", [])
                    if s.get("student_name") == student_query or s.get("student_id") == student_query
                ]
            except Exception:
                matched_subs = []

            if matched_subs:
                found_any_record = True
                s = matched_subs[0]
                size_str = format_file_size(s.get("file_size", 0))
                sub_time = s.get("submitted_at", "")[:16].replace("T", " ")
                late_tag = " [迟交]" if s.get("is_late") else ""
                results.append(
                    f"🔹【{a_name}】：\n"
                    f"    ✅ 已成功接收归档{late_tag}\n"
                    f"    📁 附件：{s.get('target_filename')}\n"
                    f"    📦 大小：{size_str} | 🕒 接收时间：{sub_time}"
                )
            else:
                # 检查是否在 missing 中
                try:
                    mis_data = api_get(f"/api/assignments/{aid}/missing")
                    is_missing = any(
                        m.get("student_id") == student_query or m.get("name") == student_query
                        for m in (mis_data.get("missing_list") or [])
                    )
                except Exception:
                    is_missing = False

                if is_missing:
                    found_any_record = True
                    results.append(
                        f"🔹【{a_name}】：\n"
                        f"    ⚠️ 未检索到有效作业提交\n"
                        f"    ⏳ 截止时间：{dl}"
                    )
                else:
                    results.append(
                        f"🔹【{a_name}】：\n"
                        f"    ❓ 未在该次作业花名册中检索到 [{student_query}]"
                    )

        if not found_any_record:
            yield event.plain_result(f"⚠️ 在课程花名册与提交记录中均未找到【{student_query}】，请核对姓名或学号是否有误。")
            return

        header = f"📋【{student_query}】作业查收报告："
        msg = header + "\n" + "━━━━━━━━━━━━━━━\n" + "\n".join(results)
        if not is_single:
            msg += "\n━━━━━━━━━━━━━━━\n💡 如有疑问请确认邮件主题与附件命名规范。"
        yield event.plain_result(msg)

    @filter.command("导出作业", alias={"下载作业"})
    async def export_cmd(self, event: AstrMessageEvent, param: str = ""):
        """管理员导出作业归档（支持多轮序号选择或带参快捷导出）"""
        if not is_admin(event):
            yield event.plain_result("❌ 权限不足：作业归档导出仅限课程助教或管理员执行。")
            return

        assignments = get_assignments()
        if not assignments:
            yield event.plain_result("❌ 获取作业列表失败或当前未配置任何作业。")
            return

        param = param.strip()

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
        PENDING_SESSIONS[session_key] = {
            "time": time.time(),
            "type": "export",
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

        session_info = PENDING_SESSIONS.get(session_key)
        if not session_info:
            return

        if time.time() - session_info["time"] > SESSION_TIMEOUT:
            del PENDING_SESSIONS[session_key]
            return

        text = event.message_str.strip()

        # 取消会话
        if text.lower() in ["取消", "退出", "q", "quit", "cancel"]:
            del PENDING_SESSIONS[session_key]
            yield event.plain_result("❎ 已取消本次操作。")
            event.stop_event()
            return

        # 用户输入了其他指令（如以 / 开头），清除当前会话状态，放行其他指令
        if text.startswith("/") or text.startswith("!"):
            del PENDING_SESSIONS[session_key]
            return

        s_type = session_info.get("type")
        options = session_info.get("options", {})

        # 命中有效数字序号或选项
        if text in options:
            del PENDING_SESSIONS[session_key]
            event.stop_event()

            if s_type == "export":
                url, filename, display_name = options[text]
                async for res in upload_file_action(event, url, filename, display_name):
                    yield res
                return
            elif s_type == "status":
                target = options[text]
                try:
                    data = api_get(f"/api/assignments/{target['id']}/status")
                    yield event.plain_result(render_status_card(data))
                except Exception as e:
                    yield event.plain_result(f"❌ 查询作业状态失败: {e}")
                return
            elif s_type == "missing":
                target = options[text]
                try:
                    data = api_get(f"/api/assignments/{target['id']}/missing")
                    yield event.plain_result(render_missing_list(data))
                except Exception as e:
                    yield event.plain_result(f"❌ 查询未交名单失败: {e}")
                return

        # 用户输入了纯数字但不在选项范围内
        if text.isdigit():
            yield event.plain_result(f"⚠️ 未找到序号 [{text}] 对应的作业选项，请回复有效序号，或回复 取消 退出。")
            event.stop_event()
            return
