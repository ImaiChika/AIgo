<script setup>
import { ref, computed, onMounted, watch } from "vue";
import { api } from "../api.js";
import { hasPerm } from "../auth.js";
import { useRoute, useRouter } from "vue-router";
import { useAICheckProgress } from "../aiCheckProgress.js";
import KnowledgePointPicker from "../components/KnowledgePointPicker.vue";
import AICheckScoreButton from "../components/AICheckScoreButton.vue";

// AI 检查分段进度（生成成功后轮询，见 aiCheckProgress.js）
const { progress: aiProgress, start: startAIProgress } = useAICheckProgress();
const route = useRoute();
const router = useRouter();

// 知识点选择（单选，使用完善的知识点选择器：精确+模糊搜索）
const selectedKPs = ref([]); // 单选模式下始终 0/1 个
const selectedKP = computed(() => (selectedKPs.value.length ? selectedKPs.value[0] : null));

// 出题配置
const selectedCount = ref(1);
const selectedDifficulty = ref("0.65");
const banks = ref([]);
const selectedBank = ref("");

// 状态
const toast = ref("");
const accessNotice = ref("");
const loading = ref(false);
const progressMsg = ref("");
const startTime = ref(0);
const stats = ref({ question_count: 0, knowledge_count: 0 });

// 生成结果
const generatedQuestions = ref([]);
const currentIndex = ref(0);
const stem = ref("");
const options = ref([]);
const answer = ref("");
const explanation = ref("");
let optionIdCounter = 0;

// 展示列表：检查中被淘汰的题自动移出预览（淘汰原因单独展示）
const displayQuestions = computed(() => {
  const discarded = new Set((aiProgress.value?.items || []).filter((i) => i.discarded).map((i) => i.question_id));
  if (!discarded.size) return generatedQuestions.value;
  return generatedQuestions.value.filter((q) => !discarded.has(q.id));
});
// 当前题在展示列表中的序号（1 起）
const displayIndex = computed(() => {
  const q = generatedQuestions.value[currentIndex.value];
  if (!q) return 0;
  return displayQuestions.value.findIndex((x) => x.id === q.id);
});
function showDisplayQuestion(dIndex) {
  const q = displayQuestions.value[dIndex];
  if (!q) return;
  const realIndex = generatedQuestions.value.findIndex((x) => x.id === q.id);
  if (realIndex >= 0) showQuestion(realIndex);
}
function prevDisplay() {
  if (displayIndex.value > 0) showDisplayQuestion(displayIndex.value - 1);
}
function nextDisplay() {
  if (displayIndex.value < displayQuestions.value.length - 1) showDisplayQuestion(displayIndex.value + 1);
}

// 当前展示题目的 ID（供 AI 检查评分按钮查询检查结果）
const currentQuestionId = computed(() => generatedQuestions.value[currentIndex.value]?.id || "");

// 当前预览的题被淘汰时，自动跳回第一道通过的题
watch(displayQuestions, (list) => {
  const q = generatedQuestions.value[currentIndex.value];
  if (q && !list.some((x) => x.id === q.id) && list.length) {
    showDisplayQuestion(0);
  }
});

const workflowSteps = computed(() => [
  {
    number: "01",
    title: "AI 起草",
    detail: selectedKP.value ? `围绕「${selectedKP.value.topic || selectedKP.value.outline_code}」生成` : "选择知识点与难度后开始",
    state: stem.value ? "done" : selectedKP.value ? "active" : "waiting",
  },
  {
    number: "02",
    title: "自动质检",
    detail: aiProgress.value?.checking > 0 ? `正在检查 ${aiProgress.value.checking} 道题` : aiProgress.value ? "格式、答案与医学质量已检查" : "落库后自动异步执行一次",
    state: aiProgress.value?.checking > 0 ? "active" : aiProgress.value ? "done" : "waiting",
  },
  {
    number: "03",
    title: "进入待审核题库",
    detail: aiProgress.value ? `通过 ${aiProgress.value.passed || 0} 道，淘汰 ${aiProgress.value.discarded || 0} 道` : "质检通过后可由管理员送审",
    state: aiProgress.value && aiProgress.value.checking === 0 ? "done" : "waiting",
  },
]);

