#!/usr/bin/env python3
"""将 8.19 批量返回中结构完整的题目补齐到对应 Excel 配额。"""
from collections import defaultdict
from pathlib import Path
import json
import re

import openpyxl


ROOT = Path(__file__).resolve().parents[1]
RESULT_DIR = ROOT / "8.19"
SOURCE_XLSX = ROOT / "data" / "requirements" / "急诊与麻醉科缺失.xlsx"

TYPE_FILES = {
    "A1": "87676221-c539-494d-9dbc-b7cb706b22b3_1787166045318_success.jsonl",
    "A2": "b980cead-a128-4d36-b302-8964e3ff0808_1787173956550_success.jsonl",
    "A3": "8f8f2a6a-5432-4f56-ad15-adcdb1939517_1787176322622_success.jsonl",
    "A4": "abfb4702-5736-43a1-a4f4-754ee45e28db_1787175996825_success.jsonl",
    "病例分析题": "1ef21092-bd98-42fc-a964-dd67cafbeb2d_1787181086323_success.jsonl",
}


def normalized(value):
    return re.sub(r"[^\w\u4e00-\u9fff]+", "", str(value)).lower()


def short_base(base):
    return base.replace("基地", "").replace("科", "")


def read_targets():
    wb = openpyxl.load_workbook(SOURCE_XLSX, read_only=True, data_only=True)
    ws = wb["Sheet1"]
    targets = {}
    for base, _, disease, a1, a2, a34, case in list(ws.values)[1:]:
        base = short_base(base)
        for question_type, enabled, count in (
            ("A1", a1, 50),
            ("A2", a2, 50),
            # 每个需求项均按 50 个题组计，不再将 A3/4 合并为 50。
            ("A3", a34, 50),
            ("A4", a34, 50),
            ("病例分析题", case, 50),
        ):
            if enabled:
                targets[(question_type, base, disease)] = count
    return targets


def extract_items(content):
    if not isinstance(content, str):
        return []
    start = content.find("[")
    if start < 0:
        return []
    decoder = json.JSONDecoder()
    position = start + 1
    items = []
    while position < len(content):
        while position < len(content) and (content[position].isspace() or content[position] == ","):
            position += 1
        try:
            item, position = decoder.raw_decode(content, position)
        except json.JSONDecodeError:
            break
        items.append(item)
    return items


def option_pairs(item):
    options = item.get("options") if isinstance(item, dict) else None
    if isinstance(options, list) and len(options) == 5:
        if all(isinstance(option, dict) for option in options):
            pairs = [(option.get("label"), option.get("text")) for option in options]
        elif all(isinstance(option, str) for option in options):
            pairs = list(zip("ABCDE", options))
        else:
            return None
    else:
        question = item.get("question", "") if isinstance(item, dict) else ""
        found = re.findall(r"(?m)^\s*([A-E])[.、:：]\s*(.+?)\s*$", question)
        if len(found) != 5:
            return None
        pairs = found
    if [label for label, _ in pairs] != list("ABCDE"):
        return None
    texts = []
    for label, text in pairs:
        if not isinstance(text, str):
            return None
        text = re.sub(rf"^\s*{label}[.、:：]\s*", "", text).strip()
        if "以上都是" in text or "以上都不是" in text:
            return None
        texts.append(text)
    if not all(texts) or len({normalized(text) for text in texts}) != 5:
        return None
    return list(zip("ABCDE", texts))


def explanation(item):
    value = item.get("explanation", item.get("analysis", ""))
    return value.strip() if isinstance(value, str) else ""


def valid_question(item, stem_key, min_stem=1, min_explanation=1):
    stem = item.get(stem_key, "") if isinstance(item, dict) else ""
    answer = item.get("answer", "") if isinstance(item, dict) else ""
    return (
        isinstance(stem, str)
        and len(stem.strip()) >= min_stem
        and option_pairs(item) is not None
        and isinstance(answer, str)
        and answer.strip().upper() in set("ABCDE")
        and len(explanation(item)) >= min_explanation
    )


def valid_group(item, question_type):
    counts = {"A3": (2, 3), "A4": (3, 5), "病例分析题": (3, 5)}
    min_stem = {"A3": 100, "A4": 120, "病例分析题": 150}
    lower, upper = counts[question_type]
    questions = item.get("questions") if isinstance(item, dict) else None
    return (
        isinstance(item, dict)
        and isinstance(item.get("clinical_stem"), str)
        and len(item["clinical_stem"].strip()) >= min_stem[question_type]
        and isinstance(questions, list)
        and lower <= len(questions) <= upper
        and all(valid_question(question, "stem", min_stem=8, min_explanation=50) for question in questions)
    )


def source_key(question_type, custom_id, targets):
    prefix = custom_id.rsplit("-", 1)[0].split("-")[1:]
    if question_type == "A1":
        disease = "-".join(prefix)
        matches = [key for key in targets if key[0] == question_type and key[2] == disease]
        return matches[0] if len(matches) == 1 else None
    if len(prefix) < 2:
        return None
    return question_type, prefix[0], "-".join(prefix[1:])


def existing_stems(ws):
    return {normalized(row[2]) for row in ws.iter_rows(min_row=2, values_only=True) if len(row) > 2 and row[2]}


