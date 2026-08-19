#!/usr/bin/env python3
"""
从急诊与麻醉科缺失.xlsx 生成 DashScope 批量推理 JSONL 文件。
每个任务生成50道题，每道题一个JSON行。
每种题型使用独立的 system prompt。
A2 prompt 完全对齐项目中已验证的标准版本。
"""
import json
import openpyxl

# ========== 通用规则（各题型共用） ==========
# 从项目 internal/batch/service.go getSystemPrompt() 提取，保持一致

COMMON_RULES = """【命题总原则】
1. 以《考试大纲》为依据，以人卫社统编教材为内容基础
2. 执业医师命题以本科生毕业后培训一年的水平为标准
3. 所命试题应是新编原创试题，采用新的素材或临床情景，避免照搬书本现成实例

【内容要求】
1. 试题内容科学、正确
2. 正确答案唯一、且无学术上的争议
3. 内容取样有较好的代表性，避免出偏题、怪题
4. 试题必须有明确的主题，题干和备选答案必须围绕同一知识点，避免在一道题中考查多个知识点
5. 避免存在性别、种族、地域、文化的不公平或歧视
6. 必须使用规范的医学术语，名词术语、药物名称、化验数值和计量单位须准确、规范

【题干要求】
1. 题干叙述简明扼要，包含回答问题所必需的全部要素
2. 题干提出的问题具体明确，使应试者一看到题干就能明确要考察的知识点和具体内容
3. 题干中不能包括备选答案或对回答问题有所暗示
4. 题干尽量以叙述式书写

【备选答案要求】
1. 备选答案之间不能有相互重叠、相互依赖的内容
2. 备选答案应在性质上、类别上相同，在逻辑、语法和内容长短上也应基本一致
3. 备选答案中避免无意义或无用的干扰答案
4. 不使用"以上都是"和"以上都不是"作为备选答案
5. 备选答案与题干符合逻辑性
6. 备选答案按逻辑顺序排列
7. 备选答案中的相同表述，统一合理地放到题干中

【文字要求】
1. 试题所用文字简明、扼要，避免生僻、艰涩、洋化用语
2. 避免暗示或模糊性用语，杜绝错别字
3. 尽量避免使用否定式，必须使用否定句时用黑体加粗强调否定词，杜绝使用双重否定

【输出要求】
- 严格输出JSON数组，不要输出任何其他文字
- 选项五选一
- 每道题必须包含完整解析，说明正确答案依据和干扰项错误原因
- 难度以0-1之间两位小数表示（0.05的倍数），如0.65
- 认知层次：记忆/理解/简单应用/综合应用 选一
- 考核要点标注具体，如：诊断与鉴别诊断，临床表现，辅助检查
- 大纲代码标到最后一级"""


# ========== A1 System Prompt ==========

SYSTEM_A1 = f"""你是医学考试命题助手，负责生成国家执业医师考试A1型单选题。

【A1型题定义】
A1型题是单句型最佳选择题。题干为一个简短的问题或不完整的陈述（通常1-2句话），不描述临床情境，直接考查知识点。

【A1型题考查方向】
- 病因、发病机制
- 临床表现、典型特征
- 辅助检查的典型结果
- 诊断标准、诊断要点
- 治疗原则、首选药物
- 适应证、禁忌证

【认知层次】以"记忆"和"理解"为主。

【典型题干示例】
• "下列关于XXX的说法，正确的是？"
• "XXX最常见的病因是？"
• "下列哪项是XXX的典型表现？"
• "XXX的首选治疗药物是？"

{COMMON_RULES}"""


# ========== A2 System Prompt（完全对齐项目标准） ==========

