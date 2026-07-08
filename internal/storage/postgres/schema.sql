-- AIgo 数据库 Schema

-- 知识点
CREATE TABLE IF NOT EXISTS knowledge_points (
    id TEXT PRIMARY KEY,
    subject TEXT NOT NULL DEFAULT '',
    system TEXT NOT NULL DEFAULT '',
    topic TEXT NOT NULL DEFAULT '',
    outline_ref TEXT NOT NULL DEFAULT '',
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
    question_id TEXT NOT NULL REFERENCES questions(id),
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
    task_id TEXT NOT NULL REFERENCES review_tasks(id),
    question_id TEXT NOT NULL REFERENCES questions(id),
    round_number INT NOT NULL,
    expert_id TEXT NOT NULL,
    conclusion TEXT NOT NULL,
    opinion TEXT NOT NULL DEFAULT '',
    before_snapshot TEXT NOT NULL DEFAULT '',
    after_snapshot TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ DEFAULT NOW()
);

-- 题目版本
CREATE TABLE IF NOT EXISTS question_versions (
    id TEXT PRIMARY KEY,
    question_id TEXT NOT NULL REFERENCES questions(id),
    version INT NOT NULL,
    snapshot TEXT NOT NULL,
    change_note TEXT NOT NULL DEFAULT '',
    changed_by TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ DEFAULT NOW()
);

-- 图片提示词
CREATE TABLE IF NOT EXISTS image_prompts (
    id TEXT PRIMARY KEY,
    question_id TEXT NOT NULL REFERENCES questions(id),
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
    prompt_id TEXT NOT NULL REFERENCES image_prompts(id),
    question_id TEXT NOT NULL REFERENCES questions(id),
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
    image_id TEXT NOT NULL REFERENCES generated_images(id),
    expert_id TEXT NOT NULL,
    conclusion TEXT NOT NULL,
    opinion TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ DEFAULT NOW()
);

-- 索引
CREATE INDEX IF NOT EXISTS idx_questions_status ON questions(status);
CREATE INDEX IF NOT EXISTS idx_questions_difficulty ON questions(difficulty);
CREATE INDEX IF NOT EXISTS idx_kp_system ON knowledge_points(system);
CREATE INDEX IF NOT EXISTS idx_kp_topic ON knowledge_points USING gin(to_tsvector('simple', topic));
CREATE INDEX IF NOT EXISTS idx_review_tasks_question ON review_tasks(question_id);
CREATE INDEX IF NOT EXISTS idx_review_records_task ON review_records(task_id);
CREATE INDEX IF NOT EXISTS idx_image_prompts_question ON image_prompts(question_id);
CREATE INDEX IF NOT EXISTS idx_generated_images_question ON generated_images(question_id);
