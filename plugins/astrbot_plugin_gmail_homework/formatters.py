# [Formatters]
from datetime import datetime, timedelta, timezone
import os
import re
import sys

_PLUGIN_DIR = os.path.dirname(os.path.abspath(__file__))
if _PLUGIN_DIR not in sys.path:
    sys.path.insert(0, _PLUGIN_DIR)

try:
    from .client import async_api_get
except ImportError:
    from client import async_api_get

BEIJING_TZ = timezone(timedelta(hours=8))

def format_file_size(size_bytes: int) -> str:
    if size_bytes < 1024:
        return f"{size_bytes} B"
    elif size_bytes < 1024 * 1024:
        return f"{size_bytes / 1024:.1f} KB"
    else:
        return f"{size_bytes / (1024 * 1024):.2f} MB"

def extract_student_id(text: str) -> str:
    m = re.search(r"\b(24\d{10})\b", text.strip())
    if m:
        return m.group(1)
    m2 = re.search(r"\b(2\d{11})\b", text.strip())
    if m2:
        return m2.group(1)
    m3 = re.search(r"(\d{12})", text.strip())
    if m3:
        return m3.group(1)
    m4 = re.search(r"(\d{10,13})", text.strip())
    if m4:
        return m4.group(1)
    return ""

def format_class_name(raw: str, student_id: str = "") -> str:
    clean = (raw or "").strip()
    if "绿" in clean or "算" in clean:
        return "24绿算"
    if "5" in clean or "五" in clean:
        return "245班"
    sid = (student_id or "").strip()
    if sid.startswith("2408090105"):
        return "245班"
    if sid.startswith("2408090121") or sid == "240810010303":
        return "24绿算"
    return clean or "245班"

def format_beijing_time(raw_time: str, with_seconds: bool = False) -> str:
    if not raw_time:
        return ""
    raw = str(raw_time).strip()
    try:
        clean = raw.replace("Z", "+00:00")
        dt = datetime.fromisoformat(clean)
        if dt.tzinfo is None:
            dt = dt.replace(tzinfo=timezone.utc)
        bj_dt = dt.astimezone(BEIJING_TZ)
        fmt = "%Y-%m-%d %H:%M:%S" if with_seconds else "%Y-%m-%d %H:%M"
        return bj_dt.strftime(fmt)
    except Exception:
        return raw[:19 if with_seconds else 16].replace("T", " ")

def calculate_next_wednesday_deadline() -> tuple:
    now = datetime.now(BEIJING_TZ)
    days_ahead = (2 - now.weekday()) % 7
    if days_ahead == 0:
        days_ahead = 7
    target_date = now + timedelta(days=days_ahead)
    target_dt = target_date.replace(hour=18, minute=0, second=0, microsecond=0)
    friendly = f"{target_dt.month}月{target_dt.day}日（下周三）18:00"
    iso_str = target_dt.strftime("%Y-%m-%dT18:00:00+08:00")
    return friendly, iso_str

def generate_notice_text(lab_num: str, deadline_friendly: str) -> str:
    return (
        f"并行计算实验{lab_num}作业提交要求\n\n"
        f"截止时间：{deadline_friendly}\n\n"
        "请将源码 + 实验报告整理后，以一个压缩包的形式提交：\n"
        "1️⃣【推荐方式】直接私聊本机器人发送作业压缩包，自动秒级归档入库！\n"
        "   （首次使用请在私聊发送：/绑定 学号 姓名）\n"
        "2️⃣【备用方式】发送至我的邮箱：dotredstone0123@gmail.com\n\n"
        "邮件主题（若走邮箱）：\n"
        f"并行计算-实验{lab_num}-姓名\n"
        f"示例：并行计算-实验{lab_num}-张三\n\n"
        "压缩包命名：\n"
        f"实验{lab_num}-班级-学号-姓名.zip\n"
        f"示例：实验{lab_num}-245班-240809010501-张三.zip 或 实验{lab_num}-24绿算-240809012103-李四.zip\n\n"
        "邮件正文（若走邮箱）：\n"
        "姓名：张三\n"
        "学号：240809010501\n"
        "班级：245班\n"
        f"提交内容：实验{lab_num}源码及实验报告\n\n"
        "请严格按照以上格式提交，邮件主题、正文信息及附件命名不要自行修改格式，方便后续统一统计和整理。"
    )

def render_status_card(data: dict) -> str:
    deadline = format_beijing_time(data.get("deadline", ""))
    return (
        f"📊【{data['assignment_name']}】作业提交统计\n"
        f"━━━━━━━━━━━━━━━\n"
        f"✅ 已提交人数：{data['submitted_count']} / {data['total_expected']} 人\n"
        f"📈 提交比例：{data['submission_rate']}\n"
        f"⚠️ 迟交人数：{data['late_count']} 人\n"
        f"⏳ 截止时间：{deadline}"
    )

def render_missing_list(data: dict) -> str:
    missing = data.get("missing_list") or []
    if not missing:
        return f"🎉【{data['assignment_name']}】全员均已按时提交完成！"

    lines = [f"📢【{data['assignment_name']}】未交作业学生名单（共 {len(missing)} 人）：", "━━━━━━━━━━━━━━━"]
    for idx, s in enumerate(missing, 1):
        cl = format_class_name(s.get("class_name", ""), s.get("student_id", ""))
        lines.append(f"{idx}. {s['name']}（{s['student_id']}，{cl}）")
    lines.append("━━━━━━━━━━━━━━━\n💡 提醒：请以上同学抓紧整理源码与实验报告，直接私聊机器人发送作业压缩包即可自动归档提交。")
    return "\n".join(lines)

