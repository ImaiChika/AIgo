"""Excel/Word export regression: published-only, valid packages, no author column."""

import json
import os
import subprocess
import urllib.error
import urllib.parse
import urllib.request
import zipfile
import uuid
from pathlib import Path


BASE = os.environ.get("AIGO_E2E_BASE_URL", "http://127.0.0.1:5173")
PROJECT_ROOT = Path(__file__).resolve().parents[2]
EXPORT_DIR = PROJECT_ROOT / "output" / "exports"
DSN = os.environ.get("AIGO_E2E_DB_DSN", "postgres://localhost:5432/aigo?sslmode=disable")


def request(path, method="GET", body=None, token=""):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(BASE + path, data=data, method=method)
    if data is not None:
        req.add_header("Content-Type", "application/json")
    if token:
        req.add_header("Authorization", "Bearer " + token)
    try:
        with urllib.request.urlopen(req, timeout=30) as response:
            return response.status, response.read()
    except urllib.error.HTTPError as exc:
        return exc.code, exc.read()


status, raw = request("/api/auth/login", "POST", {"username": "admin", "password": "admin"})
assert status == 200, (status, raw[:200])
token = json.loads(raw)["token"]
admin_id = json.loads(raw)["user"]["id"]
question_id = "ze2e-export-" + uuid.uuid4().hex[:10]
subprocess.run([
    "psql", DSN, "-v", "ON_ERROR_STOP=1", "-c", f"""
    INSERT INTO questions (id, clinical_stem, options, answer, status, version, owner_id, created_by, created_at, updated_at, search_text)
    VALUES ('{question_id}', '男，50岁。端到端导出题，最可能的诊断是？',
      '[{{"label":"A","text":"甲"}},{{"label":"B","text":"乙"}},{{"label":"C","text":"丙"}},{{"label":"D","text":"丁"}},{{"label":"E","text":"戊"}}]'::jsonb,
      'A', 'published', 1, '{admin_id}', 'admin', NOW(), NOW(), '端到端导出题');
    INSERT INTO question_share_requests (id, question_id, owner_id, status, reviewed_by, created_at, reviewed_at)
    VALUES ('share-{question_id}', '{question_id}', '{admin_id}', 'approved', '{admin_id}', NOW(), NOW());
    """
], check=True, capture_output=True, text=True)
status, raw = request("/api/questions/search?tier=formal&scope=global&page=1&page_size=1", token=token)
assert status == 200, (status, raw[:200])
payload = json.loads(raw)
questions = payload.get("questions", payload.get("items", []))
assert questions, "本地演示库没有可导出的 published 题目"
assert any(question["id"] == question_id for question in questions), (question_id, questions)

created_files = []
try:
    for fmt in ("xlsx", "docx"):
        status, raw = request(f"/api/export/{fmt}", "POST", {"question_ids": [question_id], "scope": "global"}, token)
        assert status == 200, (fmt, status, raw[:300])
        filename = json.loads(raw)["filename"]
        path = EXPORT_DIR / filename
        created_files.append(path)
        status, content = request(f"/api/export/download/{urllib.parse.quote(filename)}", token=token)
        assert status == 200 and content, (fmt, status)
        assert b"\xe5\x91\xbd\xe9\xa2\x98\xe4\xba\xba" not in content
        assert b"\xe6\x9c\x9d\xe9\x98\xb3\xe5\x8c\xbb\xe9\x99\xa2AI" not in content
        assert path.is_file()
        with zipfile.ZipFile(path) as archive:
            names = archive.namelist()
            xml_parts = b"".join(archive.read(name) for name in names if name.endswith(".xml"))
        assert "命题人".encode() not in xml_parts
        assert "朝阳医院AI".encode() not in xml_parts
        if fmt == "xlsx":
            with zipfile.ZipFile(path) as xlsx_archive:
                sheet_name = next(name for name in xlsx_archive.namelist() if name.startswith("xl/worksheets/") and name.endswith(".xml"))
                sheet = xlsx_archive.read(sheet_name)
            assert b"r=\"O1\"" in sheet
            assert b"r=\"P1\"" not in sheet
        print(f"PASS {fmt}: published export/download valid and author fields absent")
finally:
    for path in created_files:
        path.unlink(missing_ok=True)
    subprocess.run(["psql", DSN, "-c", f"DELETE FROM questions WHERE id='{question_id}'"], check=True, capture_output=True, text=True)