def existing_group_stems(ws, responses, question_type, targets):
    by_key = defaultdict(set)
    for row in ws.iter_rows(min_row=2, values_only=True):
        if len(row) < 3 or not isinstance(row[1], int) or not row[2]:
            continue
        source_line = row[1]
        if not 1 <= source_line <= len(responses):
            continue
        key = source_key(question_type, responses[source_line - 1]["custom_id"], targets)
        if key:
            by_key[key].add(normalized(row[2]))
    return by_key


def option_text(item):
    return "\n".join(f"{label}. {text}" for label, text in option_pairs(item))


def metadata(item, fallback=None):
    fallback = fallback or {}
    difficulty = item.get("difficulty", fallback.get("difficulty", ""))
    cognitive = item.get("cognitive_level", fallback.get("cognitive_level", ""))
    points = item.get(
        "exam_points",
        item.get(
            "assessment_focus",
            item.get("focus_area", fallback.get("exam_points", item.get("syllabus_code", ""))),
        ),
    )
    return difficulty, cognitive, points


def question_row(question_type, source_line, item, stem_key, clinical_stem=None, fallback=None):
    difficulty, cognitive, points = metadata(item, fallback)
    if clinical_stem is None:
        return [
            question_type,
            source_line,
            item[stem_key].strip(),
            option_text(item),
            item["answer"].strip().upper(),
            explanation(item),
            difficulty,
            cognitive,
            points,
        ]
    return [
        question_type,
        source_line,
        clinical_stem.strip(),
        item[stem_key].strip(),
        option_text(item),
        item["answer"].strip().upper(),
        explanation(item),
        difficulty,
        cognitive,
        points,
    ]


def load_responses(question_type):
    path = RESULT_DIR / TYPE_FILES[question_type]
    return [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines() if line.strip()]


def append_a1_or_a2(ws, responses, question_type, targets):
    count_by_key = defaultdict(int)
    present = set()
    added = 0
    for source_line, response in enumerate(responses, 1):
        key = source_key(question_type, response["custom_id"], targets)
        if key not in targets:
            continue
        content = response["response"]["body"]["choices"][0]["message"].get("content", "")
        for item in extract_items(content):
            stem_key = "question" if question_type == "A1" else "clinical_stem"
            min_stem, min_explanation = (10, 30) if question_type == "A1" else (60, 50)
            if count_by_key[key] >= targets[key] or not valid_question(item, stem_key, min_stem, min_explanation):
                continue
            stem = item[stem_key]
            if question_type == "A1":
                pairs = option_pairs(item)
                stem = re.sub(r"(?m)^\s*[A-E][.、:：]\s*.+?\s*$", "", stem).strip()
                item = dict(item)
                item["question"] = stem
                item["options"] = [{"label": label, "text": text} for label, text in pairs]
            fingerprint = normalized(stem)
            if not fingerprint or fingerprint in present:
                continue
            ws.append(question_row(question_type, source_line, item, stem_key))
            present.add(fingerprint)
            count_by_key[key] += 1
            added += 1
    return added, count_by_key


def append_groups(ws, responses, question_type, targets):
    count_by_key = defaultdict(int)
    present = defaultdict(set)
    added_groups = 0
    added_rows = 0
    for source_line, response in enumerate(responses, 1):
        key = source_key(question_type, response["custom_id"], targets)
        if key not in targets or count_by_key.get(key, 0) >= targets[key]:
            continue
        content = response["response"]["body"]["choices"][0]["message"].get("content", "")
        for group in extract_items(content):
            if count_by_key.get(key, 0) >= targets[key] or not valid_group(group, question_type):
                continue
            fingerprint = normalized(group["clinical_stem"])
            if not fingerprint or fingerprint in present[key]:
                continue
            for question in group["questions"]:
                ws.append(question_row(question_type, source_line, question, "stem", group["clinical_stem"], group))
                added_rows += 1
            present[key].add(fingerprint)
            count_by_key[key] = count_by_key.get(key, 0) + 1
            added_groups += 1
    return added_rows, added_groups, count_by_key


def main():
    targets = read_targets()
    for question_type in TYPE_FILES:
        path = RESULT_DIR / f"{question_type}.xlsx"
        wb = openpyxl.load_workbook(path)
        ws = wb.active
        responses = load_responses(question_type)
        # 旧表中含有把截断输出碎片误拆为题目的记录。仅保留表头，按统一质检标准重建。
        if ws.max_row > 1:
            ws.delete_rows(2, ws.max_row - 1)
        if question_type in {"A1", "A2"}:
            rows, per_key = append_a1_or_a2(ws, responses, question_type, targets)
            print(f"{question_type}: 新增 {rows} 题")
        else:
            rows, groups, per_key = append_groups(ws, responses, question_type, targets)
            print(f"{question_type}: 新增 {groups} 个题组、{rows} 个题目")
        wb.save(path)
        incomplete = {
            key: target - per_key.get(key, 0)
            for key, target in targets.items()
            if key[0] == question_type and per_key.get(key, 0) < target
        }
        if incomplete:
            print(f"{question_type}: 未达 50 题组的缺口 {incomplete}")


if __name__ == "__main__":
    main()
