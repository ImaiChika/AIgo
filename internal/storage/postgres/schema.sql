-- AIgo 数据库 Schema

-- 知识点（2024考试大纲）
CREATE TABLE IF NOT EXISTS knowledge_points (
    id TEXT PRIMARY KEY,           -- 大纲代码，如 110.2.6.2.1.1
    category TEXT NOT NULL DEFAULT '',  -- 分类：基础医学/临床综合
    subject TEXT NOT NULL DEFAULT '',   -- 专业/系统，如"病理"、"呼吸系统"
    unit TEXT NOT NULL DEFAULT '',      -- 单元，如"二、局部血液循环障碍"
    sub_item TEXT NOT NULL DEFAULT '',  -- 细目，如"1．充血和淤血"
    topic TEXT NOT NULL DEFAULT '',     -- 要点，如"（1）充血的概念和类型"
    outline_code TEXT NOT NULL DEFAULT '', -- 大纲代码（同 id）
    outline_ref TEXT NOT NULL DEFAULT '',  -- 兼容旧字段
    keywords TEXT[] DEFAULT '{}',
    created_at TIMESTAMPTZ DEFAULT NOW()
);

-- 题目
CREATE TABLE IF NOT EXISTS questions (
    id TEXT PRIMARY KEY,
    clinical_stem TEXT NOT NULL,
    options JSONB NOT NULL DEFAULT '[]',
    answer TEXT NOT NULL DEFAULT '',
    explanation TEXT NOT NULL DEFAULT '',
    source_refs JSONB DEFAULT '[]',
    knowledge_points JSONB DEFAULT '[]',
    media_refs JSONB DEFAULT '[]',
    difficulty TEXT NOT NULL DEFAULT 'medium',
    cognitive_level TEXT NOT NULL DEFAULT '',   -- 认知层次：记忆/理解/简单应用/综合应用
    exam_points TEXT NOT NULL DEFAULT '',        -- 考核要点
    outline_code TEXT NOT NULL DEFAULT '',       -- 大纲代码
    profession TEXT NOT NULL DEFAULT '',         -- 专业
    system_name TEXT NOT NULL DEFAULT '',        -- 系统
    status TEXT NOT NULL DEFAULT 'ai_draft',
    version INT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- 专家
CREATE TABLE IF NOT EXISTS experts (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    department TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL DEFAULT '',
    specialties TEXT[] DEFAULT '{}',
    expert_types TEXT[] DEFAULT '{}',
    contact TEXT NOT NULL DEFAULT '',
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

-- 审核流程配置
CREATE TABLE IF NOT EXISTS review_flows (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    subject TEXT NOT NULL DEFAULT '',
    rounds JSONB NOT NULL DEFAULT '[]',
    created_at TIMESTAMPTZ DEFAULT NOW()
);

-- 审核任务
CREATE TABLE IF NOT EXISTS review_tasks (
    id TEXT PRIMARY KEY,
    question_id TEXT NOT NULL REFERENCES questions(id) ON DELETE CASCADE,
    flow_id TEXT NOT NULL REFERENCES review_flows(id),
    current_round INT NOT NULL DEFAULT 1,
    status TEXT NOT NULL DEFAULT 'reviewing',
    assigned_to TEXT[] DEFAULT '{}',
    round_results JSONB DEFAULT '[]',
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- 审核记录
CREATE TABLE IF NOT EXISTS review_records (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES review_tasks(id) ON DELETE CASCADE,
    question_id TEXT NOT NULL REFERENCES questions(id) ON DELETE CASCADE,
    round_number INT NOT NULL,
    expert_id TEXT NOT NULL,
    conclusion TEXT NOT NULL,
    opinion TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ DEFAULT NOW()
);

-- 操作日志（审计）
CREATE TABLE IF NOT EXISTS audit_logs (
    id TEXT PRIMARY KEY,
    question_id TEXT NOT NULL DEFAULT '',
    action TEXT NOT NULL,
    actor TEXT NOT NULL,
    detail TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ DEFAULT NOW()
);

-- 图片提示词
CREATE TABLE IF NOT EXISTS image_prompts (
    id TEXT PRIMARY KEY,
    question_id TEXT NOT NULL REFERENCES questions(id) ON DELETE CASCADE,
    purpose TEXT NOT NULL DEFAULT '',
    image_type TEXT NOT NULL DEFAULT '',
    subject TEXT NOT NULL DEFAULT '',
    must_include TEXT[] DEFAULT '{}',
    must_exclude TEXT[] DEFAULT '{}',
    style TEXT NOT NULL DEFAULT '',
    knowledge_point TEXT NOT NULL DEFAULT '',
    review_focus TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ DEFAULT NOW()
);

-- 候选图
CREATE TABLE IF NOT EXISTS generated_images (
    id TEXT PRIMARY KEY,
    prompt_id TEXT NOT NULL REFERENCES image_prompts(id) ON DELETE CASCADE,
    question_id TEXT NOT NULL REFERENCES questions(id) ON DELETE CASCADE,
    image_path TEXT NOT NULL DEFAULT '',
    model_name TEXT NOT NULL DEFAULT '',
    model_version TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'pending',
    review_note TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ DEFAULT NOW()
);

-- 图片审核记录
CREATE TABLE IF NOT EXISTS image_review_records (
    id TEXT PRIMARY KEY,
    image_id TEXT NOT NULL REFERENCES generated_images(id) ON DELETE CASCADE,
    expert_id TEXT NOT NULL,
    conclusion TEXT NOT NULL,
    opinion TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ DEFAULT NOW()
);

-- 用户
CREATE TABLE IF NOT EXISTS users (
    id TEXT PRIMARY KEY,
    username TEXT UNIQUE NOT NULL,
    password_hash TEXT NOT NULL,
    display_name TEXT NOT NULL DEFAULT '',
    role TEXT NOT NULL DEFAULT 'teacher',
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- 批量任务记录
CREATE TABLE IF NOT EXISTS batch_jobs (
    id TEXT PRIMARY KEY,              -- DashScope job_id
    job_name TEXT NOT NULL DEFAULT '', -- 自定义任务名称
    status TEXT NOT NULL DEFAULT 'pending', -- 状态
    total_count INT NOT NULL DEFAULT 0,
    completed INT NOT NULL DEFAULT 0,
    failed INT NOT NULL DEFAULT 0,
    output_file_id TEXT NOT NULL DEFAULT '',
    points_json TEXT NOT NULL DEFAULT '[]', -- 关联的知识点列表JSON
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- AI 检查结果
CREATE TABLE IF NOT EXISTS ai_review_results (
    id TEXT PRIMARY KEY,
    question_id TEXT NOT NULL REFERENCES questions(id) ON DELETE CASCADE,
    question_version INT NOT NULL DEFAULT 0,     -- 检查时的题目版本号，用于判断结果是否过期
    verdict TEXT NOT NULL DEFAULT 'pass',       -- pass / issues_found / reject
    scores JSONB NOT NULL DEFAULT '{}',         -- 各维度分数
    issues JSONB NOT NULL DEFAULT '[]',         -- 问题列表
    suggestion TEXT NOT NULL DEFAULT '',        -- AI 修改建议
    model TEXT NOT NULL DEFAULT '',             -- 使用的模型
    raw_response TEXT NOT NULL DEFAULT '',      -- 原始 LLM 响应
    created_at TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_ai_review_question ON ai_review_results(question_id);

-- 索引
CREATE INDEX IF NOT EXISTS idx_users_username ON users(username);
CREATE INDEX IF NOT EXISTS idx_users_role ON users(role);
CREATE INDEX IF NOT EXISTS idx_questions_status ON questions(status);
CREATE INDEX IF NOT EXISTS idx_questions_difficulty ON questions(difficulty);
CREATE INDEX IF NOT EXISTS idx_kp_subject ON knowledge_points(subject);
CREATE INDEX IF NOT EXISTS idx_kp_topic ON knowledge_points USING gin(to_tsvector('simple', topic));
CREATE INDEX IF NOT EXISTS idx_review_tasks_question ON review_tasks(question_id);
CREATE INDEX IF NOT EXISTS idx_review_records_task ON review_records(task_id);
CREATE INDEX IF NOT EXISTS idx_audit_question ON audit_logs(question_id);
CREATE INDEX IF NOT EXISTS idx_audit_actor ON audit_logs(actor);
CREATE INDEX IF NOT EXISTS idx_audit_action ON audit_logs(action);
CREATE INDEX IF NOT EXISTS idx_audit_time ON audit_logs(created_at);
CREATE INDEX IF NOT EXISTS idx_image_prompts_question ON image_prompts(question_id);
CREATE INDEX IF NOT EXISTS idx_generated_images_question ON generated_images(question_id);
