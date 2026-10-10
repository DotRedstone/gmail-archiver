"""Conservative routing for course facts that must come from live data."""

import re


def classify_course_query(message: str) -> tuple[str, str] | None:
    """Return (skill, assignment hint) only for clear factual course requests."""
    text = re.sub(r"\[At:\d+\]|@\S+", "", message or "").strip()
    compact = re.sub(r"\s+", "", text)
    if not compact:
        return None

    if re.search(r"我(?:是|算|属于).*(?:助教|管理员)|我(?:有|的)?(?:什么)?(?:权限|角色)|(?:助教|管理员).*我(?:是|吗)", compact):
        return "my_role", ""
    if re.search(r"(?:助教|管理员|教学团队|教学管理团队|管理团队|特殊身份).*(?:谁|哪些|名单|列表|有谁|成员)|(?:谁|哪些人).*(?:是|当|担任)(?:助教|管理员)", compact):
        return "teaching_team", ""
    if re.search(r"(?:谁|哪些人|哪些同学).*(?:没交|未交)|(?:未交|没交).*(?:名单|人员|学生|同学|有谁|谁)", compact):
        match = re.search(r"(?:实验|lab)\s*\d+", text, re.I)
        return "missing_students", match.group(0) if match else ""
    if re.search(r"(?:未绑定|没绑定).*(?:名单|谁|哪些|学生|人员)|(?:谁|哪些人).*没绑定", compact):
        return "unbound_roster", ""
    if re.search(r"(?:班上|班级|全班|班里).*(?:人员|学生|同学|名单|有谁)|(?:人员|学生|绑定)名单", compact):
        return "course_roster", ""
    if compact in {"我是谁", "我的信息", "我的绑定", "查询绑定"}:
        return "my_identity", ""
    if re.search(r"(?:我|我的).*(?:作业|实验).*(?:交了|提交|归档|状态|怎么样)|(?:我交了(?:吗|没)?|我有没有交)", compact):
        return "my_homework", ""
    return None
