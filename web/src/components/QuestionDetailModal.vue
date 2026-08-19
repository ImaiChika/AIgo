<script setup>
import { ref, watch } from "vue";
import { api } from "../api.js";
import { getToken } from "../auth.js";

const props = defineProps({
  question: { type: Object, default: null },
  bankName: { type: Function, default: null },
});
const emit = defineEmits(["close"]);

const images = ref([]);

watch(
  () => props.question,
  async (q) => {
    images.value = [];
    if (q && q.id) {
      try {
        const data = await api.listImages(q.id);
        images.value = data.images || [];
      } catch (e) {
        images.value = [];
      }
    }
  },
  { immediate: true }
);

const statusText = (status) => {
  const map = {
    ai_draft: "AI草稿", auto_checked: "已初评", ai_reviewed: "AI已检查",
    reviewing: "审核中", conflict: "待决断", revision_required: "需修改",
    rejected: "已驳回", approved: "已通过", published: "已入库", archived: "已归档",
  };
  return map[status] || status;
};

const statusClass = (status) => {
  if (status === "approved" || status === "published" || status === "ai_reviewed") return "q-status-good";
  if (status === "rejected") return "q-status-bad";
  if (status === "reviewing") return "q-status-active";
  if (status === "revision_required") return "q-status-warn";
  if (status === "conflict") return "q-status-conflict";
  return "";
};

function imageSrc(path) {
  if (!path) return "";
  const filename = path.split("/").pop();
  const token = getToken();
  return `/images/${filename}${token ? `?token=${encodeURIComponent(token)}` : ""}`;
}

function bankLabel() {
  if (!props.question) return "";
  const ids = props.question.bank_ids || [];
  if (!ids.length) return "未分类";
  const names = ids.map((id) => (props.bankName ? props.bankName(id) : id));
  return names.join("、");
}

function difficultyText(d) {
  const map = { easy: "简单", medium: "中等", hard: "困难" };
  if (map[d]) return map[d];
  if (d && !isNaN(Number(d))) {
    const v = Number(d);
    if (v <= 0.6) return "简单";
    if (v <= 0.8) return "中等";
    return "困难";
  }
  return d || "-";
}
</script>

<template>
  <div v-if="question" class="q-modal-overlay" @click.self="emit('close')">
    <div class="q-modal-card">
      <div class="q-modal-head">
        <h3>题目详情</h3>
        <button class="q-close-btn" type="button" @click="emit('close')">×</button>
      </div>

      <div class="q-params">
        <span v-if="question.outline_code" class="q-param"><label>大纲代码</label>{{ question.outline_code }}</span>
        <span v-if="question.profession" class="q-param"><label>专业</label>{{ question.profession }}</span>
        <span v-if="question.system" class="q-param"><label>系统</label>{{ question.system }}</span>
        <span v-if="question.difficulty" class="q-param"><label>难度</label>{{ difficultyText(question.difficulty) }}</span>
        <span v-if="question.cognitive_level" class="q-param"><label>认知层次</label>{{ question.cognitive_level }}</span>
        <span class="q-param"><label>题库</label>{{ bankLabel() }}</span>
        <span class="q-param"><label>状态</label><b :class="statusClass(question.status)">{{ statusText(question.status) }}</b></span>
        <span class="q-param"><label>版本</label>v{{ question.version }}</span>
      </div>

      <div v-if="question.exam_points" class="q-block">
        <label>考核要点</label>
        <p>{{ question.exam_points }}</p>
      </div>

      <div class="q-block">
        <label>题干</label>
        <p class="q-stem">{{ question.clinical_stem }}</p>
      </div>

      <div class="q-block">
        <label>选项</label>
        <div v-for="opt in question.options || []" :key="opt.label" class="q-option" :class="{ correct: opt.label === question.answer }">
          <span class="q-opt-label">{{ opt.label }}</span>
          <span>{{ opt.text }}</span>
          <span v-if="opt.label === question.answer" class="q-correct-mark">✓ 正确答案</span>
        </div>
      </div>

      <div class="q-block">
        <label>解析</label>
        <p v-if="question.explanation">{{ question.explanation }}</p>
        <p v-else class="q-none">（无解析）</p>
      </div>

      <div v-if="(question.knowledge_points || []).length" class="q-block">
        <label>知识点</label>
        <div class="q-kps">
          <span v-for="kp in question.knowledge_points" :key="kp.id" class="q-kp">
            {{ kp.topic }}<span v-if="kp.outline_code" class="q-kp-code">（{{ kp.outline_code }}）</span>
          </span>
        </div>
      </div>

      <div v-if="(question.source_refs || []).length" class="q-block">
        <label>来源引用</label>
        <div v-for="s in question.source_refs" :key="s.title" class="q-source">
          {{ s.title }}<span v-if="s.note"> - {{ s.note }}</span>
        </div>
      </div>

      <div v-if="images.length" class="q-block">
        <label>配图（{{ images.length }} 张）</label>
        <div class="q-images">
          <div v-for="img in images" :key="img.id" class="q-img-item">
            <img :src="imageSrc(img.image_path)" :alt="img.id" />
            <span class="q-img-status" :class="img.status">
              {{ img.status === "approved" ? "已通过" : img.status === "rejected" ? "已驳回" : "待审核" }}
            </span>
          </div>
        </div>
      </div>

      <div class="q-block q-meta">
        <label>元信息</label>
        <p>ID：{{ question.id }}</p>
        <p>创建：{{ question.created_at ? new Date(question.created_at).toLocaleString() : "-" }}</p>
        <p>更新：{{ question.updated_at ? new Date(question.updated_at).toLocaleString() : "-" }}</p>
      </div>
    </div>
  </div>
