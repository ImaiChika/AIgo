#!/usr/bin/env python3
"""
根据 8.19 批量结果中经结构质检后仍缺失的数量，生成 DashScope 批量推理 JSONL 文件。
每个 JSONL 行只请求生成 1 道题或 1 个题组；不重复提交已达到配额的内容。
每种题型使用独立的 system prompt。
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
- 本次请求只生成1道题或1个题组：JSON数组必须且只能包含1个元素；完成该元素后立刻结束，绝不生成或续写第2道题/题组
- 输出必须是可由标准JSON解析器完整解析的JSON；不得使用Markdown代码块、注释或数组外文字
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

【A1型题输出格式】
输出一个JSON数组，且数组只包含一个元素：
{{
  "question": "A1型题干",
  "options": [
    {{"label": "A", "text": "选项文本"}},
    {{"label": "B", "text": "选项文本"}},
    {{"label": "C", "text": "选项文本"}},
    {{"label": "D", "text": "选项文本"}},
    {{"label": "E", "text": "选项文本"}}
  ],
  "answer": "正确答案标签，仅A/B/C/D/E之一",
  "explanation": "完整解析：说明正确答案依据和各干扰项错误原因",
  "difficulty": "0.65",
  "cognitive_level": "记忆/理解/简单应用/综合应用 选一",
  "exam_points": "具体考核要点"
}}

{COMMON_RULES}"""


# ========== A2 System Prompt（完全对齐项目标准） ==========

SYSTEM_A2 = """你是医学考试命题专家，负责生成国家执业医师考试A2型病例型最佳选择题。

【题干】
1. 以“男/女，年龄。”开头，按一般情况→主诉→现病史→既往史→生命征与体格信息→辅助检查信息→最后提问自然组织。
2. 正文禁止出现任何“字段名加冒号”的分段标签；主诉、病史、体格信息、检查信息和最后提问必须自然衔接。
3. 生命征按T、P、R、BP顺序；根据病例已可判断诊断时，不得在提问前直接给出诊断。
4. 最后提问不用“哪个”“什么”，句末不用问号。

【选项和答案】
1. 必须有A-E五个同质、互斥、长度相近的选项，且只有一个最佳答案。
2. 不得使用“以上都是”“以上都不是”；选项text不得重复字母标签。

【说明】
1. explanation以“正确答案为X。”开头，先明确诊断或结论，再写判断依据。
2. 逐项分析其余四个干扰项，并以“故选X。”收尾。

【元数据】
1. difficulty为0.00～1.00之间的两位小数，且是0.05的倍数。
2. cognitive_level只能为记忆、理解、简单应用、综合应用。
3. exam_points只能从以下词表选择，多项用中文逗号连接，提问直接考查的要点写在最前：
临床基本概念、医学基础知识、病因与发病机制、临床表现、辅助检查、诊断与鉴别诊断、治疗原则、具体处置措施、并发症及其诊断治疗、疾病预防与康复、医学人文。
4. exam_points不得填写具体疾病名。

【输出】
严格输出JSON数组。每个元素只能包含clinical_stem、options、answer、explanation、difficulty、cognitive_level、exam_points。不得输出Markdown或其他文字。输出前检查数量、字段、JSON闭合和上述全部规则。"""


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
    {{"stem": "问题1", "options": [{{"label": "A", "text": "选项文本"}}, {{"label": "B", "text": "选项文本"}}, {{"label": "C", "text": "选项文本"}}, {{"label": "D", "text": "选项文本"}}, {{"label": "E", "text": "选项文本"}}], "answer": "A", "explanation": "完整解析"}},
    {{"stem": "问题2", "options": [{{"label": "A", "text": "选项文本"}}, {{"label": "B", "text": "选项文本"}}, {{"label": "C", "text": "选项文本"}}, {{"label": "D", "text": "选项文本"}}, {{"label": "E", "text": "选项文本"}}], "answer": "B", "explanation": "完整解析"}}
  ],
  "difficulty": "0.65",
  "cognitive_level": "综合应用",
  "exam_points": "考核要点"
}}