function showToast(message) {
  toast.value = message;
  window.clearTimeout(showToast.timer);
  toast.timer = window.setTimeout(() => { toast.value = ""; }, 3000);
}

function consumeAccessNotice() {
  if (route.query.notice !== "batch-permission") return;
  accessNotice.value = "当前账号未分配批量推理权限；如需使用批量推理，请联系超级管理员在「用户管理」中单独勾选该权限。";
  const query = { ...route.query };
  delete query.notice;
  router.replace({ path: route.path, query });
}

watch(() => route.query.notice, consumeAccessNotice, { immediate: true });

async function loadStats() {
  if (!hasPerm("stats:view")) return;
  try {
    stats.value = await api.stats("personal");
  } catch (e) {
    console.error(e);
    showToast("加载统计信息失败: " + e.message);
  }
}

// 生成题目
async function generateQuestion() {
  if (!selectedKP.value) {
    showToast("请先选择知识点");
    return;
  }
  loading.value = true;
  progressMsg.value = "正在生成试题，请稍候...";
  startTime.value = Date.now();

  const timer = setInterval(() => {
    const elapsed = ((Date.now() - startTime.value) / 1000).toFixed(0);
    progressMsg.value = `正在生成，已等待 ${elapsed} 秒`;
  }, 1000);

  try {
    const genParams = {
      subject: selectedKP.value.subject || "临床医学",
      category: selectedKP.value.category || "",
      difficulty: selectedDifficulty.value,
      topic: selectedKP.value.topic,
      outline_code: selectedKP.value.outline_code || "",
      knowledge_point_id: selectedKP.value.id,
      version_id: selectedKP.value.version_id,
      count: selectedCount.value,
      bank_id: selectedBank.value,
    };
    console.log("生成参数:", genParams, "选中知识点:", JSON.stringify(selectedKP.value));
    const data = await api.generate(genParams);
    clearInterval(timer);
    const elapsed = ((Date.now() - startTime.value) / 1000).toFixed(1);
    if (data.questions && data.questions.length > 0) {
      generatedQuestions.value = data.questions;
      currentIndex.value = 0;
      showQuestion(0);
      progressMsg.value = `已生成 ${data.count} 道题，耗时 ${elapsed} 秒，正在自动检查质量`;
      showToast(`已生成 ${data.count} 道题，耗时 ${elapsed} 秒（AI 检查进行中）`);
      startAIProgress((data.questions || []).map((q) => q.id));
      loadStats();
    }
  } catch (e) {
    clearInterval(timer);
    progressMsg.value = `生成失败: ${e.message}`;
    showToast("生成失败: " + e.message);
  } finally {
    loading.value = false;
  }
}

function showQuestion(index) {
  if (index < 0 || index >= generatedQuestions.value.length) return;
  currentIndex.value = index;
  const q = generatedQuestions.value[index];
  stem.value = q.clinical_stem || "";
  options.value = (q.options || []).map((o) => ({ id: ++optionIdCounter, text: o.text }));
  answer.value = q.answer || "";
  explanation.value = q.explanation || "";
}

function optionLabel(index) {
  return String.fromCharCode(65 + index);
}

async function loadBanks() {
  try {
    const data = await api.listBanks(false);
    banks.value = data.banks || [];
  } catch (e) {
    console.error(e);
  }
}

onMounted(() => {
  loadStats();
  loadBanks();
});
</script>