</template>

<style scoped>
.q-modal-overlay {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.45);
  display: grid;
  place-items: center;
  z-index: 300;
}

.q-modal-card {
  width: min(760px, 92vw);
  max-height: 88vh;
  overflow-y: auto;
  background: #fff;
  border-radius: 12px;
  padding: 22px 26px;
  box-shadow: 0 24px 60px rgba(0, 0, 0, 0.25);
}

.q-modal-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 12px;
}

.q-modal-head h3 {
  margin: 0;
  font-size: 17px;
  color: #172033;
}

.q-close-btn {
  width: 30px;
  height: 30px;
  border: 1px solid #e5ebf3;
  border-radius: 6px;
  background: #fff;
  color: #6e7b8f;
  font-size: 18px;
  cursor: pointer;
}

.q-close-btn:hover {
  background: #fff0f0;
  color: #c54858;
}

.q-params {
  display: flex;
  flex-wrap: wrap;
  gap: 8px 20px;
  padding: 10px 12px;
  background: #f8fbff;
  border: 1px solid #dce8f7;
  border-radius: 8px;
  margin-bottom: 14px;
}

.q-param {
  font-size: 12px;
  color: #3a4658;
}

.q-param label {
  color: #6e7b8f;
  margin-right: 6px;
}

.q-status-good { color: #087c55; }
.q-status-bad { color: #c54858; }
.q-status-active { color: #0571dc; }
.q-status-warn { color: #c07b22; }
.q-status-conflict { color: #b93a7c; }

.q-block {
  margin-bottom: 14px;
}

.q-block label {
  display: block;
  font-size: 12px;
  font-weight: 600;
  color: #6e7b8f;
  margin-bottom: 6px;
}

.q-stem {
  font-size: 14px;
  line-height: 1.7;
  color: #172033;
  margin: 0;
}

.q-option {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 0;
  font-size: 13px;
  color: #3a4658;
}

.q-option.correct {
  color: #087c55;
  font-weight: 600;
}

.q-opt-label {
  width: 24px;
  height: 24px;
  display: grid;
  place-items: center;
  border-radius: 50%;
  background: #e8f0f8;
  color: #1385f8;
  font-size: 12px;
  font-weight: 700;
}

.q-correct-mark {
  font-size: 12px;
  color: #087c55;
}

.q-none {
  color: #9aa5b4;
  font-size: 13px;
  margin: 0;
}

.q-kps {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.q-kp {
  font-size: 12px;
  padding: 3px 10px;
  border-radius: 4px;
  background: #eff8ff;
  color: #0571dc;
}

.q-kp-code {
  color: #6e7b8f;
  font-size: 11px;
}

.q-source {
  font-size: 12px;
  color: #3a4658;
  padding: 2px 0;
}

.q-images {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
}

.q-img-item {
  display: flex;
  flex-direction: column;
  gap: 4px;
  align-items: center;
}

.q-img-item img {
  width: 160px;
  height: 120px;
  object-fit: cover;
  border: 1px solid #e5ebf3;
  border-radius: 6px;
}

.q-img-status {
  font-size: 11px;
  font-weight: 600;
  padding: 1px 6px;
  border-radius: 4px;
  background: #f0f3f7;
  color: #6e7b8f;
}

.q-img-status.approved { background: #f0fff8; color: #087c55; }
.q-img-status.rejected { background: #fff0f0; color: #c54858; }

.q-meta p {
  margin: 2px 0;
  font-size: 12px;
  color: #6e7b8f;
}
</style>