每组只含2-3个问题，questions中的每个问题必须恰有A-E五个对象选项。

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
    {{"stem": "第一阶段问题", "options": [{{"label": "A", "text": "选项文本"}}, {{"label": "B", "text": "选项文本"}}, {{"label": "C", "text": "选项文本"}}, {{"label": "D", "text": "选项文本"}}, {{"label": "E", "text": "选项文本"}}], "answer": "A", "explanation": "完整解析"}},
    {{"stem": "第二阶段问题", "options": [{{"label": "A", "text": "选项文本"}}, {{"label": "B", "text": "选项文本"}}, {{"label": "C", "text": "选项文本"}}, {{"label": "D", "text": "选项文本"}}, {{"label": "E", "text": "选项文本"}}], "answer": "B", "explanation": "完整解析"}},
    {{"stem": "第三阶段问题", "options": [{{"label": "A", "text": "选项文本"}}, {{"label": "B", "text": "选项文本"}}, {{"label": "C", "text": "选项文本"}}, {{"label": "D", "text": "选项文本"}}, {{"label": "E", "text": "选项文本"}}], "answer": "C", "explanation": "完整解析"}}
  ],
  "difficulty": "0.70",
  "cognitive_level": "综合应用",
  "exam_points": "考核要点"
}}

每组只含3-5个问题，questions中的每个问题必须恰有A-E五个对象选项。

【认知层次】以"综合应用"为主。

{COMMON_RULES}"""


# ========== 案例分析题（病例串型不定项选择题）System Prompt ==========

SYSTEM_CASE = """你是国家执业医师考试案例分析题命题专家。你生成的题型必须是“病例串型不定项选择题”，不是A2、A3、A4单选题，也不是传统五选一病例题。

【题型定义】
1. 每道案例由一个“公共题干”和3～12个递进小问组成。
2. 每个小问有6～12个备选项，标签按A、B、C……顺序排列，最多到L。
3. 每个小问正确答案数量不定，但至少1个；既可以单选，也可以多选。
4. 选项角色包括“关键选项、正确选项、无关选项、错误选项”。关键选项属于正确答案，得分权重更高。
5. 后续小问可以提供新的病史、检查或治疗反应，形成病例串；后续信息不得提前泄露到前面问题。

【计分语义】
- 选中1个正确选项得1分；选中1个关键选项得2分。
- 选中1个错误选项扣1分；选中无关选项不加分也不扣分。
- 每个小问最低得分为0，不得出现负分。
- options中的role用于明确上述计分属性，answers和key_answers必须与role严格一致。

【本次试跑固定规模】
- 每道案例必须恰好5个小问，以兼顾完整性并降低输出截断风险。
- 每个小问必须有6～8个选项。
- 至少2个小问必须有2个或以上正确答案，不能把5个小问全部写成单选题。
- 每道案例至少有1个小问设置“关键选项”。

【医学内容要求】
1. 严格围绕给定的专业基地、科室和病种命题，不得擅自更换病种。
2. 公共题干应为完整、可信、信息充分的临床病例，按一般情况、主诉、现病史、既往史、查体、辅助检查的合理顺序组织。
3. 五个小问应覆盖临床思维链中的不同环节，例如初步检查、诊断与鉴别、进一步检查、治疗、并发症与预后；不得机械重复同一知识点。
4. 正确答案应唯一可判定且无明显学术争议；干扰项必须同质、合理，不得使用“以上都是”“以上都不是”。
5. 数值、单位、药名、检查和操作名称必须规范。不要虚构相互矛盾的病史或检查结果。

【全中文要求】
1. 公共题干、小问、选项和解析必须使用简体中文，不得出现完整英文句子或纯英文题干。
2. CT、MRI、心电图、血氧饱和度等通用医学缩写可以保留；首次出现缩写时优先写中文名称并在括号中注明缩写。
3. 不要将中文内容翻译成英文，不要输出中英双语版本。

【医学安全自检】
1. 题干时间线、病理生理、检查结果和答案必须相互一致；不确定的内容宁可不考，不得编造有争议结论。
2. 若病例考虑吉兰-巴雷综合征，静脉注射免疫球蛋白或血浆置换可作为治疗；糖皮质激素不得列为该病的有效特异性治疗。
3. 心搏骤停期间可按复苏流程使用肾上腺素；恢复自主循环后不得把“肾上腺素1mg静脉推注”写成标准处理。恢复自主循环后应围绕维持灌注、避免低血压、评估病因和器官支持命题。
4. 急性会厌炎病例不得把“悬雍垂偏斜”作为典型体征；该体征更提示扁桃体周围脓肿。发生气道危象时不得以强行压舌检查或延迟气道保护作为正确处理。
5. 黄体破裂病例应符合黄体期或排卵后时间线；不得将排卵期病例与黄体破裂诊断混淆。
6. 所有药物剂量、禁忌证与急救措施必须符合规范。每个小问输出前应逐项复核：正确选项不能包含错误或仅“可能相关”的处理。

【严格JSON输出】
只输出1个JSON对象。JSON对象只能有1个顶层字段`case`，其值为1个案例对象。不得输出JSON数组、Markdown代码块、注释、解释性前言或对象外文字。完成第1个案例后立即结束，绝不续写第2个案例。

