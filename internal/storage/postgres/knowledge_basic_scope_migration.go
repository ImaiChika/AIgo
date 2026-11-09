package postgres

// Schema56 把基础医学知识点从全部未删除大纲版本的有效范围剔除：行级墓碑保留
// 历史（题目快照、审核与导出依据不受影响）；已启用知识卡中指向被剔除点的映射
// 同步收缩——仅剩被剔除映射的卡转 archived，混合卡移除失效映射并递增修订，
// 旧版本正文全部保留。迁移幂等：重复执行不再命中任何行。
func removeBasicMedicalScopeStatements() []string {
	// Freeze the same whitespace set as Go strings.TrimSpace. Plain SQL btrim
	// only removes ASCII spaces and would retain legacy tab/full-width rows.
	const trimChars = `U&'\0009\000A\000B\000C\000D\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000'`
	const basicPredicate = `btrim(category, ` + trimChars + `) LIKE '基础医学%'
		OR btrim(subject, ` + trimChars + `) = ANY(ARRAY['生理','生理学','生物化学','生化','生物化学与分子生物学','病理','病理学','病生','病理生理学','药理','药理学','免疫','医学免疫学','微生物','医学微生物学','解剖','人体解剖学','组织学与胚胎学'])`
	return []string{
		`UPDATE knowledge_points p SET deleted_at=NOW(), updated_at=NOW(), revision=revision+1
		WHERE p.deleted_at IS NULL
		  AND EXISTS (SELECT 1 FROM knowledge_versions v WHERE v.id=p.version_id AND v.deleted_at IS NULL)
		  AND (` + basicPredicate + `)`,
		`DO $$ DECLARE
			basic_ids TEXT[];
			card RECORD;
			kept JSONB;
			new_status TEXT;
			new_revision INT;
			new_body JSONB;
		BEGIN
			SELECT COALESCE(array_agg(id), '{}'::text[]) INTO basic_ids
			FROM knowledge_points
			WHERE deleted_at IS NOT NULL AND (` + basicPredicate + `);
			FOR card IN
				SELECT id, revision, body FROM knowledge_cards
				WHERE status='active' AND jsonb_typeof(body->'knowledge_points')='array'
				  AND EXISTS (
					SELECT 1 FROM jsonb_array_elements(body->'knowledge_points') m
					WHERE m->>'id' = ANY(basic_ids)
				  )
			LOOP
				SELECT COALESCE(jsonb_agg(m ORDER BY ord), '[]'::jsonb) INTO kept
				FROM jsonb_array_elements(card.body->'knowledge_points') WITH ORDINALITY AS t(m, ord)
				WHERE NOT (m->>'id' = ANY(basic_ids));
				new_revision := card.revision + 1;
				IF kept = '[]'::jsonb THEN
					new_status := 'archived';
				ELSE
					new_status := card.body->>'status';
				END IF;
				new_body := jsonb_set(jsonb_set(jsonb_set(jsonb_set(card.body, '{knowledge_points}', kept),
					'{revision}', to_jsonb(new_revision)), '{status}', to_jsonb(new_status)), '{updated_at}', to_jsonb(NOW()));
				INSERT INTO knowledge_card_versions (card_id, revision, body, created_at)
				VALUES (card.id, card.revision, card.body, NOW()) ON CONFLICT (card_id,revision) DO NOTHING;
				UPDATE knowledge_cards SET revision=new_revision, status=new_status, body=new_body, updated_at=NOW()
				WHERE id=card.id;
				INSERT INTO knowledge_card_versions (card_id, revision, body, created_at)
				VALUES (card.id, new_revision, new_body, NOW());
			END LOOP;
		END $$`,
	}
}
