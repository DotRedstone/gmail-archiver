# [Plugin]
import asyncio
import os
import re
import sys
import time

from astrbot.api.event import filter, AstrMessageEvent
from astrbot.api.star import register, Star
from astrbot.api.provider import ProviderRequest
from astrbot.api.message_components import File

_PLUGIN_DIR = os.path.dirname(os.path.abspath(__file__))
if _PLUGIN_DIR not in sys.path:
    sys.path.insert(0, _PLUGIN_DIR)

try:
    from .config import (
        API_BASE,
        SUPER_ADMIN_QQ,
        get_class_groups,
        add_class_group,
        remove_class_group,
        get_teaching_assistants,
        add_teaching_assistant,
        remove_teaching_assistant,
        is_super_admin,
        is_ta_or_admin,
        is_admin,
    )
    from .client import (
        api_get,
        async_api_get,
        async_api_post_json,
        async_api_delete,
        async_upload_file,
        get_assignments,
        match_assignment,
    )
    from .formatters import (
        format_file_size,
        extract_student_id,
        format_class_name,
        format_beijing_time,
        calculate_next_wednesday_deadline,
        generate_notice_text,
        render_status_card,
        render_missing_list,
        is_querying_my_submission,
        build_student_status_card,
        format_plagiarism_type,
        render_plagiarism_alert,
    )
    from .actions import make_reply, upload_file_action
except ImportError:
    from config import (
        API_BASE,
        SUPER_ADMIN_QQ,
        get_class_groups,
        add_class_group,
        remove_class_group,
        get_teaching_assistants,
        add_teaching_assistant,
        remove_teaching_assistant,
        is_super_admin,
        is_ta_or_admin,
        is_admin,
    )
    from client import (
        api_get,
        async_api_get,
        async_api_post_json,
        async_api_delete,
        async_upload_file,
        get_assignments,
        match_assignment,
    )
    from formatters import (
        format_file_size,
        extract_student_id,
        format_class_name,
        format_beijing_time,
        calculate_next_wednesday_deadline,
        generate_notice_text,
        render_status_card,
        render_missing_list,
        is_querying_my_submission,
        build_student_status_card,
        format_plagiarism_type,
        render_plagiarism_alert,
    )
    from actions import make_reply, upload_file_action

# [State]
PENDING_SESSIONS = {}
SESSION_TIMEOUT = 120

USER_QUERY_TIMESTAMPS = {}
MAX_USER_QUERIES_PER_MINUTE = 6
USER_QUERY_COOLDOWN_SECONDS = 3.0
MAX_PROMPT_CHARS = 1500

HOMEWORK_SYSTEM_PROMPT = """你是《并行计算》课程作业助手。
【核心职责】：
1. 专注于课程作业的发布指引、提交收集与状态核验。
2. 作业提交方式：指导学生直接私聊发送作业压缩包（.zip / .rar / .7z / .tar.gz），系统将自动核验并归档入库。
3. 作业状态查询：指导学生使用「/查收 姓名」或直接回复「看看我交了吗」自助查询。
4. 解答学生关于实验要求、打包格式、命名规范及作业提交相关的疑问。
【回复规范】：
- 语言风格：专业、简洁、直接、客观，严禁任何角色扮演、拟人化动作描写或冗余套话。
- 若学生输入完全无关的话题，简明礼貌回复：“同学你好，本助手主要负责课程作业收集与查收指引，如需交作业请直接私聊发送作业压缩包。”"""