def is_querying_my_submission(text: str) -> bool:
    clean = re.sub(r"[？?！!，,。.\s~～@]+", "", text.strip())
    if clean in ["查作业", "作业统计", "未交", "未交名单", "谁没交"]:
        return False
    patterns = [
        r"交.*[了吗没]",
        r"交没交",
        r"看看我",
        r"查查我",
        r"查一下我",
        r"帮我查",
        r"查我",
        r"我的作业",
        r"作业.*[吗没]",
        r"收到了[吗没]?",
        r"收到没",
        r"^查收",
        r"^看看$",
    ]
    for pat in patterns:
        if re.search(pat, clean):
            return True
    return False

async def build_student_status_card(student_id: str, student_name: str, class_name: str) -> str:
    try:
        assignments_resp = await async_api_get("/api/assignments")
        if isinstance(assignments_resp, list):
            assignments = assignments_resp
        elif isinstance(assignments_resp, dict):
            assignments = assignments_resp.get("assignments", [])
        else:
            assignments = []
    except Exception:
        assignments = []

    if not assignments:
        return "❌ 获取作业列表失败或当前未发布任何作业。"

    results = []
    for a in assignments:
        aid = a["id"]
        a_name = a["name"]
        dl = format_beijing_time(a.get("deadline", ""))
        try:
            sub_data = await async_api_get(f"/api/assignments/{aid}/submissions")
            matched_subs = [
                s for s in sub_data.get("submissions", [])
                if s.get("student_id") == student_id or s.get("student_name") == student_name
            ]
        except Exception:
            matched_subs = []

        if matched_subs:
            s = matched_subs[0]
            size_str = format_file_size(s.get("file_size", 0))
            sub_time = format_beijing_time(s.get("submitted_at", ""))
            late_tag = " ⚠️【迟交】" if s.get("is_late") else ""
            results.append(
                f"🔹【{a_name}】：\n"
                f"    ✅ 已成功提交归档{late_tag}\n"
                f"    📁 附件：{s.get('target_filename')}\n"
                f"    📦 大小：{size_str} | 🕒 提交时间：{sub_time}"
            )
        else:
            try:
                mis_data = await async_api_get(f"/api/assignments/{aid}/missing")
                is_missing = any(
                    m.get("student_id") == student_id or m.get("name") == student_name
                    for m in (mis_data.get("missing_list") or [])
                )
            except Exception:
                is_missing = False

            if is_missing:
                results.append(
                    f"🔹【{a_name}】：\n"
                    f"    ⚠️ 暂未查询到提交记录\n"
                    f"    ⏳ 截止时间：{dl}"
                )
            else:
                results.append(
                    f"🔹【{a_name}】：\n"
                    f"    ❓ 未在该次作业花名册中找到该学生"
                )

    c_show = format_class_name(class_name, student_id)
    msg = (
        f"👋【{student_name}】同学（{c_show}）你好！为你查到作业提交状态：\n"
        "━━━━━━━━━━━━━━━\n"
        + "\n".join(results)
        + "\n━━━━━━━━━━━━━━━\n"
        "💡 如需提交或更新作业，直接在私聊把新的压缩包发给我即可秒级自动入库！"
    )
    return msg

def format_plagiarism_type(dup_type_raw: str) -> tuple:
    if dup_type_raw == "exact_archive":
        return (
            "整包直接复制（压缩包完全一致）",
            "检测到你的压缩包文件哈希与已提交同学完全一致。严禁直接复制压缩包提交！请独立完成实验后再行提交。"
        )
    elif dup_type_raw == "exact_code":
        return (
            "源码完全一致（原代码未做修改）",
            "检测到你的核心源代码文件与已提交同学完全一致。严禁仅修改文件名或实验报告互相抄袭！请独立编写代码后再行提交。"
        )
    elif dup_type_raw == "normalized_code":
        return (
            "换壳抄袭（仅修改注释姓名或排版格式）",
            "检测到你的代码逻辑与已提交同学完全一致（仅修改了注释姓名或排版缩进）。系统已自动穿透注释层比对，请独立完成实验！"
        )
    elif dup_type_raw == "structural_code":
        return (
            "换壳抄袭（核心算法结构100%雷同，仅替换变量名/函数名）",
            "检测到你的代码语法结构与算法逻辑与已提交同学 100% 雷同（仅重命名了变量名或函数名）。代码抽象语法树校验未通过，请独立完成实验！"
        )
    return (
        "换壳抄袭（核心源码高度雷同）",
        "检测到你的核心代码与已提交同学高度雷同。严禁抄袭他人代码，请独立完成实验！"
    )

def render_plagiarism_alert(upload_res: dict) -> str:
    dup = upload_res.get("duplicate") or {}
    dup_type_raw = dup.get("duplicate_type", "")
    dup_type, desc_str = format_plagiarism_type(dup_type_raw)
    files = dup.get("identical_files") or []
    files_str = "、".join(files) if files else "全部代码文件"
    matched_name = dup.get("matched_student_name", "其他同学")
    matched_sid = dup.get("matched_student_id", "")
    masked_sid = (matched_sid[:4] + "****" + matched_sid[-2:]) if len(matched_sid) > 6 else matched_sid

    return (
        "⚠️【学术诚信拦截警报】\n"
        "━━━━━━━━━━━━━━━\n"
        "❌ 作业归档被拒绝：代码查重与指纹校验未通过！\n"
        f"🔍 判定类型：{dup_type}\n"
        f"📌 碰撞源码：[{files_str}]\n"
        f"👥 相同来源：同学【{matched_name}】({masked_sid})\n"
        "━━━━━━━━━━━━━━━\n"
        f"💡 说明：{desc_str}"
    )
