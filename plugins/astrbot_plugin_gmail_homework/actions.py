try:
    from astrbot.api.event import AstrMessageEvent
    from astrbot.api.message_components import At, Plain
except ImportError:
    AstrMessageEvent = object
    At = lambda qq: f"@{qq}"
    Plain = lambda text: text

def make_reply(event: AstrMessageEvent, text: str):
    sender_id = str(event.get_sender_id() or "")
    if not event.is_private_chat() and sender_id:
        clean_text = text.lstrip("\n")
        return event.chain_result([At(qq=sender_id), Plain("\n" + clean_text)])
    return event.plain_result(text)

async def upload_file_action(event: AstrMessageEvent, download_url: str, filename: str, display_name: str):
    bot = getattr(event, "bot", None)
    if not bot:
        yield make_reply(event, "❌ 内部错误：未能获取底层协议客户端。")
        return

    group_id = event.get_group_id()
    sender_id = event.get_sender_id()
    target_desc = f"群 {group_id} 的群文件" if group_id else "私聊会话"

    yield make_reply(event, f"⏳ 正在打包【{display_name}】并上传至{target_desc}，请稍候...")

    try:
        if group_id:
            await bot.call_action(
                "upload_group_file",
                group_id=str(group_id),
                file=download_url,
                name=filename,
            )
            yield make_reply(event, f"✅ 作业归档【{filename}】已成功上传至本群群文件！可前往群文件下载。")
        else:
            await bot.call_action(
                "upload_private_file",
                user_id=str(sender_id),
                file=download_url,
                name=filename,
            )
            yield make_reply(event, f"✅ 作业归档【{filename}】已作为私聊文件发送给你！")
    except Exception as e:
        # download_url can carry the API token for NapCat, so never echo it
        # into a group/private chat on failure.
        yield make_reply(event, f"❌ 上传文件失败: {e}\n💡 请联系助教重试导出。")
