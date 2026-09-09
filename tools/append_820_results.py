#!/usr/bin/env python3
"""审查 820 批量结果，并将合格的单题组追加到 8.19 Excel。"""
from collections import Counter, defaultdict
from pathlib import Path
import json

import openpyxl

from append_819_results import (
    RESULT_DIR,
    existing_group_stems,
    extract_items,
    load_responses,
    read_targets,
    source_key,
    valid_group,
)


ROOT = Path(__file__).resolve().parents[1]
NEW_DIR = ROOT / "820"
NEW_FILES = {
    "A3": "caf49412-538e-4849-ad44-4b76c5d9e230_1787218126613_success.jsonl",
    "A4": "011f6791-5dfb-444f-9460-8b0642419f6a_1787221277815_success.jsonl",
    "病例分析题": "bcc5ef07-7dbc-4520-9ea6-8b4696ff0716_1787221349689_success.jsonl",
}
MIN_STEM = {"A3": 100, "A4": 120, "病例分析题": 150}


def normalized(value):
    import re

    return re.sub(r"[^\w\u4e00-\u9fff]+", "", str(value)).lower()


def option_text(question):
    return "\n".join(
        f"{option['label']}. {option['text'].strip()}" for option in question["options"]
    )


def explanation(question):
    return question.get("explanation", question.get("analysis", "")).strip()


def metadata(question, group):
    return (
        question.get("difficulty", group.get("difficulty", "")),
        question.get("cognitive_level", group.get("cognitive_level", "")),
        question.get("exam_points", group.get("exam_points", "")),
    )


def append_group(ws, question_type, source_ref, group):
    for question in group["questions"]:
        difficulty, cognitive, points = metadata(question, group)
        ws.append(
            [
                question_type,
                source_ref,
                group["clinical_stem"].strip(),
                question["stem"].strip(),
                option_text(question),
                question["answer"].strip().upper(),
                explanation(question),
                difficulty,
                cognitive,
                points,
            ]
        )


def load_new(question_type):
    path = NEW_DIR / NEW_FILES[question_type]
    return [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines() if line]


def import_type(question_type, targets):
    xlsx_path = RESULT_DIR / f"{question_type}.xlsx"
    wb = openpyxl.load_workbook(xlsx_path)
    ws = wb.active

    old_responses = load_responses(question_type)
    existing = existing_group_stems(ws, old_responses, question_type, targets)
    current = {key: len(stems) for key, stems in existing.items()}
    new_responses = load_new(question_type)

    audit = Counter()
    added_groups = added_rows = 0
    for line_number, response in enumerate(new_responses, 1):
        key = source_key(question_type, response["custom_id"], targets)
        if key not in targets:
            audit["unexpected_custom_id"] += 1
            continue
        body = response.get("response", {}).get("body", {})
        choices = body.get("choices", [])
        if response.get("response", {}).get("status_code") != 200 or not choices:
            audit["api_error"] += 1
            continue
        content = choices[0].get("message", {}).get("content", "")
        groups = extract_items(content)
        if len(groups) != 1:
            audit["not_exactly_one_group"] += 1
            continue
        group = groups[0]
        if not valid_group(group, question_type):
            audit["invalid_structure_or_short_content"] += 1
            continue
        fingerprint = normalized(group["clinical_stem"])
        if fingerprint in existing[key]:
            audit["duplicate_case"] += 1
            continue
        if current.get(key, 0) >= targets[key]:
            audit["over_target"] += 1
            continue
        source_ref = f"820/{NEW_FILES[question_type]}:{line_number}"
        append_group(ws, question_type, source_ref, group)
        existing[key].add(fingerprint)
        current[key] = current.get(key, 0) + 1
        added_groups += 1
        added_rows += len(group["questions"])
        audit["accepted"] += 1

    wb.save(xlsx_path)
    missing = {
        f"{key[1]}｜{key[2]}": target - current.get(key, 0)
        for key, target in targets.items()
        if key[0] == question_type and current.get(key, 0) < target
    }
    return {
        "accepted_groups": added_groups,
        "appended_question_rows": added_rows,
        "rejected": dict(audit),
        "remaining_groups": missing,
    }


def main():
    targets = read_targets()
    report = {question_type: import_type(question_type, targets) for question_type in NEW_FILES}
    report_path = NEW_DIR / "quality_import_report.json"
    report_path.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(report, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