JSON结构必须为：
{
  "case": {
    "professional_base": "必须与用户提供的专业基地完全一致",
    "department": "必须与用户提供的科室完全一致",
    "disease": "必须与用户提供的病种完全一致",
    "common_stem": "全中文的完整公共题干",
    "questions": [
      {
        "question_no": 1,
        "new_information": "本小问新增的病例信息；没有则为空字符串",
        "stem": "小问内容",
        "options": [
          {"label": "A", "text": "选项文本", "role": "关键选项/正确选项/无关选项/错误选项四选一"},
          {"label": "B", "text": "选项文本", "role": "关键选项/正确选项/无关选项/错误选项四选一"},
          {"label": "C", "text": "选项文本", "role": "关键选项/正确选项/无关选项/错误选项四选一"},
          {"label": "D", "text": "选项文本", "role": "关键选项/正确选项/无关选项/错误选项四选一"},
          {"label": "E", "text": "选项文本", "role": "关键选项/正确选项/无关选项/错误选项四选一"},
          {"label": "F", "text": "选项文本", "role": "关键选项/正确选项/无关选项/错误选项四选一"}
        ],
        "answers": ["所有关键选项和正确选项的标签，至少1个"],
        "key_answers": ["关键选项标签；没有关键选项时为空数组"],
        "explanation": "全中文解析，逐项说明正确依据和主要干扰项错误原因"
      }
    ],
    "difficulty": "0.00～1.00之间、0.05的倍数",
    "cognitive_level": "综合应用",
    "exam_point": "必须与用户提供的病种完全一致"
  }
}

【字段一致性校验】
1. answers必须等于role为“关键选项”或“正确选项”的全部标签，不得多、不得少。
2. key_answers必须等于role为“关键选项”的全部标签，并且必须是answers的子集。
3. question_no必须依次为1、2、3、4、5。
4. 每问选项标签必须连续、唯一；6个选项为A-F，8个选项为A-H。
5. professional_base、department、disease、exam_point必须与用户输入逐字一致。
6. 案例对象的顶层字段只能且必须是professional_base、department、disease、common_stem、questions、difficulty、cognitive_level、exam_point；绝对禁止输出common_st、common_stm、clinical_stem等别名或额外字段。
7. 每个小问的字段只能且必须是question_no、new_information、stem、options、answers、key_answers、explanation；new_information必须存在，没有新增信息时填空字符串。
8. 输出前自行检查JSON闭合、字符串转义、字段名拼写及对象完整性。"""


# ========== User Prompt 模板 ==========

def build_user_prompt(base, dept, disease, qtype_desc):
    return f"""请根据以下信息，生成1{qtype_desc}。

【专业基地】{base}
【科室】{dept}
【病种/知识点】{disease}

只输出JSON数组，不要输出其他任何内容。"""


def build_user_prompt_case(base, dept, disease):
    return f"""请生成1道国家执业医师考试案例分析题（病例串型不定项选择题）。

【专业基地】{base}
【科室】{dept}
【病种】{disease}

本题必须严格围绕上述病种，生成1个公共题干和恰好5个递进小问。每问6～8个选项，答案可以单选或多选；至少2问为多选，至少1问含关键选项。所有内容必须为简体中文，通用医学缩写除外。

professional_base必须输出“{base}”；department必须输出“{dept}”；disease和exam_point必须输出“{disease}”。

只输出顶层字段为case的完整JSON对象，不要输出任何其他文字。"""


# ========== A2 User Prompt（对齐标准格式） ==========

def build_user_prompt_a2(base, dept, disease):
    return f"""请根据以下信息，生成1道国家执业医师考试A2型单选题。

【专业基地】{base}
【科室】{dept}
【病种/知识点】{disease}

【输出格式】
输出一个JSON数组，包含一个元素：
{{
  "clinical_stem": "以男/女、年龄开头的临床情境题干；不用格式引导词；最后提问不用哪个/什么和问号",
  "options": [
    {{"label": "A", "text": "选项文本"}},
    {{"label": "B", "text": "选项文本"}},
    {{"label": "C", "text": "选项文本"}},
    {{"label": "D", "text": "选项文本"}},
    {{"label": "E", "text": "选项文本"}}
  ],
  "answer": "唯一正确答案标签",
  "explanation": "正确答案为X。明确诊断或结论和判断依据，逐项分析其余四个选项。故选X。",
  "difficulty": "0.65",
  "cognitive_level": "记忆/理解/简单应用/综合应用 选一",
  "exam_points": "从受控词表选择，如：诊断与鉴别诊断，临床表现，辅助检查"
}}

