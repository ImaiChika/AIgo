package postgres

// Historical Schema37 SQL is immutable; active preference APIs were retired in Schema62.
func generationRuleMigrationStatements() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS generation_rules (
        id TEXT PRIMARY KEY, owner_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
        name TEXT NOT NULL, content TEXT NOT NULL, kind TEXT NOT NULL CHECK(kind IN ('constraint','suggestion')),
        enabled BOOLEAN NOT NULL DEFAULT TRUE, sort_order INT NOT NULL DEFAULT 0,
        revision INT NOT NULL DEFAULT 1 CHECK(revision>0), created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
        updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), deleted_at TIMESTAMPTZ
    )`,
		`CREATE INDEX IF NOT EXISTS idx_generation_rules_owner ON generation_rules(owner_id,sort_order,id) WHERE deleted_at IS NULL`,
		`ALTER TABLE batch_jobs ADD COLUMN IF NOT EXISTS rules_snapshot JSONB`,
		// 仅迁移时赋予默认值；以后启动不得补回被刻意撤销的权限。
		`UPDATE roles SET permissions=ARRAY(SELECT DISTINCT p FROM unnest(permissions || ARRAY['rule:view','rule:manage','rule:use','rule:view_all']) p) WHERE id IN ('super_admin','admin')`,
		`UPDATE roles SET permissions=ARRAY(SELECT DISTINCT p FROM unnest(permissions || ARRAY['rule:view','rule:manage','rule:use']) p) WHERE id='teacher'`,
	}
}

func singleEnabledRuleMigrationStatements() []string {
	return []string{
		`WITH ranked AS (
    SELECT id,ROW_NUMBER() OVER(PARTITION BY owner_id ORDER BY updated_at DESC,sort_order,id) AS position
    FROM generation_rules WHERE enabled AND deleted_at IS NULL
  ) UPDATE generation_rules r SET enabled=FALSE,revision=revision+1,updated_at=NOW()
    FROM ranked WHERE r.id=ranked.id AND ranked.position>1`,
		`CREATE UNIQUE INDEX IF NOT EXISTS generation_rules_one_enabled_per_owner ON generation_rules(owner_id) WHERE enabled AND deleted_at IS NULL`,
	}
}