<template>
  <div v-if="accessNotice" class="permission-notice" role="status">
    <span>{{ accessNotice }}</span>
    <button type="button" aria-label="关闭提示" @click="accessNotice = ''">×</button>
  </div>
  <div class="main-grid">
    <div class="editor-column">
      <!-- 多题切换（数字直达 + 左右切换；被淘汰的题自动移出） -->
      <section v-if="displayQuestions.length" class="panel question-nav">
        <button class="ghost-button" type="button" :disabled="displayIndex <= 0" @click="prevDisplay">←</button>
        <button
          v-for="(q, i) in displayQuestions"
          :key="q.id"
          class="nav-chip"
          :class="{ active: i === displayIndex }"
          type="button"
          :title="`第 ${i + 1} 题`"
          @click="showDisplayQuestion(i)"
        >
          {{ i + 1 }}
        </button>
        <button class="ghost-button" type="button" :disabled="displayIndex >= displayQuestions.length - 1" @click="nextDisplay">→</button>
        <span class="nav-info">第 {{ displayIndex + 1 }} / {{ displayQuestions.length }} 题</span>
      </section>

      <!-- 生成结果（只读展示：AI 出题内容不可编辑，检查通过后进入题库） -->
      <section class="panel">
        <div class="section-heading">
          <span class="dot blue"></span>
          <h2>题目预览</h2>
          <small v-if="answer">正确答案：{{ answer }}</small>
          <AICheckScoreButton v-if="currentQuestionId" class="preview-ai-score" :question-id="currentQuestionId" />
        </div>
        <template v-if="stem">
          <p class="readonly-stem">{{ stem }}</p>
          <div class="readonly-options">
            <div v-for="(opt, index) in options" :key="opt.id" class="readonly-option" :class="{ correct: optionLabel(index) === answer }">
              <span class="option-label">{{ optionLabel(index) }}</span>
              <span>{{ opt.text }}</span>
              <span v-if="optionLabel(index) === answer" class="correct-mark">✓ 正确答案</span>
            </div>
          </div>
          <div v-if="explanation" class="readonly-explanation">
            <label>解析</label>
            <p>{{ explanation }}</p>
          </div>
        </template>
        <div v-else class="preview-empty">
          <span class="preview-empty-mark">A2</span>
          <div>
            <strong>从右侧选择一个知识点开始命题</strong>
            <p>系统将生成临床情境、4–5 个选项与唯一答案；生成结果只读，质量检查通过后进入待审核题库。</p>
          </div>
        </div>
      </section>

      <!-- AI 检查进度与本次生题具体情况 -->
      <div v-if="aiProgress" class="progress-bar ai-check-progress" :class="{ done: aiProgress.checking === 0 && !aiProgress.stalled, error: aiProgress.stalled || aiProgress.exhausted > 0 }">
        <span v-if="aiProgress.checking > 0" class="spinner"></span>
        <template v-if="aiProgress.checking > 0">AI 检查中（完成 {{ aiProgress.total - aiProgress.checking }}/{{ aiProgress.total }}）…</template>
        <template v-else-if="aiProgress.stalled">AI 检查仍在后台进行，可稍后在题库中查看结果</template>
        <template v-else>
          本次生成 {{ aiProgress.total }} 道：通过 {{ aiProgress.passed }} · 淘汰 {{ aiProgress.discarded }}<template v-if="aiProgress.exhausted > 0"> · 检查异常 {{ aiProgress.exhausted }}（可重试）</template>
        </template>
      </div>

      <!-- 淘汰明细：检查不通过的题目已自动删除，展示原因 -->
      <section v-if="aiProgress && aiProgress.discarded > 0" class="panel discard-panel">
        <div class="section-heading">
          <span class="dot red"></span>
          <h2>未通过检查的题目（已自动删除）</h2>
        </div>
        <div v-for="item in (aiProgress.items || []).filter(i => i.discarded)" :key="item.question_id" class="discard-item">
          <p class="discard-stem">{{ item.stem_summary }}</p>
          <p class="discard-verdict">
            检查结论：{{ item.verdict === "reject" ? "不合格" : "存在问题" }}
          </p>
          <ul v-if="(item.issues || []).length" class="discard-issues">
            <li v-for="(issue, i) in item.issues" :key="i">{{ issue.message }}</li>
          </ul>
          <p v-if="item.suggestion" class="discard-suggestion">AI 建议：{{ item.suggestion }}</p>
        </div>
        <p class="discard-hint">被淘汰的题目不会进入题库；可调整生成参数后重新出题。</p>
      </section>

      <section class="panel workflow-panel">
        <div class="section-heading">
          <span class="dot green"></span>
          <h2>命题闭环</h2>
          <small>本页只负责生成，修订与送审均按权限流转</small>
        </div>
        <div class="workflow-rail">
          <div v-for="step in workflowSteps" :key="step.number" class="workflow-step" :class="step.state">
            <div class="workflow-number">{{ step.state === "done" ? "✓" : step.number }}</div>
            <div>
              <strong>{{ step.title }}</strong>
              <p>{{ step.detail }}</p>
            </div>
          </div>
        </div>
        <div class="quality-contract">
          <div>
            <span class="quality-kicker">首次生成自动检查</span>
            <strong>先筛除明显问题，再进入人工审核</strong>
          </div>
          <ul>
            <li>题干完整</li>
            <li>答案唯一</li>
            <li>A2 题型匹配</li>
          </ul>
        </div>
      </section>
    </div>

    <!-- 右侧配置 -->
    <aside class="config-column">
      <section class="panel sticky-panel">
        <h2>生成设置</h2>

        <div v-if="hasPerm('stats:view')" class="metric-row">
          <div>
            <span>题库题量</span>
            <strong>{{ stats.question_count }}道</strong>
          </div>
          <div>
            <span>知识点数</span>
            <strong>{{ stats.knowledge_count }}个</strong>
          </div>
        </div>

        <!-- 知识点选择器（精确+模糊搜索，单选） -->
        <div class="kp-selector">
          <label class="field-label">选择知识点</label>
          <KnowledgePointPicker v-model="selectedKPs" :multiple="false" placeholder="搜索知识点、大纲代码、专业...（支持精确筛选与模糊搜索）" />
        </div>

        <label class="field">
          <span>难度系数</span>
          <select v-model="selectedDifficulty">
            <option value="0.55">简单 (0.55)</option>
            <option value="0.65">中等 (0.65)</option>
            <option value="0.75">偏难 (0.75)</option>
            <option value="0.85">困难 (0.85)</option>
          </select>
        </label>

        <label class="field" v-if="banks.length">
          <span>目标题库</span>
          <select v-model="selectedBank">
            <option value="">未分类</option>
            <option v-for="b in banks" :key="b.id" :value="b.id">{{ b.name }}</option>
          </select>
        </label>

        <div class="count-row">
          <label>生成数量：</label>
          <div class="segmented" aria-label="生成数量">
            <button
              v-for="count in [1, 3, 5]"
              :key="count"
              :class="{ active: selectedCount === count }"
              type="button"
              @click="selectedCount = count"
            >
              {{ count }}道
            </button>
          </div>
          <input
            v-model.number="selectedCount"
            type="number"
            min="1"
            max="20"
            class="count-input"
            placeholder="自定义"
          />
        </div>

        <button
          class="primary-button full"
          type="button"
          :disabled="loading || !selectedKP"
          @click="generateQuestion"
        >
          {{ loading ? "生成中..." : "生成试题" }}
        </button>

        <!-- 生成进度提示 -->
        <div v-if="progressMsg && !aiProgress" class="progress-bar">
          <div class="progress-inner" :class="{ done: progressMsg.startsWith('已生成'), error: progressMsg.startsWith('生成失败') }">
            <span v-if="loading" class="spinner"></span>
            {{ progressMsg }}
          </div>
        </div>

        <section v-if="stem" class="preview-card">
          <div class="section-heading compact">
            <span class="dot blue"></span>
            <h3>题目预览</h3>
          </div>
          <p>{{ stem.slice(0, 150) }}{{ stem.length > 150 ? "..." : "" }}</p>
          <ol type="A">
            <li v-for="opt in options" :key="opt.id">{{ opt.text }}</li>
          </ol>
          <strong v-if="answer">答案：{{ answer }}</strong>
        </section>
      </section>
    </aside>
  </div>

  <div class="toast" :class="{ show: toast }" role="status" aria-live="polite">{{ toast }}</div>
