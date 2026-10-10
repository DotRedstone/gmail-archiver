"""Local, privacy-preserving policy and counters for conversational access.

The homework backend remains the authority for a student's identity.  This file
only records operational metadata needed to meter model use and alert the owner;
it deliberately never stores message text, names, student IDs, or model output.
"""

from __future__ import annotations

import datetime as dt
import os
import sqlite3
import time
from typing import Any


_DB_PATH = os.path.join(os.path.dirname(__file__), "conversation_stats.sqlite3")


def _today() -> str:
    return dt.datetime.now(dt.timezone(dt.timedelta(hours=8))).date().isoformat()


def _connection() -> sqlite3.Connection:
    conn = sqlite3.connect(_DB_PATH, timeout=5)
    conn.execute("PRAGMA journal_mode=WAL")
    conn.execute(
        """
        CREATE TABLE IF NOT EXISTS conversation_events (
            id INTEGER PRIMARY KEY,
            created_at INTEGER NOT NULL,
            day TEXT NOT NULL,
            sender_id TEXT NOT NULL,
            group_id TEXT NOT NULL DEFAULT '',
            scope TEXT NOT NULL,
            mode TEXT NOT NULL,
            reason TEXT NOT NULL DEFAULT ''
        )
        """
    )
    conn.execute(
        "CREATE INDEX IF NOT EXISTS idx_conversation_events_day_sender "
        "ON conversation_events(day, sender_id, mode)"
    )
    conn.execute(
        """
        CREATE TABLE IF NOT EXISTS conversation_alerts (
            alert_key TEXT PRIMARY KEY,
            last_sent_at INTEGER NOT NULL
        )
        """
    )
    return conn


def record_event(
    sender_id: str,
    group_id: str,
    scope: str,
    mode: str,
    reason: str = "",
) -> None:
    """Record operational metadata only; message content must never be passed."""
    with _connection() as conn:
        conn.execute(
            """
            INSERT INTO conversation_events
                (created_at, day, sender_id, group_id, scope, mode, reason)
            VALUES (?, ?, ?, ?, ?, ?, ?)
            """,
            (int(time.time()), _today(), str(sender_id), str(group_id), scope, mode, reason),
        )


def daily_llm_count(sender_id: str, day: str | None = None) -> int:
    with _connection() as conn:
        row = conn.execute(
            "SELECT COUNT(*) FROM conversation_events "
            "WHERE day = ? AND sender_id = ? AND mode = 'llm'",
            (day or _today(), str(sender_id)),
        ).fetchone()
    return int(row[0] if row else 0)


def claim_alert(alert_key: str, cooldown_seconds: int) -> bool:
    """Atomically decide whether an alert may be sent now."""
    now = int(time.time())
    with _connection() as conn:
        row = conn.execute(
            "SELECT last_sent_at FROM conversation_alerts WHERE alert_key = ?",
            (alert_key,),
        ).fetchone()
        if row and now - int(row[0]) < cooldown_seconds:
            return False
        conn.execute(
            """
            INSERT INTO conversation_alerts(alert_key, last_sent_at) VALUES (?, ?)
            ON CONFLICT(alert_key) DO UPDATE SET last_sent_at = excluded.last_sent_at
            """,
            (alert_key, now),
        )
    return True


def daily_summary(day: str | None = None, top_n: int = 8) -> dict[str, Any]:
    target_day = day or _today()
    with _connection() as conn:
        totals = dict(
            conn.execute(
                "SELECT mode, COUNT(*) FROM conversation_events WHERE day = ? GROUP BY mode",
                (target_day,),
            ).fetchall()
        )
        top_users = conn.execute(
            """
            SELECT sender_id,
                   SUM(CASE WHEN mode = 'message' THEN 1 ELSE 0 END) AS attempts,
                   SUM(CASE WHEN mode = 'llm' THEN 1 ELSE 0 END) AS llm,
                   SUM(CASE WHEN mode IN ('rate_limited', 'unbound_blocked', 'group_blocked')
                            THEN 1 ELSE 0 END) AS guarded
            FROM conversation_events
            WHERE day = ?
            GROUP BY sender_id
            ORDER BY attempts DESC, sender_id ASC
            LIMIT ?
            """,
            (target_day, top_n),
        ).fetchall()
    return {"day": target_day, "totals": totals, "top_users": top_users}