SYSTEM_A2 = """你是医学考试命题助手，负责生成国家执业医师考试A2型单选题。

【一、命题总原则】
1. 以《考试大纲》为依据，以人卫社统编教材为内容基础
2. 执业医师命题以本科生毕业后培训一年的水平为标准
3. 所命试题应是新编原创试题，采用新的素材或临床情景，避免照搬书本现成实例

【二、A2型题格式】
题干按病历书写顺序：一般情况→主诉→现病史→既往史→查体→辅助检查→提问
- 一般情况：男/女，年龄
- 主诉：主要症状或体征+时间
- 现病史：发病诱因、症状特点、伴随症状、诊治经过
- 既往史：根据病例需要编写
- 查体：按生命征→一般情况→头颈→肺→心→腹→脊柱→四肢→神经系统顺序
- 辅助检查：常用检查执业医师以英文表示
- 提问：该患者最可能的诊断是 / 最有价值的检查是 / 治疗原则是

【三、内容要求】
1. 试题内容科学、正确
2. 正确答案唯一、且无学术上的争议
3. 内容取样有较好的代表性，避免出偏题、怪题
4. 试题必须有明确的主题，题干和备选答案必须围绕同一知识点，避免在一道题中考查多个知识点
5. 避免存在性别、种族、地域、文化的不公平或歧视
6. 必须使用规范的医学术语，名词术语、药物名称、化验数值和计量单位须准确、规范

【四、题干要求】
1. 题干叙述简明扼要，包含回答问题所必需的全部要素
2. 题干提出的问题具体明确，使应试者一看到题干就能明确要考察的知识点和具体内容
3. 题干中不能包括备选答案或对回答问题有所暗示
4. 题干尽量以叙述式书写

【五、备选答案要求】
1. 备选答案之间不能有相互重叠、相互依赖的内容
2. 备选答案应在性质上、类别上相同，在逻辑、语法和内容长短上也应基本一致
3. 备选答案中避免无意义或无用的干扰答案
4. 不使用"以上都是"和"以上都不是"作为备选答案
5. 备选答案与题干符合逻辑性
6. 备选答案按逻辑顺序排列
7. 备选答案中的相同表述，统一合理地放到题干中

【六、文字要求】
1. 试题所用文字简明、扼要，避免生僻、艰涩、洋化用语
2. 避免暗示或模糊性用语，杜绝错别字
3. 尽量避免使用否定式，必须使用否定句时用黑体加粗强调否定词，杜绝使用双重否定

【七、输出要求】
- 严格输出JSON数组，不要输出任何其他文字
- 选项五选一
- 每道题必须包含完整解析，说明正确答案依据和干扰项错误原因
- 难度以0-1之间两位小数表示（0.05的倍数），如0.65
- 认知层次：记忆/理解/简单应用/综合应用 选一
- 考核要点标注具体，如：诊断与鉴别诊断，临床表现，辅助检查
- 大纲代码标到最后一级"""


# ========== A3 System Prompt ==========

SYSTEM_A3 = f"""你是医学考试命题助手，负责生成国家执业医师考试A3型题（病例组型题）。

【A3型题定义】
A3型题以一个完整的临床病例为背景（比A2更详细），围绕该病例提出2-3个问题。每个问题有5个备选答案，各自独立选择。

【A3型题特征】
- 病例描述完整但固定，不随时间变化（静态病例）
- 问题之间有逻辑关联但不递进（如：诊断→检查→治疗）
- 病例比A2更详细，包含完整的诊疗经过
- 辅助检查以英文表示

【A3型题输出格式】
输出JSON数组，每个元素为一组题：
{{
  "clinical_stem": "完整病例描述（一般情况→主诉→现病史→既往史→查体→辅助检查）",
  "questions": [
    {{"stem": "问题1", "options": [{{"label": "A", "text": "..."}},...], "answer": "A", "explanation": "..."}},
    {{"stem": "问题2", "options": [...], "answer": "...", "explanation": "..."}},
    {{"stem": "问题3", "options": [...], "answer": "...", "explanation": "..."}}
  ],
  "difficulty": "0.65",
  "cognitive_level": "综合应用",
  "exam_points": "考核要点"
}}

【认知层次】以"综合应用"为主。

{COMMON_RULES}"""


# ========== A4 System Prompt ==========

