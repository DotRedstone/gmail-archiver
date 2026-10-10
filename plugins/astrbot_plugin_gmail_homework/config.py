# [Config]
import json
import os
try:
    from astrbot.api.event import AstrMessageEvent
except ImportError:
    AstrMessageEvent = object

CONFIG_PATH = os.path.join(os.path.dirname(__file__), "plugin_config.json")

def load_config() -> dict:
    default_cfg = {
        "class_groups": [],
        "teaching_assistants": [],
        # LLM is the normal conversation path. The threshold only raises a
        # rate-limited operational alert; it never degrades a valid request.
        "conversation": {
            "daily_llm_alert_threshold": 100,
            "alert_cooldown_seconds": 1800,
        },
    }
    if os.path.exists(CONFIG_PATH):
        try:
            with open(CONFIG_PATH, "r", encoding="utf-8") as f:
                data = json.load(f)
                if isinstance(data, dict):
                    default_cfg.update(data)
                    return default_cfg
        except Exception:
            pass
    return default_cfg


# Deployment-specific endpoints and identifiers never belong in Git. Prefer
# environment variables, but permit the deployment-owned plugin config so a
# container can be upgraded without baking personal values into its image.
_runtime_cfg = load_config()
API_BASE = os.environ.get(
    "GMAIL_ARCHIVER_API_BASE", str(_runtime_cfg.get("api_base", "https://archiver.example.invalid"))
).rstrip("/")
API_KEY = os.environ.get("GMAIL_ARCHIVER_API_KEY", "").strip()
SUPER_ADMIN_QQ = os.environ.get(
    "GMAIL_ARCHIVER_SUPER_ADMIN_QQ", str(_runtime_cfg.get("super_admin_qq", ""))
).strip()

def save_config(cfg: dict):
    try:
        with open(CONFIG_PATH, "w", encoding="utf-8") as f:
            json.dump(cfg, f, ensure_ascii=False, indent=2)
    except Exception as e:
        print(f"Failed to save config: {e}")

def get_class_groups() -> list:
    return load_config().get("class_groups", [])


def get_conversation_policy() -> dict:
    cfg = load_config().get("conversation", {})
    if not isinstance(cfg, dict):
        cfg = {}
    try:
        daily_llm_alert_threshold = max(1, int(cfg.get("daily_llm_alert_threshold", 100)))
    except (TypeError, ValueError):
        daily_llm_alert_threshold = 100
    try:
        alert_cooldown_seconds = max(60, int(cfg.get("alert_cooldown_seconds", 1800)))
    except (TypeError, ValueError):
        alert_cooldown_seconds = 1800
    return {
        "daily_llm_alert_threshold": daily_llm_alert_threshold,
        "alert_cooldown_seconds": alert_cooldown_seconds,
    }

def add_class_group(group_id: str) -> bool:
    cfg = load_config()
    groups = cfg.get("class_groups", [])
    if group_id not in groups:
        groups.append(group_id)
        cfg["class_groups"] = groups
        save_config(cfg)
        return True
    return False

def remove_class_group(group_id: str) -> bool:
    cfg = load_config()
    groups = cfg.get("class_groups", [])
    if group_id in groups:
        groups.remove(group_id)
        cfg["class_groups"] = groups
        save_config(cfg)
        return True
    return False

# [Auth]
def get_teaching_assistants() -> list:
    return [str(q) for q in load_config().get("teaching_assistants", [])]

def add_teaching_assistant(qq_id: str) -> bool:
    qq = str(qq_id).strip()
    if not qq.isdigit():
        return False
    cfg = load_config()
    tas = [str(q) for q in cfg.get("teaching_assistants", [])]
    if qq not in tas:
        tas.append(qq)
        cfg["teaching_assistants"] = tas
        save_config(cfg)
        return True
    return False

def remove_teaching_assistant(qq_id: str) -> bool:
    qq = str(qq_id).strip()
    cfg = load_config()
    tas = [str(q) for q in cfg.get("teaching_assistants", [])]
    if qq in tas:
        tas.remove(qq)
        cfg["teaching_assistants"] = tas
        save_config(cfg)
        return True
    return False

def is_super_admin(event: AstrMessageEvent) -> bool:
    return bool(SUPER_ADMIN_QQ) and str(event.get_sender_id() or "") == SUPER_ADMIN_QQ

def is_ta_or_admin(event: AstrMessageEvent) -> bool:
    sender_id = str(event.get_sender_id() or "")
    if SUPER_ADMIN_QQ and sender_id == SUPER_ADMIN_QQ:
        return True
    return sender_id in get_teaching_assistants()

def is_admin(event: AstrMessageEvent) -> bool:
    return is_ta_or_admin(event)
