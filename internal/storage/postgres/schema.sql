-- AIgo 数据库 Schema

-- 版本化迁移记录（由迁移器在执行版本 1 前创建；此处用于完整描述基线）
CREATE TABLE IF NOT EXISTS schema_migrations (
    version BIGINT PRIMARY KEY,
    name TEXT NOT NULL,
    checksum TEXT NOT NULL,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

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
    CONSTRAINT questions_no_legacy_approved CHECK (status <> 'approved'),
    version INT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- 题目内容版本快照（不可变）。状态和题库归属可变化，但内容变化必须生成新版本。
CREATE TABLE IF NOT EXISTS question_versions (
	 id TEXT PRIMARY KEY,
    question_id TEXT NOT NULL REFERENCES questions(id) ON DELETE CASCADE,
    version INT NOT NULL,
    snapshot JSONB NOT NULL,
    actor TEXT NOT NULL DEFAULT 'system',
    change_type TEXT NOT NULL DEFAULT 'create',
    change_note TEXT NOT NULL DEFAULT '',
	 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	 UNIQUE (question_id, version)
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

-- 题库（分库）
CREATE TABLE IF NOT EXISTS question_banks (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    professions TEXT[] DEFAULT '{}',          -- 专业范围（自动归纳规则）
    created_at TIMESTAMPTZ DEFAULT NOW()
);

-- 题目-题库多对多关系（一道题可属于多个题库）
-- 必须放在 questions 与 question_banks 之后，确保全新数据库可一次完成建表。
CREATE TABLE IF NOT EXISTS question_bank_members (
    question_id TEXT NOT NULL REFERENCES questions(id) ON DELETE CASCADE,
    bank_id TEXT NOT NULL REFERENCES question_banks(id) ON DELETE CASCADE,
    PRIMARY KEY (question_id, bank_id)
);

-- 审核流程配置
CREATE TABLE IF NOT EXISTS review_flows (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    subject TEXT NOT NULL DEFAULT '',
    bank_id TEXT NOT NULL DEFAULT '',             -- 适用题库（空=通用）
    final_reviewer_ids TEXT[] DEFAULT '{}',       -- 最终把关管理员
    vote_rule TEXT NOT NULL DEFAULT '',           -- 投票规则：''=通过票数推进 / veto=一票否决
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
    CONSTRAINT review_tasks_no_legacy_approved CHECK (status <> 'approved'),
    assigned_to TEXT[] DEFAULT '{}',
    final_reviewer_ids TEXT[] DEFAULT '{}',
    final_decision JSONB DEFAULT 'null',
    question_prev_status TEXT NOT NULL DEFAULT '',   -- 提交前题目状态（撤销时恢复用）
    question_version INT NOT NULL DEFAULT 1,         -- 本任务实际审核的题目内容版本
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
    role TEXT NOT NULL DEFAULT 'teacher',    -- 角色模板 ID（空=未分配角色）
    permissions TEXT[] DEFAULT '{}',         -- 直接分配的权限点（与角色权限取并集）
    bank_ids TEXT[] DEFAULT '{}',            -- 题库范围（空=全部题库）
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- 角色模板（管理员可自定义任意数量角色）
CREATE TABLE IF NOT EXISTS roles (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    permissions TEXT[] DEFAULT '{}',
    is_builtin BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- 批量任务记录
CREATE TABLE IF NOT EXISTS batch_jobs (
    id TEXT PRIMARY KEY,              -- provider job_id（API 层使用 backend:id 稳定引用）
    backend TEXT NOT NULL DEFAULT 'dashscope',
    backend_profile TEXT NOT NULL DEFAULT 'dashscope-default',
    model TEXT NOT NULL DEFAULT '',    -- 提交时固化，禁止用当前配置冒充历史模型
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
CREATE INDEX IF NOT EXISTS idx_bank_members_bank ON question_bank_members(bank_id);
CREATE INDEX IF NOT EXISTS idx_bank_members_question ON question_bank_members(question_id);
CREATE INDEX IF NOT EXISTS idx_kp_subject ON knowledge_points(subject);
CREATE INDEX IF NOT EXISTS idx_kp_topic ON knowledge_points USING gin(to_tsvector('simple', topic));
CREATE INDEX IF NOT EXISTS idx_review_tasks_question ON review_tasks(question_id);
CREATE INDEX IF NOT EXISTS idx_question_versions_question ON question_versions(question_id, version DESC);
CREATE INDEX IF NOT EXISTS idx_review_records_task ON review_records(task_id);
CREATE INDEX IF NOT EXISTS idx_audit_question ON audit_logs(question_id);
CREATE INDEX IF NOT EXISTS idx_audit_actor ON audit_logs(actor);
CREATE INDEX IF NOT EXISTS idx_audit_action ON audit_logs(action);
CREATE INDEX IF NOT EXISTS idx_audit_time ON audit_logs(created_at);
CREATE INDEX IF NOT EXISTS idx_image_prompts_question ON image_prompts(question_id);
CREATE INDEX IF NOT EXISTS idx_generated_images_question ON generated_images(question_id);