@register("gmail_homework", "DotRedstone", "课程作业全流程助手：QQ 直收归档、身份绑定、实验卡一键分发与催交", "1.4.3")
class HomeworkPlugin(Star):
    def __init__(self, context):
        super().__init__(context)

    # [Guardrails]
    @filter.on_llm_request()
    async def handle_llm_guardrails(self, event: AstrMessageEvent, req: ProviderRequest):
        if not event.is_private_chat() and not is_admin(event):
            event.stop_event()
            await event.send(make_reply(event,
                "同学你好！群聊中仅支持作业指令查询（/查作业、/未交、/查收、/帮助）。\n"
                "为了保持群内消息整洁并保护你的提问隐私，代码调试与学术疑问请直接【私聊我】提问哦~"
            ))
            return

        if event.is_private_chat() and not is_admin(event):
            msg_text = event.get_message_str().strip()
            sender_id = str(event.get_sender_id())

            try:
                bind_data = await async_api_get(f"/api/bindings/{sender_id}")
            except Exception:
                bind_data = {}

            if bind_data.get("error"):
                sid = extract_student_id(msg_text)
                if sid:
                    try:
                        auto_bind = await async_api_post_json("/api/bindings", {"qq_id": sender_id, "student_id": sid})
                        if auto_bind.get("success"):
                            bind_data = auto_bind.get("binding", {})
                    except Exception:
                        pass

            if bind_data.get("error") and not msg_text.startswith("/") and not msg_text.startswith("!"):
                event.stop_event()
                session_key = (sender_id, "")
                PENDING_SESSIONS[session_key] = {
                    "time": time.time(),
                    "type": "bind_and_chat",
                    "question": msg_text,
                }
                await event.send(make_reply(event,
                    "👋 同学你好！欢迎使用《并行计算》作业助手。\n"
                    "首次使用请直接回复你的【学号】（例如：240809010501）完成身份绑定，绑定后可直接私聊发送作业压缩包秒级归档提交。"
                ))
                return

            if len(msg_text) > MAX_PROMPT_CHARS:
                event.stop_event()
                await event.send(make_reply(event,
                    f"⚠️ 单次提问内容过长（超过 {MAX_PROMPT_CHARS} 字）。\n"
                    "请提炼核心代码报错或关键问题分段发送哦~"
                ))
                return

            sender_id = str(event.get_sender_id())
            now = time.time()
            history = USER_QUERY_TIMESTAMPS.get(sender_id, [])
            history = [t for t in history if now - t < 60]

            if history and (now - history[-1] < USER_QUERY_COOLDOWN_SECONDS):
                event.stop_event()
                await event.send(make_reply(event,
                    f"⏳ 提问太快啦~ 请稍等 {int(USER_QUERY_COOLDOWN_SECONDS)} 秒后再发送新问题。"
                ))
                return

            if len(history) >= MAX_USER_QUERIES_PER_MINUTE:
                event.stop_event()
                await event.send(make_reply(event,
                    "⚠️ 最近 1 分钟内的提问过于频繁，请稍等 15 秒后再试哦~"
                ))
                return

            history.append(now)
            USER_QUERY_TIMESTAMPS[sender_id] = history

        student_ctx = ""
        if event.is_private_chat():
            try:
                b_info = await async_api_get(f"/api/bindings/{sender_id}")
            except Exception:
                b_info = {}
            if not b_info.get("error"):
                s_name = b_info.get("student_name", "")
                s_id = b_info.get("student_id", "")
                s_cl = format_class_name(b_info.get("class_name", ""), s_id)
                student_ctx = f"\n\n【当前对话学生】：姓名：{s_name}，学号：{s_id}，班级：{s_cl}。若学生询问自己的作业是否收到或提交情况，可调用 query_student_homework('{s_name}') 为其查询并在回复中客观告知结果。"

        full_prompt = HOMEWORK_SYSTEM_PROMPT + student_ctx
        req.system_prompt = full_prompt

    # [LLM Tools]
    @filter.llm_tool(name="query_student_homework")
    async def tool_query_student(self, event: AstrMessageEvent, student_name_or_id: str) -> str:
        '''查询指定学生在各次作业中的提交与归档状态。

        Args:
            student_name_or_id(string): 学生的姓名或学号
        '''
        assignments = get_assignments()
        if not assignments:
            return "未能获取到当前作业列表。"

        sender_id = str(event.get_sender_id() or "")
        student_query = student_name_or_id.strip()

        if not is_ta_or_admin(event):
            try:
                b_info = await async_api_get(f"/api/bindings/{sender_id}")
            except Exception:
                b_info = {}
            if b_info.get("error"):
                return "权限不足：未绑定身份的学生无法查询作业。请先发送 /绑定 学号 姓名。"
            my_sid = str(b_info.get("student_id", "")).strip()
            my_name = str(b_info.get("student_name", "")).strip()
            if student_query != my_sid and student_query != my_name:
                return "权限不足：为保护同学个人隐私，普通学生仅可查询本人的作业状态，无法跨人查询其他同学。"

        results = []
        for a in assignments:
            aid = a["id"]
            a_name = a["name"]
            try:
                sub_data = api_get(f"/api/assignments/{aid}/submissions")
                subs = [
                    s for s in sub_data.get("submissions", [])
                    if s.get("student_name") == student_query or s.get("student_id") == student_query
                ]
            except Exception:
                subs = []

            if subs:
                s = subs[0]
                size_str = format_file_size(s.get("file_size", 0))
                time_str = format_beijing_time(s.get("submitted_at", ""))
                late = " (迟交)" if s.get("is_late") else ""
                results.append(f"{a_name}: 已提交归档{late}，附件名 {s.get('target_filename')}，大小 {size_str}，时间 {time_str}")
            else:
                try:
                    mis_data = api_get(f"/api/assignments/{aid}/missing")
                    is_missing = any(
                        m.get("student_id") == student_query or m.get("name") == student_query
                        for m in (mis_data.get("missing_list") or [])
                    )
                except Exception:
                    is_missing = False
                if is_missing:
                    dl = format_beijing_time(a.get("deadline", ""))
                    results.append(f"{a_name}: 未提交 (截止时间 {dl})")
                else:
                    results.append(f"{a_name}: 未在花名册中找到该学生")

        return f"学生【{student_query}】的作业状态：\n" + "\n".join(results)

    @filter.llm_tool(name="get_homework_list")
    async def tool_get_homework_list(self, event: AstrMessageEvent) -> str:
        '''获取当前课程所有已发布的作业清单、截止时间与提交要求。'''
        assignments = get_assignments()
        if not assignments:
            return "当前暂未发布任何作业。"

        lines = ["当前发布的作业列表："]
        for idx, a in enumerate(assignments, 1):
            dl = format_beijing_time(a.get("deadline", ""))
            lines.append(f"{idx}. {a['name']} (ID: {a['id']})，截止时间：{dl}，应交人数：{a.get('total_expected', 0)} 人")
        return "\n".join(lines)

    # [Help]
    @filter.command("帮助", alias={"作业帮助"})
    async def help_cmd(self, event: AstrMessageEvent):
        msg = (
            "📖【并行计算课程 · 作业助手指南】\n"
            "━━━━━━━━━━━━━━━\n"
            "💡 提示：日常无需输入斜杠「/」，直接自然提问或发送快捷口令即可！\n\n"
            "🎓【学生日常功能】（群聊/私聊均可）：\n"
            "1️⃣「看看我交了吗？」或「查收」—— 自动核对个人作业归档状态\n"
            "2️⃣ 私聊直接发作业压缩包 —— 自动识别身份，秒级规范命名并安全归档入库\n"
            "3️⃣「查作业」—— 查看作业提交人数与整体进度\n"
            "4️⃣「未交」—— 查看当前未交作业名单\n"
            "5️⃣ 绑定 <学号> [姓名] —— 绑定学生身份（例：绑定 240809010501 支全振）\n"
            "6️⃣「我的信息」—— 查看当前绑定的学号、姓名与班级\n\n"
            "👑【助教/管理员专属】：\n"
            "• 查收 <姓名或学号> —— 直接查询指定同学作业（例：查收 支全振）\n"
            "• 导出作业 / 导出整学期 —— 打包下载全员作业归档压缩包\n"
            "• 一键查重 / 查重 [序号] —— 全员代码哈希指纹查重，排查抄袭嫌疑\n"
            "• 私聊发实验卡文件 —— 自动提取实验号并一键分发群文件与广播\n"
            "• 设为班级群 —— 在群内执行，将当前群标记为作业通告群\n"
            "• 助教列表 —— 查看教学管理团队人员\n\n"
            "👑【超级管理员专属】：\n"
            "• 添加助教 <QQ> —— 任命新的课程助教\n"
            "• 移除助教 <QQ> —— 移除指定的课程助教"
        )
        yield make_reply(event, msg)

    # [Binding Commands]
    @filter.command("绑定")
    async def bind_student(self, event: AstrMessageEvent, param: str = ""):
        sender_id = str(event.get_sender_id())
        parts = param.strip().split()
        if not parts:
            yield make_reply(event, 
                "💡 用法：/绑定 <学号> [姓名]\n"
                "例如：/绑定 240809010501 支全振\n"
                "（绑定身份后，直接私聊把作业压缩包发给机器人即可自动秒级入库！）"
            )
            return

        student_id = parts[0]
        student_name = parts[1] if len(parts) > 1 else ""

        try:
            resp = await async_api_post_json("/api/bindings", {
                "qq_id": sender_id,
                "student_id": student_id,
                "student_name": student_name,
            })
        except Exception as e:
            yield make_reply(event, f"❌ 请求服务端失败: {e}")
            return

        if not resp.get("success"):
            err_msg = resp.get("error", "未知错误")
            yield make_reply(event, f"⚠️ 绑定失败：{err_msg}")
            return

        b = resp.get("binding", {})
        cl = format_class_name(b.get("class_name", ""), b.get("student_id", ""))
        yield make_reply(event, 
            "🎉【学生身份绑定成功】\n"
            "━━━━━━━━━━━━━━━\n"
            f"👤 姓名：{b.get('student_name')}\n"
            f"🆔 学号：{b.get('student_id')}\n"
            f"🏫 班级：{cl}\n"
            f"📱 绑定 QQ：{sender_id}\n"
            "━━━━━━━━━━━━━━━\n"
            "💡 现在你可以直接【私聊把作业压缩包发给我】秒级自动入库，无需再发送邮件！"
        )

    @filter.command("我的信息", alias={"查询绑定", "我的绑定"})
    async def my_info(self, event: AstrMessageEvent):
        sender_id = str(event.get_sender_id())
        try:
            resp = await async_api_get(f"/api/bindings/{sender_id}")
        except Exception as e:
            yield make_reply(event, f"❌ 查询失败: {e}")
            return

        if resp.get("error"):
            yield make_reply(event, 
                "❓ 你当前尚未绑定学生身份。\n"
                "👉 请回复：/绑定 学号 姓名（例如：/绑定 240809010501 支全振）进行绑定。"
            )
            return

        cl = format_class_name(resp.get("class_name", ""), resp.get("student_id", ""))
        yield make_reply(event, 
            "📋【你的学生身份信息】\n"
            "━━━━━━━━━━━━━━━\n"
            f"👤 姓名：{resp.get('student_name')}\n"
            f"🆔 学号：{resp.get('student_id')}\n"
            f"🏫 班级：{cl}\n"
            f"📱 QQ号：{sender_id}\n"
            "━━━━━━━━━━━━━━━\n"
            "💡 如需提交作业，直接在私聊会话中把压缩包发送给机器人即可。"
        )

    @filter.command("解绑")
    async def unbind_student(self, event: AstrMessageEvent, target_qq: str = ""):
        sender_id = str(event.get_sender_id())
        to_unbind = sender_id

        if target_qq.strip():
            if not is_admin(event):
                yield make_reply(event, "❌ 权限不足：只有管理员可指定解绑其他账号。")
                return
            to_unbind = target_qq.strip()

        try:
            resp = await async_api_delete(f"/api/bindings/{to_unbind}")
        except Exception as e:
            yield make_reply(event, f"❌ 解绑失败: {e}")
            return

        if resp.get("success"):
            yield make_reply(event, f"✅ 已解除 QQ [{to_unbind}] 的身份绑定。")
        else:
            yield make_reply(event, f"⚠️ 解绑失败：{resp.get('error', '未找到绑定记录')}")

    @filter.command("绑定列表")
    async def list_bindings_cmd(self, event: AstrMessageEvent):
        if not is_admin(event):
            yield make_reply(event, "❌ 权限不足：此指令仅限助教或管理员使用。")
            return

        try:
            resp = await async_api_get("/api/bindings")
        except Exception as e:
            yield make_reply(event, f"❌ 获取绑定列表失败: {e}")
            return

        bindings = resp.get("bindings", [])
        if not bindings:
            yield make_reply(event, "当前暂无学生完成身份绑定。")
            return

        lines = [f"📋【已绑定学生清单（共 {len(bindings)} 人）】", "━━━━━━━━━━━━━━━"]
        for idx, b in enumerate(bindings, 1):
            cl = format_class_name(b.get("class_name", ""), b.get("student_id", ""))
            lines.append(f"{idx}. {b.get('student_name')}（{b.get('student_id')}，{cl}）- QQ:{b.get('qq_id')}")
        yield make_reply(event, "\n".join(lines))

    # [Class Group Commands]
    @filter.command("设为班级群")
    async def set_class_group_cmd(self, event: AstrMessageEvent):
        if not is_admin(event):
            yield make_reply(event, "❌ 权限不足：仅助教或管理员可配置班级群。")
            return

        group_id = str(event.get_group_id() or "")
        if not group_id:
            yield make_reply(event, "⚠️ 请在班级群聊内发送此指令以绑定本群。")
            return

        if add_class_group(group_id):
            yield make_reply(event, f"✅ 成功将当前群【{group_id}】设置为并行计算作业通告群！发布新实验时将自动推送群文件与广播。")
        else:
            yield make_reply(event, f"ℹ️ 当前群【{group_id}】已在通告群列表中。")

    @filter.command("移除班级群")
    async def remove_class_group_cmd(self, event: AstrMessageEvent):
        if not is_admin(event):
            yield make_reply(event, "❌ 权限不足。")
            return

        group_id = str(event.get_group_id() or "")
        if not group_id:
            yield make_reply(event, "⚠️ 请在要移除的群聊内发送此指令。")
            return

        if remove_class_group(group_id):
            yield make_reply(event, f"✅ 已从通知群列表中移除当前群【{group_id}】。")
        else:
            yield make_reply(event, f"ℹ️ 当前群【{group_id}】不在班级群列表中。")

    @filter.command("班级群列表")
    async def list_class_groups_cmd(self, event: AstrMessageEvent):
        if not is_admin(event):
            yield make_reply(event, "❌ 权限不足。")
            return

        groups = get_class_groups()
        if not groups:
            yield make_reply(event, "当前尚未配置任何班级群。请在目标群聊内发送 /设为班级群。")
            return

        yield make_reply(event, "📢 当前已配置的并行计算通告群：\n" + "\n".join([f"• 群号：{g}" for g in groups]))

    # [TA Management Commands]
    @filter.command("添加助教")
    async def add_ta_cmd(self, event: AstrMessageEvent, qq: str = ""):
        if not is_super_admin(event):
            yield make_reply(event, "❌ 权限不足：仅超级管理员可任命助教。")
            return
        qq = qq.strip()
        if not qq.isdigit():
            yield make_reply(event, "⚠️ 请提供有效的纯数字 QQ 号，例如：/添加助教 3293421754")
            return
        if qq == SUPER_ADMIN_QQ:
            yield make_reply(event, "ℹ️ 该账号已是超级管理员，无需重复添加。")
            return

        name_info = ""
        try:
            b = await async_api_get(f"/api/bindings/{qq}")
            if not b.get("error"):
                name_info = f"（{b.get('student_name', '')}同学，{b.get('class_name', '')}）"
        except Exception:
            pass

        if add_teaching_assistant(qq):
            yield make_reply(event, f"✅ 成功添加助教：QQ {qq}{name_info}！\n该助教现已具备查询全员作业、导出下载作业及一键查重的权限。")
        else:
            yield make_reply(event, f"ℹ️ QQ {qq} 已在助教列表中。")

    @filter.command("移除助教")
    async def remove_ta_cmd(self, event: AstrMessageEvent, qq: str = ""):
        if not is_super_admin(event):
            yield make_reply(event, "❌ 权限不足：仅超级管理员可移除助教。")
            return
        qq = qq.strip()
        if not qq:
            yield make_reply(event, "⚠️ 请指定要移除的助教 QQ 号，例如：/移除助教 3293421754")
            return
        if remove_teaching_assistant(qq):
            yield make_reply(event, f"✅ 已成功移除助教：QQ {qq}。")
        else:
            yield make_reply(event, f"⚠️ 未在助教列表中找到 QQ {qq}。")

    @filter.command("助教列表")
    async def list_ta_cmd(self, event: AstrMessageEvent):
        if not is_ta_or_admin(event):
            yield make_reply(event, "❌ 权限不足：仅助教或管理员可查看助教名单。")
            return
        tas = get_teaching_assistants()
        lines = [
            "👥【并行计算课程 · 教学管理团队】",
            "━━━━━━━━━━━━━━━",
            f"👑 超级管理员：QQ {SUPER_ADMIN_QQ}",
        ]
        if tas:
            lines.append("🎓 课程助教：")
            for idx, q in enumerate(tas, 1):
                name_str = ""
                try:
                    b = await async_api_get(f"/api/bindings/{q}")
                    if not b.get("error"):
                        name_str = f" - {b.get('student_name', '')}（{b.get('class_name', '')}）"
                except Exception:
                    pass
                lines.append(f"  {idx}. QQ {q}{name_str}")
        else:
            lines.append("🎓 课程助教：（暂未配置，管理员可发送 /添加助教 <QQ> 添加）")
        lines.append("━━━━━━━━━━━━━━━")
        yield make_reply(event, "\n".join(lines))

    # [Status & Missing Commands]
    @filter.command("查作业")
    async def status_cmd(self, event: AstrMessageEvent, param: str = ""):
        assignments = get_assignments()
        if not assignments:
            yield make_reply(event, "❌ 获取作业列表失败或当前未配置任何作业。")
            return

        param = param.strip()

        if param:
            target = match_assignment(param, assignments)
            if not target:
                opts = "、".join([f"{i}.{a['name']}" for i, a in enumerate(assignments, 1)])
                yield make_reply(event, f"⚠️ 未找到与 [{param}] 匹配的作业。\n当前可选作业：{opts}")
                return
            try:
                data = api_get(f"/api/assignments/{target['id']}/status")
                yield make_reply(event, render_status_card(data))
            except Exception as e:
                yield make_reply(event, f"❌ 查询作业状态失败: {e}")
            return

        if len(assignments) == 1:
            try:
                data = api_get(f"/api/assignments/{assignments[0]['id']}/status")
                yield make_reply(event, render_status_card(data))
            except Exception as e:
                yield make_reply(event, f"❌ 查询作业状态失败: {e}")
            return

        lines = ["📊【各次作业提交概览】", "━━━━━━━━━━━━━━━"]
        status_options = {}
        for idx, a in enumerate(assignments, 1):
            status_options[str(idx)] = a
            try:
                s_data = api_get(f"/api/assignments/{a['id']}/status")
                dl = format_beijing_time(s_data.get("deadline", ""))
                lines.append(f"{idx}️⃣ {a['name']}：已交 {s_data['submitted_count']}/{s_data['total_expected']} 人 ({s_data['submission_rate']})")
                lines.append(f"    • 迟交 {s_data['late_count']} 人 | 截止时间 {dl}")
            except Exception:
                lines.append(f"{idx}️⃣ {a['name']}（详情获取失败）")

        lines.append("━━━━━━━━━━━━━━━")
        lines.append("💡 请回复对应【数字序号】（如：1 或 2）查看具体作业详情")
        lines.append("（回复 取消 退出，120 秒内有效）")

        session_key = (str(event.get_sender_id()), str(event.get_group_id() or ""))
        PENDING_SESSIONS[session_key] = {
            "time": time.time(),
            "type": "status",
            "options": status_options,
        }
        yield make_reply(event, "\n".join(lines))

    @filter.command("未交")
    async def missing_cmd(self, event: AstrMessageEvent, param: str = ""):
        assignments = get_assignments()
        if not assignments:
            yield make_reply(event, "❌ 获取作业列表失败或当前未配置任何作业。")
            return

        param = param.strip()

        if param:
            target = match_assignment(param, assignments)
            if not target:
                opts = "、".join([f"{i}.{a['name']}" for i, a in enumerate(assignments, 1)])
                yield make_reply(event, f"⚠️ 未找到与 [{param}] 匹配的作业。\n当前可选作业：{opts}")
                return
            try:
                data = api_get(f"/api/assignments/{target['id']}/missing")
                yield make_reply(event, render_missing_list(data))
            except Exception as e:
                yield make_reply(event, f"❌ 查询未交名单失败: {e}")
            return

        if len(assignments) == 1:
            try:
                data = api_get(f"/api/assignments/{assignments[0]['id']}/missing")
                yield make_reply(event, render_missing_list(data))
            except Exception as e:
                yield make_reply(event, f"❌ 查询未交名单失败: {e}")
            return

        lines = ["📋【请选择要查看未交名单的作业】", "━━━━━━━━━━━━━━━"]
        missing_options = {}
        for idx, a in enumerate(assignments, 1):
            missing_options[str(idx)] = a
            dl = format_beijing_time(a.get("deadline", ""))
            lines.append(f"{idx}️⃣ {a['name']}（截止时间：{dl}）")
        lines.append("━━━━━━━━━━━━━━━")
        lines.append("💡 请直接回复对应【数字序号】（如：1 或 2），或使用 /未交 2")
        lines.append("（回复 取消 退出，120 秒内有效）")

        session_key = (str(event.get_sender_id()), str(event.get_group_id() or ""))
        PENDING_SESSIONS[session_key] = {
            "time": time.time(),
            "type": "missing",
            "options": missing_options,
        }
        yield make_reply(event, "\n".join(lines))

    @filter.command("查收")
    async def check_student(self, event: AstrMessageEvent, query: str = ""):
        query = query.strip()
        sender_id = str(event.get_sender_id())
        is_ta = is_ta_or_admin(event)

        try:
            bind_info = await async_api_get(f"/api/bindings/{sender_id}")
        except Exception:
            bind_info = {}
        has_binding = not bind_info.get("error")
        my_sid = str(bind_info.get("student_id", "")).strip()
        my_name = str(bind_info.get("student_name", "")).strip()

        assignments = get_assignments()
        if not assignments:
            yield make_reply(event, "❌ 获取作业列表失败或当前未配置任何作业。")
            return

        target_assignment = None

        if not query:
            if not has_binding:
                if is_ta:
                    yield make_reply(event, "💡 用法：/查收 <姓名或学号> [作业序号]，例如：/查收 支全振 或 /查收 支全振 2")
                else:
                    yield make_reply(event, "💡 你尚未绑定学生身份。请先发送「/绑定 学号 姓名」完成绑定，或直接私聊发送作业压缩包。")
                return
            student_query = my_sid
        else:
            parts = query.split()
            student_query = ""

            if len(parts) == 1:
                p0 = parts[0]
                matched_a = match_assignment(p0, assignments)
                if matched_a:
                    target_assignment = matched_a
                    if has_binding:
                        student_query = my_sid
                    else:
                        if is_ta:
                            yield make_reply(event, f"💡 请指定要查询的学生姓名或学号，例如：/查收 支全振 {p0}")
                        else:
                            yield make_reply(event, "💡 你尚未绑定学生身份。请先发送「/绑定 学号 姓名」完成绑定。")
                        return
                else:
                    student_query = p0
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

            if not is_ta:
                if not has_binding:
                    yield make_reply(event, 
                        "❌ 权限不足：为保护同学隐私，仅课程助教可查询他人作业。\n"
                        "💡 请先发送「/绑定 学号 姓名」完成绑定后直接发送「/查收」。"
                    )
                    return
                if student_query != my_sid and student_query != my_name:
                    yield make_reply(event, 
                        "❌ 权限不足：为保护同学隐私，普通学生仅可查询本人的作业提交状态。\n"
                        "💡 请直接发送「/查收」或「看看我交了吗」查看自己的作业。"
                    )
                    return

        check_list = [target_assignment] if target_assignment else assignments
        is_single = bool(target_assignment)
        results = []
        found_any_record = False

        for a in check_list:
            aid = a["id"]
            a_name = a["name"]
            dl = format_beijing_time(a.get("deadline", ""))
            
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
                sub_time = format_beijing_time(s.get("submitted_at", ""))
                late_tag = " ⚠️[迟交]" if s.get("is_late") else ""
                results.append(
                    f"🔹【{a_name}】：\n"
                    f"    ✅ 已成功提交归档{late_tag}\n"
                    f"    📁 附件：{s.get('target_filename')}\n"
                    f"    📦 大小：{size_str} | 🕒 提交时间：{sub_time}"
                )
            else:
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
                        f"    ⚠️ 暂未查询到提交记录\n"
                        f"    ⏳ 截止时间：{dl}"
                    )
                else:
                    results.append(
                        f"🔹【{a_name}】：\n"
                        f"    ❓ 未在该次作业花名册中检索到 [{student_query}]"
                    )

        if not found_any_record:
            yield make_reply(event, f"⚠️ 在课程花名册与提交记录中均未找到【{student_query}】，请核对姓名或学号是否有误。")
            return

        header = f"📋【{student_query}】作业查收状态："
        msg = header + "\n" + "━━━━━━━━━━━━━━━\n" + "\n".join(results)
        if not is_single:
            msg += "\n━━━━━━━━━━━━━━━\n💡 提示：如需提交或更新作业，可直接私聊机器人发送压缩包。"
        yield make_reply(event, msg)

    # [Export Commands]
    @filter.command("导出作业", alias={"下载作业"})
    async def export_cmd(self, event: AstrMessageEvent, param: str = ""):
        if not is_admin(event):
            yield make_reply(event, "❌ 权限不足：作业归档导出仅限课程助教或管理员执行。")
            return

        assignments = get_assignments()
        if not assignments:
            yield make_reply(event, "❌ 获取作业列表失败或当前未配置任何作业。")
            return

        param = param.strip()

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

        if param and param in options:
            url, filename, display_name = options[param]
            async for res in upload_file_action(event, url, filename, display_name):
                yield res
            return

        session_key = (str(event.get_sender_id()), str(event.get_group_id() or ""))
        PENDING_SESSIONS[session_key] = {
            "time": time.time(),
            "type": "export",
            "options": options,
        }

        menu_lines = ["📋【请选择要导出的作业归档】", "━━━━━━━━━━━━━━━"]
        for idx, a in enumerate(assignments, 1):
            deadline_str = format_beijing_time(a.get('deadline', ''))
            menu_lines.append(f"{idx}️⃣ {a['name']}")
            menu_lines.append(f"    • 作业标识：{a['id']}")
            menu_lines.append(f"    • 应交人数：{a.get('total_expected', 0)} 人 | 截止时间 {deadline_str}")

        menu_lines.append("0️⃣ 整学期全量作业归档（打包所有实验）")
        menu_lines.append("━━━━━━━━━━━━━━━")
        menu_lines.append("💡 请直接回复对应【数字序号】（如：1 或 0）")
        menu_lines.append("（回复 取消 可退出本次导出，120 秒内有效）")

        yield make_reply(event, "\n".join(menu_lines))

    @filter.command("导出整学期", alias={"导出全部作业"})
    async def export_all_direct_cmd(self, event: AstrMessageEvent):
        if not is_admin(event):
            yield make_reply(event, "❌ 权限不足：整学期归档导出仅限课程助教或管理员执行。")
            return

        url = f"{API_BASE}/api/assignments/export/all"
        filename = "整学期全量作业归档.zip"
        async for res in upload_file_action(event, url, filename, "整学期全量作业"):
            yield res

    # [Plagiarism Commands]
    @filter.command("查重", alias={"代码查重", "学术诚信", "一键查重", "查抄袭"})
    async def plagiarism_cmd(self, event: AstrMessageEvent, param: str = ""):
        if not is_ta_or_admin(event):
            yield make_reply(event, "❌ 权限不足：代码查重仅限课程助教或管理员执行。")
            return

        assignments = get_assignments()
        if not assignments:
            yield make_reply(event, "❌ 获取作业列表失败或当前未配置任何作业。")
            return

        param = param.strip()

        if param and param not in ["全部", "all"]:
            target = match_assignment(param, assignments)
            if not target:
                opts = "、".join([f"{i}.{a['name']}" for i, a in enumerate(assignments, 1)])
                yield make_reply(event, f"⚠️ 未找到与 [{param}] 匹配的作业。\n当前可选作业：{opts}")
                return

            try:
                data = api_get(f"/api/assignments/{target['id']}/plagiarism")
            except Exception as e:
                yield make_reply(event, f"❌ 获取查重数据失败: {e}")
                return

            pairs = data.get("pairs", [])
            a_name = data.get("assignment_name", target.get("name", ""))

            if not pairs:
                yield make_reply(event, 
                    f"🎉【{a_name} · 代码哈希查重报告】\n"
                    "━━━━━━━━━━━━━━━\n"
                    "✅ 全员代码哈希指纹校验通过，未检出整包复制或换壳抄袭记录。"
                )
                return

            lines = [
                f"🔍【{a_name} · 代码哈希查重报告】",
                "━━━━━━━━━━━━━━━",
                f"⚠️ 共检出 {len(pairs)} 组雷同嫌疑记录：",
            ]
            for idx, p in enumerate(pairs, 1):
                p_type, _ = format_plagiarism_type(p.get("duplicate_type"))
                f_str = "、".join(p.get("identical_files", []))
                lines.append(f"{idx}️⃣ {p['student_a_name']}（{p['student_a']}）↔ {p['student_b_name']}（{p['student_b']}）")
                lines.append(f"    • 判定类型：{p_type}")
                lines.append(f"    • 碰撞源码：{f_str}")

            lines.append("━━━━━━━━━━━━━━━")
            lines.append("💡 建议助教与上述同学核实源码实现与提交情况。")
            yield make_reply(event, "\n".join(lines))
            return

        total_suspect_pairs = 0
        summary_lines = [
            "🔍【并行计算课程 · 全员代码哈希查重报告】",
            "━━━━━━━━━━━━━━━",
        ]
        detail_blocks = []

        for idx, a in enumerate(assignments, 1):
            try:
                data = api_get(f"/api/assignments/{a['id']}/plagiarism")
                pairs = data.get("pairs", [])
                a_name = data.get("assignment_name", a.get("name", ""))
                if pairs:
                    total_suspect_pairs += len(pairs)
                    summary_lines.append(f"⚠️ {idx}️⃣ {a_name}：检出 {len(pairs)} 组雷同嫌疑！")
                    block = [f"📌【{a_name} 雷同明细】："]
                    for p_idx, p in enumerate(pairs, 1):
                        p_type, _ = format_plagiarism_type(p.get("duplicate_type"))
                        f_str = "、".join(p.get("identical_files", []))
                        block.append(f"  {p_idx}. {p['student_a_name']}（{p['student_a']}）↔ {p['student_b_name']}（{p['student_b']}） [{p_type}]")
                        block.append(f"     碰撞源码：{f_str}")
                    detail_blocks.append("\n".join(block))
                else:
                    summary_lines.append(f"✅ {idx}️⃣ {a_name}：指纹校验通过（0 组雷同）")
            except Exception as e:
                summary_lines.append(f"❓ {idx}️⃣ {a.get('name', '')}：查重查询失败 ({e})")

        summary_lines.append("━━━━━━━━━━━━━━━")
        if total_suspect_pairs == 0:
            summary_lines.append("🎉 当前全部实验指纹校验通过，未检出整包复制或换壳抄袭行为。")
        else:
            summary_lines.append(f"⚠️ 全学期累计发现 {total_suspect_pairs} 组疑似抄袭，明细如下：\n")
            summary_lines.extend(detail_blocks)
            summary_lines.append("\n━━━━━━━━━━━━━━━\n💡 建议助教与上述同学核实源码与提交记录。")

        summary_lines.append("💡 提示：如需单独查看某次作业详情，可发送：/查重 1")
        yield make_reply(event, "\n".join(summary_lines))

    # [Message Listener]
    @filter.platform_adapter_type(filter.PlatformAdapterType.ALL, priority=1)
    async def on_message_listener(self, event: AstrMessageEvent):
        sender_id = str(event.get_sender_id())
        group_id = str(event.get_group_id() or "")
        session_key = (sender_id, group_id)
        is_private = event.is_private_chat()

        async def reply(msg):
            if isinstance(msg, str):
                await event.send(make_reply(event, msg))
            else:
                await event.send(msg)

        file_comp = None
        if hasattr(event.message_obj, "message") and isinstance(event.message_obj.message, list):
            for comp in event.message_obj.message:
                if isinstance(comp, File):
                    file_comp = comp
                    break

        if is_private and file_comp:
            event.stop_event()
            raw_filename = file_comp.name or "homework.zip"

            lab_match = re.search(r"(?:实验卡|实验|lab)\s*(\d+)", raw_filename, re.I)
            is_doc_ext = any(raw_filename.lower().endswith(ext) for ext in [".pdf", ".docx", ".doc"])
            if is_admin(event) and lab_match and is_doc_ext:
                lab_num = lab_match.group(1)
                friendly_dl, iso_dl = calculate_next_wednesday_deadline()
                notice_text = generate_notice_text(lab_num, friendly_dl)

                local_file = await file_comp.get_file()

                PENDING_SESSIONS[session_key] = {
                    "time": time.time(),
                    "type": "publish_lab",
                    "lab_num": lab_num,
                    "filename": raw_filename,
                    "file_path": local_file,
                    "deadline": iso_dl,
                    "notice_text": notice_text,
                }

                preview_msg = (
                    f"👑 助教好！检测到实验卡【{raw_filename}】。\n"
                    f"📌 实验编号：实验{lab_num}\n"
                    f"⏳ 推算截止时间：{friendly_dl}\n\n"
                    "📢【自动生成群通知文案预览】：\n"
                    "━━━━━━━━━━━━━━━\n"
                    f"{notice_text}\n"
                    "━━━━━━━━━━━━━━━\n"
                    f"💡 请核对上方信息。回复【发布】或【发布 {lab_num}】即可自动：\n"
                    f"  1️⃣ 在云端注册新作业【并行计算实验{lab_num}】\n"
                    "  2️⃣ 将该实验卡上传至班级群群文件\n"
                    "  3️⃣ 在班级群发送上述通知文案\n"
                    "（回复 取消 可放弃发布，120 秒内有效）"
                )
                await reply(preview_msg)
                return

            valid_hw_exts = [".zip", ".rar", ".7z", ".tar.gz", ".tgz", ".tar", ".pdf"]
            if not any(raw_filename.lower().endswith(ext) for ext in valid_hw_exts):
                await reply(
                    f"⚠️ 收到文件【{raw_filename}】，但这好像不是标准的作业压缩包（建议格式：.zip、.rar、.7z、.tar.gz）。\n"
                    "请将源码和实验报告打包为压缩包后重新发送哦~"
                )
                return

            try:
                bind_data = await async_api_get(f"/api/bindings/{sender_id}")
            except Exception as e:
                await reply(f"❌ 检索绑定数据失败: {e}")
                return

            if bind_data.get("error"):
                await reply(f"⏳ 正在接收并把作业【{raw_filename}】暂存...")
                local_path = await file_comp.get_file()
                if not local_path or not os.path.exists(local_path):
                    await reply("❌ 接收文件失败，请重新发送。")
                    return

                PENDING_SESSIONS[session_key] = {
                    "time": time.time(),
                    "type": "bind_and_submit",
                    "file_path": local_path,
                    "filename": raw_filename,
                }
                await reply(
                    f"👋 同学你好！已安全接收你的作业【{raw_filename}】。\n"
                    "由于你是首次使用，请直接回复你的【学号】（例如：240809010501）：\n"
                    "核对花名册后将自动完成绑定，并将刚才的作业直接存入系统！\n"
                    "（回复 取消 可放弃本次提交，120 秒内有效）"
                )
                return

            student_id = bind_data.get("student_id", "")
            student_name = bind_data.get("student_name", "")
            class_name = format_class_name(bind_data.get("class_name", ""), student_id)

            await reply(f"⏳ 正在核验作业文件【{raw_filename}】并上传归档...")

            try:
                local_path = await file_comp.get_file()
                if not local_path or not os.path.exists(local_path):
                    await reply("❌ 接收文件失败：未能下载文件流。请稍后重试。")
                    return

                upload_res = await async_upload_file(
                    assignment_id="latest",
                    file_path=local_path,
                    orig_filename=raw_filename,
                    student_id=student_id,
                    student_name=student_name,
                    class_name=class_name,
                    qq_id=sender_id,
                )

                try:
                    if os.path.exists(local_path):
                        os.remove(local_path)
                except Exception:
                    pass

                if not upload_res.get("success"):
                    if upload_res.get("duplicate"):
                        await reply(render_plagiarism_alert(upload_res))
                        return
                    await reply(f"⚠️ 作业归档失败: {upload_res.get('error', '未知错误')}")
                    return

                size_str = format_file_size(upload_res.get("file_size", 0))
                sub_time = format_beijing_time(upload_res.get("submitted_at", ""), with_seconds=True)
                sha_short = upload_res.get("sha256", "")[:16]
                ver = upload_res.get("version", 1)
                is_update = upload_res.get("is_update", False)
                is_late = upload_res.get("is_late", False)

                update_str = " (覆盖更新)" if is_update else ""
                late_str = " ⚠️【迟交】" if is_late else ""

                receipt_card = (
                    f"🎉【{upload_res.get('assignment_name')}】作业归档成功！\n"
                    "━━━━━━━━━━━━━━━\n"
                    f"👤 学生：{student_name}（{student_id}）\n"
                    f"🏫 班级：{class_name}\n"
                    f"📁 规范重命名：{upload_res.get('target_filename')}\n"
                    f"📦 文件大小：{size_str}\n"
                    f"🔒 SHA256：{sha_short}...\n"
                    f"🕒 提交时间：{sub_time}\n"
                    f"📌 提交状态：第 {ver} 次提交{update_str}{late_str}\n"
                    "━━━━━━━━━━━━━━━\n"
                    "✅ 作业已安全入库！若需修改代码或报告，在截止时间前直接再次发送新文件即可覆盖更新。"
                )
                await reply(receipt_card)
                return

            except Exception as e:
                await reply(f"❌ 处理作业提交异常: {e}")
                return

        session_info = PENDING_SESSIONS.get(session_key)
        if not session_info:
            return

        if time.time() - session_info["time"] > SESSION_TIMEOUT:
            del PENDING_SESSIONS[session_key]
            return

        text = event.message_str.strip()

        if text.lower() in ["取消", "退出", "q", "quit", "cancel"]:
            fpath = session_info.get("file_path")
            if fpath and os.path.exists(fpath):
                try:
                    os.remove(fpath)
                except Exception:
                    pass
            del PENDING_SESSIONS[session_key]
            await reply("❎ 已取消本次操作。")
            event.stop_event()
            return

        if text.startswith("/") or text.startswith("!"):
            del PENDING_SESSIONS[session_key]
            return

        s_type = session_info.get("type")

        if s_type == "publish_lab":
            lab_num = session_info.get("lab_num")
            if text in ["发布", f"发布 {lab_num}", "确认发布", "确认", "yes", "y"]:
                del PENDING_SESSIONS[session_key]
                event.stop_event()

                await reply(f"⏳ 正在为【并行计算实验{lab_num}】注册云端规则并广播通知...")

                rule_cfg = {
                    "id": f"parallel_computing_lab{lab_num}",
                    "name": f"并行计算实验{lab_num}",
                    "deadline": session_info["deadline"],
                    "target_filename": f"实验{lab_num}-{{class}}-{{student_id}}-{{name}}.{{ext}}",
                    "rosters": ["rosters/2024_cs_5.csv", "rosters/2024_green_compute_1.csv"],
                    "patterns": {
                        "subject_regex": fr"(?i)并行计算.*实验\s*{lab_num}.*(?P<name>[\p{Han}\w]+)",
                        "attachment_regex": fr"(?i)实验\s*{lab_num}.*(?P<ext>zip|rar|7z|tar\.gz)",
                    }
                }
                try:
                    create_res = await async_api_post_json("/api/assignments", rule_cfg)
                    if not create_res.get("success"):
                        await reply(f"❌ 注册云端作业规则失败: {create_res.get('error')}")
                        return
                except Exception as e:
                    await reply(f"❌ 调用作业注册接口异常: {e}")
                    return

                bot = getattr(event, "bot", None)
                class_groups = get_class_groups()
                notice_text = session_info["notice_text"]
                file_path = session_info.get("file_path")
                filename = session_info.get("filename")

                success_groups = []
                if bot and class_groups:
                    for g_id in class_groups:
                        try:
                            await bot.call_action("send_group_msg", group_id=int(g_id), message=notice_text)
                            if file_path and os.path.exists(file_path):
                                await bot.call_action("upload_group_file", group_id=str(g_id), file=file_path, name=filename)
                            success_groups.append(g_id)
                        except Exception as e:
                            print(f"Failed to broadcast to group {g_id}: {e}")

                group_status_str = f"已自动广播至班级群【{', '.join(success_groups)}】并上传群文件。" if success_groups else "（暂未配置班级群，可在群内发送 /设为班级群 配置）"

                await reply(
                    f"🎉【并行计算实验{lab_num}】发布成功！\n"
                    "━━━━━━━━━━━━━━━\n"
                    f"✅ 云端规则已生效：{create_res.get('name')}\n"
                    f"👥 关联应交人数：{create_res.get('roster_count')} 人\n"
                    f"⏳ 截止时间：{format_beijing_time(create_res.get('deadline', ''))}\n"
                    f"📢 群通知与文件：{group_status_str}\n"
                    "━━━━━━━━━━━━━━━\n"
                    "💡 学生现在可以直接私聊把作业压缩包发给机器人，或发送至邮箱，全链路已开启！"
                )
                return

        if s_type == "bind_and_submit":
            sid = extract_student_id(text)
            if not sid:
                await reply("⚠️ 未识别到有效学号。请直接回复 12 位学号（例如：240809010501），回复 取消 可退出本次提交。")
                event.stop_event()
                return

            del PENDING_SESSIONS[session_key]
            event.stop_event()

            await reply(f"⏳ 正在核对学号【{sid}】并绑定归档作业...")

            bind_res = await async_api_post_json("/api/bindings", {"qq_id": sender_id, "student_id": sid})
            if not bind_res.get("success"):
                err = bind_res.get("error", "学号核验失败")
                await reply(f"❌ 绑定失败：{err}\n请核对学号后重新发送作业文件。")
                fpath = session_info.get("file_path")
                if fpath and os.path.exists(fpath):
                    try:
                        os.remove(fpath)
                    except Exception:
                        pass
                return

            b = bind_res.get("binding", {})
            st_name = b.get("student_name", "")
            cl_name = format_class_name(b.get("class_name", ""), sid)
            fpath = session_info.get("file_path")
            fname = session_info.get("filename")

            try:
                upload_res = await async_upload_file(
                    assignment_id="latest",
                    file_path=fpath,
                    orig_filename=fname,
                    student_id=sid,
                    student_name=st_name,
                    class_name=cl_name,
                    qq_id=sender_id,
                )
            finally:
                if fpath and os.path.exists(fpath):
                    try:
                        os.remove(fpath)
                    except Exception:
                        pass

            if not upload_res.get("success"):
                if upload_res.get("duplicate"):
                    await reply(
                        f"✅ 身份绑定成功：{st_name}（{cl_name}）！\n\n" + render_plagiarism_alert(upload_res)
                    )
                    return
                await reply(
                    f"✅ 身份连接成功：{st_name}（{cl_name}）！\n"
                    f"⚠️ 但作业归档失败：{upload_res.get('error', '未知错误')}\n"
                    "现在你的身份已绑定完成，请直接重新发送一次作业压缩包即可！"
                )
                return

            size_str = format_file_size(upload_res.get("file_size", 0))
            sub_time = format_beijing_time(upload_res.get("submitted_at", ""), with_seconds=True)
            sha_short = upload_res.get("sha256", "")[:16]
            ver = upload_res.get("version", 1)
            is_update = upload_res.get("is_update", False)
            is_late = upload_res.get("is_late", False)
            update_str = " (覆盖更新)" if is_update else ""
            late_str = " ⚠️【迟交】" if is_late else ""

            combined_card = (
                f"🎉【身份绑定与作业归档成功】\n"
                "━━━━━━━━━━━━━━━\n"
                f"👤 学生：{st_name}（{sid}）\n"
                f"🏫 班级：{cl_name}\n"
                f"📱 绑定账号：QQ {sender_id}\n"
                "━━━━━━━━━━━━━━━\n"
                f"📁 归档文件：{upload_res.get('target_filename')}\n"
                f"📦 文件大小：{size_str}\n"
                f"🔒 SHA256：{sha_short}...\n"
                f"🕒 提交时间：{sub_time}\n"
                f"📌 提交状态：第 {ver} 次提交{update_str}{late_str}\n"
                "━━━━━━━━━━━━━━━\n"
                "✅ 身份已绑定，作业已安全入库！以后修改作业直接私聊发送压缩包即可，无需再输入任何信息。"
            )
            await reply(combined_card)
            return

        if s_type == "bind_and_chat":
            sid = extract_student_id(text)
            if not sid:
                await reply("⚠️ 未识别到有效学号。请直接回复 12 位学号（例如：240809010501），回复 取消 退出。")
                event.stop_event()
                return

            del PENDING_SESSIONS[session_key]
            event.stop_event()

            bind_res = await async_api_post_json("/api/bindings", {"qq_id": sender_id, "student_id": sid})
            if not bind_res.get("success"):
                err = bind_res.get("error", "学号核验失败")
                await reply(f"❌ 绑定失败：{err}\n请核对学号后重新发送。")
                return

            b = bind_res.get("binding", {})
            st_name = b.get("student_name", "")
            cl_name = format_class_name(b.get("class_name", ""), sid)

            status_card = await build_student_status_card(sid, st_name, cl_name)
            await reply(
                f"🎉 绑定成功：【{st_name}】同学（{cl_name}）。\n\n"
                f"{status_card}\n"
                "━━━━━━━━━━━━━━━\n"
                "💡 操作指引：\n"
                "• 提交作业：直接私聊发送作业压缩包，系统自动核验归档\n"
                "• 查收状态：发送「看看我交了吗」或「/查收」随时核对"
            )
            return

        if s_type == "bind_and_report_status":
            sid = extract_student_id(text)
            if not sid:
                await reply("⚠️ 未识别到有效学号。请直接回复 12 位学号（例如：240809010501），回复 取消 退出。")
                event.stop_event()
                return

            del PENDING_SESSIONS[session_key]
            event.stop_event()

            await reply(f"⏳ 正在核对学号【{sid}】并查询作业状态...")

            bind_res = await async_api_post_json("/api/bindings", {"qq_id": sender_id, "student_id": sid})
            if not bind_res.get("success"):
                err = bind_res.get("error", "学号核验失败")
                await reply(f"❌ 绑定失败：{err}\n请核对学号是否在花名册中。")
                return

            b = bind_res.get("binding", {})
            st_name = b.get("student_name", "")
            cl_name = format_class_name(b.get("class_name", ""), sid)

            status_card = await build_student_status_card(sid, st_name, cl_name)
            await reply(
                f"🎉 绑定成功！欢迎【{st_name}】同学（{cl_name}）！\n\n"
                + status_card
            )
            return

        options = session_info.get("options", {})
        if text in options:
            del PENDING_SESSIONS[session_key]
            event.stop_event()

            if s_type == "export":
                if not is_ta_or_admin(event):
                    await reply("❌ 权限不足：作业归档导出仅限课程助教或管理员执行。")
                    return
                url, filename, display_name = options[text]
                async for res in upload_file_action(event, url, filename, display_name):
                    await reply(res)
                return
            elif s_type == "status":
                target = options[text]
                try:
                    data = api_get(f"/api/assignments/{target['id']}/status")
                    await reply(render_status_card(data))
                except Exception as e:
                    await reply(f"❌ 查询作业状态失败: {e}")
                return
            elif s_type == "missing":
                target = options[text]
                try:
                    data = api_get(f"/api/assignments/{target['id']}/missing")
                    await reply(render_missing_list(data))
                except Exception as e:
                    await reply(f"❌ 查询未交名单失败: {e}")
                return

        if text.isdigit() and session_info:
            await reply(f"⚠️ 未找到序号 [{text}] 对应的作业选项，请回复有效序号，或回复 取消 退出。")
            event.stop_event()
            return

        clean_text = text.strip()
        if clean_text in ["查作业", "作业统计", "作业概览", "查看作业", "作业进度", "全部作业"]:
            event.stop_event()
            async for r in self.status_cmd(event):
                await reply(r)
            return
        elif clean_text in ["未交", "未交名单", "谁没交", "催交", "没交作业"]:
            event.stop_event()
            async for r in self.missing_cmd(event):
                await reply(r)
            return
        elif clean_text in ["帮助", "菜单", "作业帮助", "指令", "指令菜单"]:
            event.stop_event()
            async for r in self.help_cmd(event):
                await reply(r)
            return
        elif clean_text in ["我的信息", "我的绑定", "我是谁", "查询绑定"]:
            event.stop_event()
            async for r in self.my_info(event):
                await reply(r)
            return
        elif clean_text in ["解绑", "解除绑定"]:
            event.stop_event()
            async for r in self.unbind_student(event):
                await reply(r)
            return
        elif clean_text in ["查重", "代码查重", "一键查重", "学术诚信", "查抄袭"]:
            event.stop_event()
            async for r in self.plagiarism_cmd(event):
                await reply(r)
            return
        elif clean_text.startswith("查重 ") or clean_text.startswith("代码查重 ") or clean_text.startswith("一键查重 "):
            event.stop_event()
            p = clean_text.split(maxsplit=1)[1]
            async for r in self.plagiarism_cmd(event, p):
                await reply(r)
            return
        elif clean_text in ["导出作业", "下载作业"]:
            event.stop_event()
            async for r in self.export_cmd(event):
                await reply(r)
            return
        elif clean_text in ["导出整学期", "导出全部作业", "下载全部作业"]:
            event.stop_event()
            async for r in self.export_all_direct_cmd(event):
                await reply(r)
            return
        elif clean_text in ["助教列表", "助教团队"]:
            event.stop_event()
            async for r in self.list_ta_cmd(event):
                await reply(r)
            return
        elif clean_text.startswith("绑定 ") or clean_text.startswith("绑定:"):
            event.stop_event()
            param = clean_text.split(maxsplit=1)[1] if " " in clean_text else clean_text.split(":", 1)[1]
            async for r in self.bind_student(event, param):
                await reply(r)
            return
        elif clean_text.startswith("查收 ") or clean_text.startswith("查 "):
            event.stop_event()
            param = clean_text.split(maxsplit=1)[1]
            async for r in self.check_student(event, param):
                await reply(r)
            return
        elif clean_text in ["查收", "查我", "我的作业"]:
            event.stop_event()
            async for r in self.check_student(event, ""):
                await reply(r)
            return

        if is_querying_my_submission(clean_text):
            event.stop_event()

            explicit_sid = extract_student_id(clean_text)
            try:
                bind_data = await async_api_get(f"/api/bindings/{sender_id}")
            except Exception:
                bind_data = {}

            if not bind_data.get("error"):
                target_sid = explicit_sid if explicit_sid else bind_data.get("student_id", "")
                target_name = bind_data.get("student_name", "") if not explicit_sid else ""
                target_class = format_class_name(bind_data.get("class_name", ""), target_sid)
                card = await build_student_status_card(target_sid, target_name, target_class)
                await reply(card)
                return
            else:
                if explicit_sid:
                    bind_res = await async_api_post_json("/api/bindings", {"qq_id": sender_id, "student_id": explicit_sid})
                    if bind_res.get("success"):
                        b = bind_res.get("binding", {})
                        st_name = b.get("student_name", "")
                        cl_name = format_class_name(b.get("class_name", ""), explicit_sid)
                        card = await build_student_status_card(explicit_sid, st_name, cl_name)
                        await reply(f"🎉 自动完成身份绑定：【{st_name}】同学（{cl_name}）！\n\n" + card)
                        return

                if is_private:
                    PENDING_SESSIONS[session_key] = {
                        "time": time.time(),
                        "type": "bind_and_report_status",
                    }
                    await reply(
                        "👋 同学你好呀！我还不认识你呢，你是哪位同学呀？\n"
                        "请直接回复你的【学号】（例如：240809010501），我马上帮你核对并查询你的作业！"
                    )
                    return
                else:
                    await reply(
                        "同学你好！我还不认识你呢，为了保护你的个人信息，请直接【私聊我】发送学号绑定，即可随时查询你的作业状态哦~"
                    )
                    return

        if is_private and not text.startswith("/") and not text.startswith("!"):
            sid = extract_student_id(text)
            clean_digits = text.replace(" ", "").replace("学号", "").replace("：", "").replace(":", "")
            if sid and len(clean_digits) <= 16:
                try:
                    check_b = await async_api_get(f"/api/bindings/{sender_id}")
                except Exception:
                    check_b = {}

                if check_b.get("error"):
                    event.stop_event()
                    await reply(f"⏳ 正在核对学号【{sid}】...")
                    bind_res = await async_api_post_json("/api/bindings", {"qq_id": sender_id, "student_id": sid})
                    if bind_res.get("success"):
                        b = bind_res.get("binding", {})
                        cl_name = format_class_name(b.get("class_name", ""), sid)
                        st_name = b.get("student_name", "")
                        card = await build_student_status_card(sid, st_name, cl_name)
                        await reply(
                            f"🎉【学生身份绑定成功】\n"
                            "━━━━━━━━━━━━━━━\n"
                            f"👤 学生姓名：{st_name}\n"
                            f"🆔 学号：{sid}\n"
                            f"🏫 班级：{cl_name}\n"
                            "━━━━━━━━━━━━━━━\n\n"
                            + card
                        )
                        return
                    else:
                        await reply(f"⚠️ 绑定失败：{bind_res.get('error')}")
                        return
