#!/usr/bin/env python3
"""按需求 Excel 的前 10 个病例分析需求生成试跑 JSONL。"""
from pathlib import Path
import json
import sys

import openpyxl

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))

from generate_batch_jsonl import make_request_line


OUTPUT = ROOT / "output" / "batch_jsonl" / "batch_病例分析题.jsonl"


def main():
    ws = openpyxl.load_workbook(ROOT / "data" / "requirements" / "急诊与麻醉科缺失.xlsx", read_only=True, data_only=True)["Sheet1"]
    requirements = [row[:3] for row in list(ws.values)[1:] if row[6]][:10]
    requests = []
    for index, (base, department, disease) in enumerate(requirements, 1):
        base_short = base.replace("基地", "").replace("科", "")
        # 保持 case-专业基地-病种-序号 结构，便于回传后自动匹配需求属性。
        custom_id = f"case-{base_short}-{disease}-trial{index:02d}"
        requests.append(make_request_line(base, department, disease, "病例分析题", custom_id))

    OUTPUT.parent.mkdir(parents=True, exist_ok=True)
    with OUTPUT.open("w", encoding="utf-8") as file:
        for request in requests:
            file.write(json.dumps(request, ensure_ascii=False) + "\n")
    print(f"{OUTPUT}: {len(requests)} 行")


if __name__ == "__main__":
    main()