</template>

<style scoped>
.permission-notice {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  margin: 0 0 14px;
  padding: 11px 14px;
  border: 1px solid #f1d4d8;
  border-left: 3px solid #c54858;
  border-radius: 7px;
  background: #fff8f9;
  color: #8a3b47;
  font-size: 13px;
  line-height: 1.6;
}

.permission-notice button {
  flex: none;
  border: 0;
  background: transparent;
  color: #a23b4b;
  font-size: 18px;
  line-height: 1;
  cursor: pointer;
}

/* 知识点选择器 */
.kp-selector {
  margin-bottom: 16px;
}

.field-label {
  display: block;
  font-size: 13px;
  font-weight: 600;
  color: #6e7b8f;
  margin-bottom: 6px;
}

.selected-kp {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 12px;
  background: #eff8ff;
  border: 1px solid #b9ddff;
  border-radius: 8px;
}

.kp-info {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.kp-code {
  font-size: 11px;
  font-family: monospace;
  color: #0571dc;
}

.kp-subject {
  font-size: 11px;
  color: #6e7b8f;
}

.kp-topic {
  font-size: 13px;
  color: #172033;
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.clear-btn {
  width: 24px;
  height: 24px;
  border: 1px solid #b9ddff;
  border-radius: 50%;
  background: #fff;
  color: #0571dc;
  font-size: 16px;
  cursor: pointer;
  display: grid;
  place-items: center;
  flex-shrink: 0;
}

.clear-btn:hover {
  background: #0571dc;
  color: #fff;
}

.kp-search-box {
  position: relative;
}

.kp-search-input {
  width: 100%;
  height: 38px;
  border: 1px solid #e5ebf3;
  border-radius: 8px;
  padding: 0 12px;
  font-size: 13px;
}

.kp-search-input:focus {
  border-color: #1385f8;
  outline: none;
  box-shadow: 0 0 0 3px rgba(19, 133, 248, 0.1);
}

.kp-dropdown {
  position: fixed;
  top: auto;
  left: auto;
  width: 300px;
  margin-top: 4px;
  background: #fff;
  border: 1px solid #e5ebf3;
  border-radius: 8px;
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.12);
  z-index: 1000;
  max-height: 400px;
  overflow: hidden;
  display: flex;
  flex-direction: column;
}

.kp-list {
  overflow-y: auto;
  max-height: 350px;
}

.kp-item {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 8px 12px;
  border: 0;
  background: transparent;
  text-align: left;
  cursor: pointer;
  font-size: 13px;
  border-bottom: 1px solid #f0f3f7;
}

.kp-item:hover {
  background: #f8fbff;
}

.kp-item-code {
  font-size: 10px;
  font-family: monospace;
  color: #0571dc;
  background: #eff8ff;
  padding: 2px 6px;
  border-radius: 3px;
  flex-shrink: 0;
}

.kp-item-subject {
  font-size: 11px;
  color: #6e7b8f;
  flex-shrink: 0;
}

.kp-item-topic {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.kp-loading, .kp-empty {
  padding: 20px;
  text-align: center;
  color: #6e7b8f;
  font-size: 13px;
}

.kp-load-more {
  padding: 8px;
  text-align: center;
  border-top: 1px solid #f0f3f7;
}

.dropdown-overlay {
  position: fixed;
  inset: 0;
  z-index: 150;
}

/* 其他样式 */
.count-row {
  display: flex;
  align-items: center;
  gap: 10px;
}

.count-row label {
  font-size: 13px;
  color: #6e7b8f;
  white-space: nowrap;
}

.count-input {
  width: 60px;
  height: 34px;
  border: 1px solid #e5ebf3;
  border-radius: 7px;
  padding: 0 8px;
  text-align: center;
  font-size: 13px;
}

.question-nav {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  flex-wrap: wrap;
}

.nav-chip {
  min-width: 30px;
  height: 30px;
  border: 1px solid #e5ebf3;
  border-radius: 6px;
  background: #fff;
  color: #6e7b8f;
  font-size: 13px;
  font-weight: 700;
  cursor: pointer;
}

.nav-chip:hover {
  border-color: #1385f8;
  color: #1385f8;
}

.nav-chip.active {
  background: #1385f8;
  border-color: #1385f8;
  color: #fff;
}

.nav-info {
  font-size: 14px;
  font-weight: 600;
  color: #172033;
}

.remove-btn {
  width: 28px;
  height: 28px;
  border: 1px solid #e5ebf3;
  border-radius: 6px;
  background: #fff;
  color: #c54858;
  font-size: 18px;
  cursor: pointer;
  display: grid;
  place-items: center;
  flex-shrink: 0;
}

.remove-btn:hover:not(:disabled) {
  background: #fff0f0;
  border-color: #c54858;
}

.remove-btn:disabled {
  opacity: 0.3;
  cursor: not-allowed;
}

.answer-row {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: 12px;
}

.answer-row label {
  font-size: 13px;
  color: #6e7b8f;
  font-weight: 600;
}

.answer-row select {
  height: 34px;
  border: 1px solid #e5ebf3;
  border-radius: 7px;
  padding: 0 12px;
  font-size: 14px;
  font-weight: 700;
  color: #1385f8;
}

.explanation-input {
  width: 100%;
  min-height: 120px;
  border: 1px solid #e5ebf3;
  border-radius: 8px;
  padding: 12px;
  font-size: 14px;
  line-height: 1.7;
  color: #435269;
  resize: vertical;
}

.save-section {
  display: flex;
  align-items: center;
  gap: 14px;
}

.save-hint {
  font-size: 13px;
  color: #6e7b8f;
}

.progress-bar {
  margin-top: 10px;
}

.progress-inner {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 12px;
  border-radius: 7px;
  background: #eff8ff;
  color: #0571dc;
  font-size: 13px;
  font-weight: 600;
}

.progress-inner.done {
  background: #f0fff8;
  color: #087c55;
}

.progress-inner.error {
  background: #fff0f0;
  color: #c54858;
}

.spinner {
  width: 14px;
  height: 14px;
  border: 2px solid #b9ddff;
  border-top-color: #0571dc;
  border-radius: 50%;
  animation: spin 0.8s linear infinite;
}

@keyframes spin {
  to { transform: rotate(360deg); }
}

/* 生成结果只读展示 */
.readonly-stem {
  font-size: 14px;
  line-height: 1.7;
  color: #172033;
  margin: 0 0 12px;
}

.readonly-options {
  display: grid;
  gap: 6px;
  margin-bottom: 12px;
}

.readonly-option {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  color: #3a4658;
}

.readonly-option.correct {
  color: #087c55;
  font-weight: 600;
}

.correct-mark {
  font-size: 12px;
  color: #087c55;
}

.readonly-explanation label {
  font-size: 12px;
  color: #9aa5b4;
}

.readonly-explanation p {
  font-size: 13px;
  line-height: 1.7;
  color: #3a4658;
  margin: 4px 0 0;
}

.preview-empty {
  min-height: 220px;
  display: grid;
  grid-template-columns: auto minmax(0, 1fr);
  align-content: center;
  align-items: center;
  gap: 22px;
  padding: 26px;
  border: 1px dashed #c9d9ea;
  border-radius: 12px;
  background: linear-gradient(135deg, #f8fbff 0%, #f2f8f6 100%);
}

.preview-empty-mark {
  width: 74px;
  height: 74px;
  display: grid;
  place-items: center;
  border-radius: 22px 8px 22px 8px;
  background: #103b61;
  color: #fff;
  font: 800 20px/1 Georgia, serif;
  letter-spacing: 0.08em;
  box-shadow: 0 12px 24px rgba(16, 59, 97, 0.18);
}

.preview-empty strong {
  display: block;
  margin-bottom: 8px;
  color: #172033;
  font-size: 17px;
}

.preview-empty p {
  max-width: 620px;
  margin: 0;
  color: #607086;
  font-size: 13px;
  line-height: 1.75;
}

.workflow-panel {
  overflow: hidden;
  background: linear-gradient(160deg, #ffffff 0%, #f8fbff 62%, #f4faf7 100%);
}

.workflow-rail {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 10px;
}

.workflow-step {
  position: relative;
  display: grid;
  grid-template-columns: 36px minmax(0, 1fr);
  gap: 10px;
  min-height: 112px;
  padding: 15px;
  border: 1px solid #e1e9f2;
  border-radius: 10px;
  background: rgba(255, 255, 255, 0.78);
}

.workflow-step.active {
  border-color: #8dc7ff;
  box-shadow: inset 0 0 0 1px rgba(19, 133, 248, 0.1);
}

.workflow-step.done {
  border-color: #a9ddc8;
  background: rgba(244, 252, 248, 0.9);
}

.workflow-number {
  width: 36px;
  height: 36px;
  display: grid;
  place-items: center;
  border-radius: 50%;
  background: #edf2f7;
  color: #78879a;
  font-size: 11px;
  font-weight: 800;
}

.workflow-step.active .workflow-number { background: #1385f8; color: #fff; }
.workflow-step.done .workflow-number { background: #11835d; color: #fff; font-size: 15px; }

.workflow-step strong {
  display: block;
  margin: 3px 0 6px;
  color: #1c2a3d;
  font-size: 14px;
}

.workflow-step p {
  margin: 0;
  color: #6e7b8f;
  font-size: 12px;
  line-height: 1.55;
}

.quality-contract {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 18px;
  margin-top: 12px;
  padding: 13px 15px;
  border-left: 3px solid #11835d;
  background: rgba(235, 248, 242, 0.72);
}

.quality-contract strong { display: block; color: #17382d; font-size: 13px; }
.quality-kicker { display: block; margin-bottom: 3px; color: #11835d; font-size: 10px; font-weight: 800; letter-spacing: 0.08em; }
.quality-contract ul { display: flex; gap: 7px; margin: 0; padding: 0; list-style: none; flex-wrap: wrap; justify-content: flex-end; }
.quality-contract li { padding: 4px 8px; border: 1px solid #b7dfcf; border-radius: 999px; color: #26634f; background: #fff; font-size: 11px; white-space: nowrap; }

@media (max-width: 760px) {
  .preview-empty { grid-template-columns: 1fr; min-height: 0; padding: 20px; }
  .workflow-rail { grid-template-columns: 1fr; }
  .workflow-step { min-height: 0; }
  .quality-contract { align-items: flex-start; flex-direction: column; }
  .quality-contract ul { justify-content: flex-start; }
}

/* 淘汰明细 */
.discard-panel {
  border-color: #f3d9b0;
}

.discard-item {
  border: 1px solid #f3e2c2;
  background: #fffdf7;
  border-radius: 8px;
  padding: 10px 12px;
  margin-bottom: 8px;
}

.discard-stem {
  font-size: 13px;
  font-weight: 600;
  color: #172033;
  margin: 0 0 6px;
}

.discard-verdict {
  font-size: 12px;
  color: #c07b22;
  margin: 0 0 6px;
}

.discard-issues {
  margin: 0 0 6px;
  padding-left: 18px;
  font-size: 12px;
  color: #3a4658;
}

.discard-suggestion {
  font-size: 12px;
  color: #6e7b8f;
  margin: 0 0 4px;
}

.discard-hint {
  font-size: 12px;
  color: #9aa5b4;
  margin: 4px 0 0;
}

/* 预览标题行右侧的 AI 检查评分按钮 */
.preview-ai-score {
  margin-left: auto;
}
</style>