只输出JSON数组，不要输出其他任何内容。"""


# ========== 题型配置 ==========
TYPE_CONFIG = {
    "A1": {
        "system": SYSTEM_A1,
        "user_fn": lambda base, dept, disease: build_user_prompt(base, dept, disease, "道国家执业医师考试A1型单选题"),
        "temperature": 0.5,
    },
    "A2": {
        "system": SYSTEM_A2,
        "user_fn": lambda base, dept, disease: build_user_prompt_a2(base, dept, disease),
        "temperature": 0.4,
    },
    "A3": {
        "system": SYSTEM_A3,
        "user_fn": lambda base, dept, disease: build_user_prompt(base, dept, disease, "组国家执业医师考试A3型题（病例组型题，2-3个问题）"),
        "temperature": 0.5,
    },
    "A4": {
        "system": SYSTEM_A4,
        "user_fn": lambda base, dept, disease: build_user_prompt(base, dept, disease, "组国家执业医师考试A4型题（病例串型题，3-5个问题，病例按时间线发展）"),
        "temperature": 0.5,
    },
    "病例分析题": {
        "system": SYSTEM_CASE,
        "user_fn": lambda base, dept, disease: build_user_prompt_case(base, dept, disease),
        "temperature": 0.3,
    },
}


# 以 820 回传后的严格质检结果为准：每项需求均须达到 50 题组。
# A1、A2 已完成；这里只保留未达到 50 的下一轮缺口。
REMAINING_COUNTS = {
    ("A3", "全科医学科", "急性气道梗阻"): 7,
    ("A3", "眼科", "创伤包扎固定及搬运技能"): 1,
    ("A3", "耳鼻咽喉科", "耳前瘘管继发感染"): 2,
    ("A3", "耳鼻咽喉科", "鼻鼻窦良恶性肿瘤"): 7,
    ("A3", "耳鼻咽喉科", "喉癌前病变"): 3,
    ("A3", "急诊科", "局部浸润麻醉的管理"): 8,
    ("A3", "骨科", "臂丛神经阻滞"): 8,
    ("A3", "骨科", "局部浸润麻醉的管理"): 8,
    ("A3", "麻醉科", "疼痛门诊和(或)病房"): 2,
    ("A4", "急诊科基地", "肿瘤急症"): 6,
    ("A4", "全科医学科", "急性气道梗阻"): 13,
    ("A4", "妇产科", "黄体破裂"): 4,
    ("A4", "妇产科", "胎动异常"): 10,
    ("A4", "眼科", "心电图检查及认读"): 2,
    ("A4", "眼科", "创伤包扎固定及搬运技能"): 4,
    ("A4", "耳鼻咽喉科", "耳前瘘管继发感染"): 7,
    ("A4", "耳鼻咽喉科", "鼻鼻窦良恶性肿瘤"): 18,
    ("A4", "耳鼻咽喉科", "喉癌前病变"): 17,
    ("A4", "耳鼻咽喉科", "喉良性增生性病变"): 12,
    ("A4", "放射肿瘤科", "急性发热"): 2,
    ("A4", "急诊科", "局部浸润麻醉的管理"): 8,
    ("A4", "骨科", "臂丛神经阻滞"): 8,
    ("A4", "骨科", "局部浸润麻醉的管理"): 9,
    ("A4", "骨科", "椎管内麻醉的管理"): 7,
    ("A4", "麻醉科", "疼痛门诊和(或)病房"): 3,
    ("病例分析题", "神经内科", "呼吸衰竭"): 2,
    ("病例分析题", "神经内科", "重症感染"): 5,
    ("病例分析题", "妇产科", "黄体破裂"): 3,
    ("病例分析题", "妇产科", "各种手术/产后并发症"): 4,
    ("病例分析题", "眼科", "心肺复苏"): 12,
    ("病例分析题", "耳鼻咽喉科", "急性会厌炎"): 4,
    ("病例分析题", "耳鼻咽喉科", "急慢性扁桃体炎"): 4,
    ("病例分析题", "重症医学科", "急性胆囊炎"): 2,
    ("病例分析题", "急诊科", "局部浸润麻醉的管理"): 3,
    ("病例分析题", "骨科", "臂丛神经阻滞"): 5,
    ("病例分析题", "骨科", "局部浸润麻醉的管理"): 4,
    ("病例分析题", "骨科", "椎管内麻醉的管理"): 4,
    ("病例分析题", "麻醉科", "小儿麻醉"): 7,
    ("病例分析题", "麻醉科", "疼痛门诊和(或)病房"): 7,
    ("病例分析题", "麻醉科", "眼科与耳鼻咽喉科麻醉"): 4,
    ("病例分析题", "麻醉科", "急性疼痛治疗"): 4,
    ("病例分析题", "麻醉科", "麻醉恢复室(PACU)"): 4,
    ("病例分析题", "麻醉科", "妇产科麻醉（含产科麻醉40例）"): 6,
    ("病例分析题", "麻醉科", "重症监护[含麻醉重症监护病房(AICU)收治病例]"): 4,
    ("病例分析题", "麻醉科", "院内急救"): 8,
}


def make_request_line(base, dept, disease, qtype, custom_id):
    cfg = TYPE_CONFIG[qtype]
    user_prompt = cfg["user_fn"](base, dept, disease)
    body = {
        "model": "qwen3.5-flash",
        "messages": [
            {"role": "system", "content": cfg["system"]},
            {"role": "user", "content": user_prompt},
        ],
        "temperature": cfg["temperature"],
        # 不设置 max_tokens，避免思考 Token 和完整 JSON 输出共享过小额度导致截断。
        # qwen3.5 系列默认支持思考模式；显式开启，保证批量文件内模式一致。
        "enable_thinking": True,
    }
    if qtype == "病例分析题":
        # Qwen 的 JSON Object 模式保证返回合法 JSON；案例对象字段仍由 prompt 严格约束。
        body["response_format"] = {"type": "json_object"}

    return {
        "custom_id": custom_id,
        "method": "POST",
        "url": "/v1/chat/completions",
        "body": body,
    }


def main():
    wb = openpyxl.load_workbook("data/requirements/急诊与麻醉科缺失.xlsx")
    ws = wb["Sheet1"]
    rows = list(ws.iter_rows(values_only=True))
    data_rows = rows[1:]

    files_by_type = {"A1": [], "A2": [], "A3": [], "A4": [], "病例分析题": []}
    total_lines = 0

    for row in data_rows:
        base, dept, disease, a1, a2, a34, case = row

        # 用专业基地前缀防止不同科室同病种的 custom_id 冲突
        base_short = base.replace("基地", "").replace("科", "")

        # 每个 JSONL 行只生成 1 道题/组；只写入严格质检后仍缺失的数量。
        a1_remaining = REMAINING_COUNTS.get(("A1", base, disease), 0)
        if a1_remaining:
            for i in range(a1_remaining):
                cid = f"a1-{base_short}-{disease}-{i+1:03d}"
                files_by_type["A1"].append(make_request_line(base, dept, disease, "A1", cid))
                total_lines += 1

        a2_remaining = REMAINING_COUNTS.get(("A2", base, disease), 0)
        if a2_remaining:
            for i in range(a2_remaining):
                cid = f"a2-{base_short}-{disease}-{i+1:03d}"
                files_by_type["A2"].append(make_request_line(base, dept, disease, "A2", cid))
                total_lines += 1

        a3_remaining = REMAINING_COUNTS.get(("A3", base, disease), 0)
        if a3_remaining:
            for i in range(a3_remaining):
                cid = f"a3-{base_short}-{disease}-{i+1:03d}"
                files_by_type["A3"].append(make_request_line(base, dept, disease, "A3", cid))
                total_lines += 1
        a4_remaining = REMAINING_COUNTS.get(("A4", base, disease), 0)
        if a4_remaining:
            for i in range(a4_remaining):
                cid = f"a4-{base_short}-{disease}-{i+1:03d}"
                files_by_type["A4"].append(make_request_line(base, dept, disease, "A4", cid))
                total_lines += 1

        case_remaining = REMAINING_COUNTS.get(("病例分析题", base, disease), 0)
        if case_remaining:
            for i in range(case_remaining):
                cid = f"case-{base_short}-{disease}-{i+1:03d}"
                files_by_type["病例分析题"].append(make_request_line(base, dept, disease, "病例分析题", cid))
                total_lines += 1

    # 写入文件
    output_dir = "output/batch_jsonl"
    import os
    os.makedirs(output_dir, exist_ok=True)

    for type_key, lines in files_by_type.items():
        filename = f"{output_dir}/batch_{type_key}.jsonl"
        with open(filename, "w", encoding="utf-8") as f:
            for line in lines:
                f.write(json.dumps(line, ensure_ascii=False) + "\n")
        print(f"✅ {filename}: {len(lines)} 行")

    print(f"\n总计: {total_lines} 行 ({total_lines} 道题)")


if __name__ == "__main__":
    main()