SYSTEM_A4 = f"""你是医学考试命题助手，负责生成国家执业医师考试A4型题（病例串型题）。

【A4型题定义】
A4型题以一个逐步发展的临床病例为背景（核心特征：病情随时间演变），围绕该病例提出3-5个问题。

【A4型题特征——与A3的核心区别】
- 病例按时间线展开，包含多个阶段：
  • 第一阶段：初诊（主诉、查体、初步检查）
  • 第二阶段：初步处理后复诊（病情变化、新检查结果）
  • 第三阶段：进一步处理或出现并发症（治疗反应、新问题）
  • 可根据需要增加第四、五阶段
- 每个阶段可增加新的信息（新症状、新检查结果、治疗反应）
- 每个问题对应病例发展的不同阶段
- 后续问题可能基于前面问题的处理结果或新出现的情况

【A4型题输出格式】
输出JSON数组，每个元素为一组题：
{{
  "clinical_stem": "按时间线发展的完整病例描述（初诊→复诊→病情变化→进一步处理）",
  "questions": [
    {{"stem": "第一阶段问题", "options": [...], "answer": "...", "explanation": "..."}},
    {{"stem": "第二阶段问题", "options": [...], "answer": "...", "explanation": "..."}},
    {{"stem": "第三阶段问题", "options": [...], "answer": "...", "explanation": "..."}}
  ],
  "difficulty": "0.70",
  "cognitive_level": "综合应用",
  "exam_points": "考核要点"
}}

【认知层次】以"综合应用"为主。

{COMMON_RULES}"""


# ========== 病例分析题 System Prompt ==========

SYSTEM_CASE = f"""你是医学考试命题助手，负责生成国家执业医师考试病例分析题。

【病例分析题定义】
病例分析题描述一个复杂的临床病例，围绕该病例提出3-5个问题，全面考查临床思维能力。

【病例分析题特征——比A4更复杂】
- 病例更复杂，通常涉及鉴别诊断、并发症处理
- 问题必须覆盖以下方面（至少覆盖3个）：
  ① 初步诊断及诊断依据（从年龄、症状、体征、辅助检查等角度逐条列出）
  ② 鉴别诊断（需排除哪些疾病，各疾病的鉴别要点）
  ③ 为进一步明确诊断还需做的检查（列出有针对性的检查项目）
  ④ 治疗原则和具体方案（一般治疗、对症治疗、病因治疗）
  ⑤ 并发症的识别与处理
  ⑥ 预后判断和随访要点
- 辅助检查以英文表示

【病例分析题输出格式】
输出JSON数组，每个元素为一道病例分析题：
{{
  "clinical_stem": "完整复杂病例描述（详细的病史、查体、辅助检查、诊疗经过）",
  "questions": [
    {{"stem": "问题1（如：初步诊断及诊断依据）", "options": [...], "answer": "...", "explanation": "..."}},
    {{"stem": "问题2（如：鉴别诊断）", "options": [...], "answer": "...", "explanation": "..."}},
    {{"stem": "问题3（如：进一步检查）", "options": [...], "answer": "...", "explanation": "..."}},
    {{"stem": "问题4（如：治疗原则）", "options": [...], "answer": "...", "explanation": "..."}}
  ],
  "difficulty": "0.70",
  "cognitive_level": "综合应用",
  "exam_points": "考核要点"
}}

【认知层次】以"综合应用"为主。

{COMMON_RULES}"""


# ========== User Prompt 模板 ==========

def build_user_prompt(base, dept, disease, count, qtype_desc):
    return f"""请根据以下信息，生成{count}{qtype_desc}。

【专业基地】{base}
【科室】{dept}
【病种/知识点】{disease}

只输出JSON数组，不要输出其他任何内容。"""


# ========== A2 User Prompt（对齐标准格式，含知识点信息） ==========

def build_user_prompt_a2(base, dept, disease, count):
    return f"""请根据以下信息，生成{count}道国家执业医师考试A2型单选题。

【专业基地】{base}
【科室】{dept}
【病种/知识点】{disease}

【输出格式】
输出一个JSON数组，每个元素包含以下字段：
{{
  "clinical_stem": "临床情境题干（按病历顺序书写）",
  "options": [
    {{"label": "A", "text": "选项文本"}},
    {{"label": "B", "text": "选项文本"}},
    {{"label": "C", "text": "选项文本"}},
    {{"label": "D", "text": "选项文本"}},
    {{"label": "E", "text": "选项文本"}}
  ],
  "answer": "正确答案标签",
  "explanation": "完整解析：说明正确答案依据和干扰项错误原因",
  "difficulty": "0.65",
  "cognitive_level": "记忆/理解/简单应用/综合应用 选一",
  "exam_points": "考核要点，如：诊断与鉴别诊断，临床表现"
}}

只输出JSON数组，不要输出其他任何内容。"""


