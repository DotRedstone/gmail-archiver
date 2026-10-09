# [API Client]
import json
import os
import sys
import urllib.request
import urllib.parse
import aiohttp

_PLUGIN_DIR = os.path.dirname(os.path.abspath(__file__))
if _PLUGIN_DIR not in sys.path:
    sys.path.insert(0, _PLUGIN_DIR)

try:
    from .config import API_BASE
except ImportError:
    from config import API_BASE

def api_get(endpoint: str):
    url = f"{API_BASE}{endpoint}"
    req = urllib.request.Request(url, headers={"User-Agent": "AstrBot"})
    with urllib.request.urlopen(req, timeout=10) as resp:
        return json.loads(resp.read().decode("utf-8"))

async def async_api_get(endpoint: str) -> dict:
    url = f"{API_BASE}{endpoint}"
    async with aiohttp.ClientSession() as session:
        async with session.get(url, timeout=10) as resp:
            return await resp.json()

async def async_api_post_json(endpoint: str, data: dict) -> dict:
    url = f"{API_BASE}{endpoint}"
    async with aiohttp.ClientSession() as session:
        async with session.post(url, json=data, timeout=10) as resp:
            return await resp.json()

async def async_api_delete(endpoint: str) -> dict:
    url = f"{API_BASE}{endpoint}"
    async with aiohttp.ClientSession() as session:
        async with session.delete(url, timeout=10) as resp:
            return await resp.json()

async def async_upload_file(assignment_id: str, file_path: str, orig_filename: str, student_id: str, student_name: str, class_name: str, qq_id: str) -> dict:
    url = f"{API_BASE}/api/assignments/{assignment_id}/upload"
    data = aiohttp.FormData()
    data.add_field("file", open(file_path, "rb"), filename=orig_filename)
    if student_id:
        data.add_field("student_id", student_id)
    if student_name:
        data.add_field("student_name", student_name)
    if class_name:
        data.add_field("class_name", class_name)
    if qq_id:
        data.add_field("qq_id", qq_id)
        data.add_field("uploader", f"qq:{qq_id}")

    async with aiohttp.ClientSession() as session:
        async with session.post(url, data=data, timeout=60) as resp:
            return await resp.json()

def get_assignments():
    try:
        return api_get("/api/assignments")
    except Exception:
        return []

def match_assignment(query: str, assignments: list):
    if not query or not assignments:
        return None
    query = query.strip()

    if query.isdigit():
        idx = int(query)
        if 1 <= idx <= len(assignments):
            return assignments[idx - 1]

    for a in assignments:
        if query == a.get("id") or query == a.get("name"):
            return a

    q_lower = query.lower()
    for a in assignments:
        if q_lower in a.get("id", "").lower() or q_lower in a.get("name", "").lower():
            return a

    return None