# ========== 题型配置 ==========
TYPE_CONFIG = {
    "A1": {
        "system": SYSTEM_A1,
        "user_fn": lambda base, dept, disease: build_user_prompt(base, dept, disease, 50, "道国家执业医师考试A1型单选题"),
        "max_tokens": 6000,
        "temperature": 0.5,
    },
    "A2": {
        "system": SYSTEM_A2,
        "user_fn": lambda base, dept, disease: build_user_prompt_a2(base, dept, disease, 50),
        "max_tokens": 8000,
        "temperature": 0.4,  # 对齐标准 temperature=0.4
    },
    "A3": {
        "system": SYSTEM_A3,
        "user_fn": lambda base, dept, disease: build_user_prompt(base, dept, disease, 50, "组国家执业医师考试A3型题（病例组型题，每组2-3个问题）"),
        "max_tokens": 10000,
        "temperature": 0.5,
    },
    "A4": {
        "system": SYSTEM_A4,
        "user_fn": lambda base, dept, disease: build_user_prompt(base, dept, disease, 50, "组国家执业医师考试A4型题（病例串型题，每组3-5个问题，病例按时间线发展）"),
        "max_tokens": 12000,
        "temperature": 0.5,
    },
    "病例分析题": {
        "system": SYSTEM_CASE,
        "user_fn": lambda base, dept, disease: build_user_prompt(base, dept, disease, 50, "道国家执业医师考试病例分析题（每道3-5个问题，覆盖诊断/鉴别/检查/治疗/并发症）"),
        "max_tokens": 16000,
        "temperature": 0.5,
    },
}


def make_request_line(base, dept, disease, qtype, custom_id):
    cfg = TYPE_CONFIG[qtype]
    user_prompt = cfg["user_fn"](base, dept, disease)

    return {
        "custom_id": custom_id,
        "method": "POST",
        "url": "/v1/chat/completions",
        "body": {
            "model": "qwen3.5-flash",
            "messages": [
                {"role": "system", "content": cfg["system"]},
                {"role": "user", "content": user_prompt},
            ],
            "temperature": cfg["temperature"],
            "max_tokens": cfg["max_tokens"],
            "enable_thinking": True,
        },
    }


def main():
    wb = openpyxl.load_workbook("急诊与麻醉科缺失.xlsx")
    ws = wb["Sheet1"]
    rows = list(ws.iter_rows(values_only=True))
    data_rows = rows[1:]

    files_by_type = {"A1": [], "A2": [], "A3": [], "A4": [], "病例分析题": []}
    total_lines = 0

    for row in data_rows:
        base, dept, disease, a1, a2, a34, case = row

        # 用专业基地前缀防止不同科室同病种的 custom_id 冲突
        base_short = base.replace("基地", "").replace("科", "")

        if a1:
            for i in range(50):
                cid = f"a1-{base_short}-{disease}-{i+1:03d}"
                files_by_type["A1"].append(make_request_line(base, dept, disease, "A1", cid))
                total_lines += 1

        if a2:
            for i in range(50):
                cid = f"a2-{base_short}-{disease}-{i+1:03d}"
                files_by_type["A2"].append(make_request_line(base, dept, disease, "A2", cid))
                total_lines += 1

        if a34:
            for i in range(25):
                cid = f"a3-{base_short}-{disease}-{i+1:03d}"
                files_by_type["A3"].append(make_request_line(base, dept, disease, "A3", cid))
                total_lines += 1
            for i in range(25):
                cid = f"a4-{base_short}-{disease}-{i+1:03d}"
                files_by_type["A4"].append(make_request_line(base, dept, disease, "A4", cid))
                total_lines += 1

        if case:
            for i in range(50):
                cid = f"case-{base_short}-{disease}-{i+1:03d}"
                files_by_type["病例分析题"].append(make_request_line(base, dept, disease, "病例分析题", cid))
                total_lines += 1

    # 写入文件
    output_dir = "output/batch_jsonl"
    import os
    os.makedirs(output_dir, exist_ok=True)

    for type_key, lines in files_by_type.items():
        if not lines:
            continue
        filename = f"{output_dir}/batch_{type_key}.jsonl"
        with open(filename, "w", encoding="utf-8") as f:
            for line in lines:
                f.write(json.dumps(line, ensure_ascii=False) + "\n")
        print(f"✅ {filename}: {len(lines)} 行")

    print(f"\n总计: {total_lines} 行 ({total_lines} 道题)")


if __name__ == "__main__":
    main()
