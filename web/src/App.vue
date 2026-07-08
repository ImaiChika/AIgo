<script setup>
import { computed, ref } from "vue";

const navItems = [
  { key: "generate", icon: "✦", label: "AI出题" },
  { key: "knowledge", icon: "⌁", label: "知识点" },
  { key: "review", icon: "✓", label: "多轮审核" },
  { key: "bank", icon: "□", label: "题库" },
];

const topics = ["急性心肌梗死", "稳定型心绞痛", "心电图诊断"];

const sampleStems = {
  急性心肌梗死:
    "患者，男，58岁，突发胸骨后压榨性疼痛2小时，伴大汗，休息后不缓解。既往高血压病史5年，吸烟20年。心电图提示相邻导联ST段抬高。",
  稳定型心绞痛:
    "患者，男，62岁，反复活动后胸骨后疼痛半年，每次持续3-5分钟，休息后可缓解。查体无明显异常，静息心电图未见明显改变。",
  心电图诊断:
    "患者，女，66岁，胸痛伴气促1小时入院。心电图示多个相邻导联ST段改变，需结合临床表现判断最可能诊断。",
};

const optionSets = {
  急性心肌梗死: ["稳定型心绞痛", "急性心肌梗死", "主动脉夹层", "心包炎"],
  稳定型心绞痛: ["变异型心绞痛", "稳定型心绞痛", "急性肺栓塞", "急性心肌梗死"],
  心电图诊断: ["窦性心动过速", "房室传导阻滞", "急性冠脉综合征", "低钾血症"],
};

const activeNav = ref("generate");
const currentStep = ref(1);
const selectedTopic = ref("急性心肌梗死");
const selectedCount = ref(5);
const selectedImage = ref(0);
const toast = ref("");
const stem = ref(sampleStems[selectedTopic.value]);
const options = ref([...optionSets[selectedTopic.value]]);
const promptText = ref(`图片用途：执业医师考试 A2 型题配图
图片类型：医学教学示意图
医学主题：胸痛患者心电图异常提示
必须出现：心电图纸、ST段异常提示、清晰导联线
不能出现：真实患者信息、医院标识、文字答案提示
风格：医学教材示意图，清晰，简洁，非照片
审核重点：是否与题干胸痛场景一致，是否误导答案唯一性`);

const previewOptions = computed(() => options.value.filter(Boolean));

function showToast(message) {
  toast.value = message;
  window.clearTimeout(showToast.timer);
  showToast.timer = window.setTimeout(() => {
    toast.value = "";
  }, 2200);
}

function applyTopic(topic) {
  selectedTopic.value = topic;
  stem.value = sampleStems[topic];
  options.value = [...optionSets[topic]];
}

function generateQuestion() {
  applyTopic(selectedTopic.value);
  showToast("已模拟调用千问生成 A2 题草稿");
}

function generateImages() {
  selectedImage.value = 0;
  showToast("已生成 4 张 AI 配图候选");
}

function selectImage(index) {
  selectedImage.value = index;
  showToast(`已选择候选图 ${index + 1}，等待专家审核`);
}

function submitReview() {
  showToast("已提交到命题教师初审，题目和配图将分开审核");
}

function addOption() {
  if (options.value.length >= 5) {
    showToast("A2 单选题最多保留 A-E 五个选项");
    return;
  }
  options.value.push("新增选项");
}

function optionLabel(index) {
  return String.fromCharCode(65 + index);
}

function ecgPath(index) {
  if (index % 2 === 0) {
    return "M18 76 L52 76 L60 58 L69 98 L80 45 L90 76 L126 76 L136 66 L148 84 L158 76 L214 76 L224 54 L234 98 L244 74 L286 74";
  }
  return "M18 72 L50 72 L60 66 L70 80 L82 30 L93 94 L104 72 L138 72 L148 64 L160 86 L172 72 L220 72 L232 52 L244 91 L258 72 L286 72";
}

function candidateColor(index) {
  return ["#d8eef8", "#eef2ff", "#fff3e2", "#e9f8ef"][index % 4];
}

function candidateAccent(index) {
  return ["#0d7be8", "#4e6ce8", "#dd8a00", "#199e63"][index % 4];
}
</script>

<template>
  <div class="app-shell">
    <aside class="sidebar" aria-label="主导航">
      <div class="brand">
        <div class="brand-mark">A</div>
        <div>
          <strong>AIgo</strong>
          <span>智能命题</span>
        </div>
      </div>

      <nav class="nav-list">
        <button
          v-for="item in navItems"
          :key="item.key"
          class="nav-item"
          :class="{ active: activeNav === item.key }"
          type="button"
          @click="activeNav = item.key"
        >
          <span class="nav-icon">{{ item.icon }}</span>
          {{ item.label }}
        </button>
      </nav>

      <div class="sidebar-note">
        <span>当前版本</span>
        <strong>千问直出题 + AI配图候选 + 专家审核</strong>
      </div>
    </aside>

    <main class="workspace">
      <header class="topbar">
        <div>
          <h1>AI智能出题工作台</h1>
          <p>按照知识点生成 A2 型题，并为题目生成 3-5 张可审核配图候选。</p>
        </div>
        <div class="top-actions">
          <button class="ghost-button" type="button" @click="showToast('草稿已保存到本地模拟状态')">
            保存草稿
          </button>
          <button class="primary-button" type="button" @click="submitReview">提交审核</button>
        </div>
      </header>

      <section class="notice-strip" aria-label="当前缺口提示">
        <div>
          <strong>当前还缺：</strong>
          后端 API 对接、真实千问调用、Excel 上传接口、真实生图模型、专家审核流页面落库。
        </div>
        <span>前端先按最终工作流搭可交互原型</span>
      </section>

      <section class="main-grid">
        <div class="editor-column">
          <section class="panel progress-panel" aria-label="生成题目列表">
            <div class="panel-title-row">
              <h2>AI生成试题（共5题）</h2>
              <span>当前编辑：第{{ currentStep }}题</span>
            </div>
            <div class="step-list" aria-label="题目分页">
              <button
                v-for="step in 5"
                :key="step"
                class="step"
                :class="{ active: currentStep === step }"
                type="button"
                @click="currentStep = step; showToast(`切换到第 ${step} 题`)"
              >
                {{ step }}
              </button>
            </div>
          </section>

          <section class="panel">
            <div class="section-heading">
              <span class="dot blue"></span>
              <h2>知识点标签</h2>
            </div>
            <div class="tag-row">
              <button
                v-for="topic in topics"
                :key="topic"
                class="tag"
                :class="{ active: selectedTopic === topic }"
                type="button"
                @click="applyTopic(topic)"
              >
                {{ topic }}
              </button>
            </div>
          </section>

          <section class="panel">
            <div class="section-heading">
              <span class="dot blue"></span>
              <h2>题干编辑</h2>
            </div>
            <textarea v-model="stem" class="stem-input" aria-label="题干编辑"></textarea>
          </section>

          <section class="panel">
            <div class="section-heading">
              <span class="dot teal"></span>
              <h2>AI配图候选</h2>
              <small>选择一张后提交专家审核</small>
            </div>
            <div class="image-prompt">
              <label for="promptInput">结构化生图提示词</label>
              <textarea id="promptInput" v-model="promptText" aria-label="结构化生图提示词"></textarea>
              <button class="outline-button" type="button" @click="generateImages">生成4张候选图</button>
            </div>
            <div class="candidate-grid">
              <button
                v-for="index in 4"
                :key="index"
                class="candidate-card"
                :class="{ selected: selectedImage === index - 1 }"
                type="button"
                @click="selectImage(index - 1)"
              >
                <svg
                  class="candidate-art"
                  viewBox="0 0 304 188"
                  role="img"
                  :aria-label="`AI候选医学示意图${index}`"
                >
                  <rect width="304" height="188" :fill="candidateColor(index - 1)" />
                  <g opacity="0.5" stroke="#ffffff" stroke-width="1">
                    <path v-for="line in 15" :key="`v-${line}`" :d="`M${(line - 1) * 22} 0 V188`" />
                    <path v-for="line in 9" :key="`h-${line}`" :d="`M0 ${(line - 1) * 22} H304`" />
                  </g>
                  <rect
                    x="20"
                    y="20"
                    width="264"
                    height="128"
                    rx="6"
                    fill="rgba(255,255,255,.72)"
                    stroke="rgba(80,100,130,.18)"
                  />
                  <path
                    :d="ecgPath(index - 1)"
                    fill="none"
                    :stroke="candidateAccent(index - 1)"
                    stroke-width="4"
                    stroke-linecap="round"
                    stroke-linejoin="round"
                  />
                  <circle
                    :cx="selectedImage === index - 1 ? 264 : 42"
                    cy="160"
                    r="10"
                    :fill="selectedImage === index - 1 ? '#1385f8' : '#ffffff'"
                    :stroke="selectedImage === index - 1 ? '#1385f8' : '#b8c4d4'"
                    stroke-width="2"
                  />
                  <path
                    v-if="selectedImage === index - 1"
                    d="M258 160 l4 4 l8 -9"
                    fill="none"
                    stroke="#fff"
                    stroke-width="3"
                    stroke-linecap="round"
                    stroke-linejoin="round"
                  />
                  <text x="20" y="169" fill="#5f7087" font-size="12">教学示意图候选 {{ index }}</text>
                </svg>
                <div class="candidate-meta">
                  <span>候选图 {{ index }}</span>
                  <span class="status">{{ selectedImage === index - 1 ? "已选择" : "待审" }}</span>
                </div>
              </button>
            </div>
          </section>

          <section class="panel">
            <div class="section-heading">
              <span class="dot blue"></span>
              <h2>选项编辑</h2>
            </div>
            <div class="options-list">
              <div v-for="(_, index) in options" :key="index" class="option-row">
                <span class="option-label">{{ optionLabel(index) }}</span>
                <input v-model="options[index]" :aria-label="`${optionLabel(index)}选项`" />
              </div>
            </div>
            <button class="text-button" type="button" @click="addOption">+ 添加选项</button>
          </section>
        </div>

        <aside class="config-column">
          <section class="panel sticky-panel">
            <h2>AI出题配置</h2>

            <div class="metric-row">
              <div>
                <span>题库题量</span>
                <strong>42道</strong>
              </div>
              <div>
                <span>图片待审</span>
                <strong class="warning">4张</strong>
              </div>
            </div>

            <label class="field">
              <span>所属专科</span>
              <select>
                <option>内科学</option>
                <option>外科学</option>
                <option>儿科学</option>
              </select>
            </label>

            <label class="field">
              <span>难度系数</span>
              <select>
                <option value="medium">中等</option>
                <option value="easy">简单</option>
                <option value="hard">困难</option>
              </select>
            </label>

            <label class="field">
              <span>单知识点出题</span>
              <select v-model="selectedTopic" @change="applyTopic(selectedTopic)">
                <option v-for="topic in topics" :key="topic">{{ topic }}</option>
              </select>
            </label>

            <div class="segmented" aria-label="生成数量">
              <button
                v-for="count in [1, 5, 10]"
                :key="count"
                :class="{ active: selectedCount === count }"
                type="button"
                @click="selectedCount = count"
              >
                {{ count }}道
              </button>
            </div>

            <button class="primary-button full" type="button" @click="generateQuestion">AI自动生成试题</button>

            <section class="preview-card">
              <div class="section-heading compact">
                <span class="dot blue"></span>
                <h3>实时预览</h3>
              </div>
              <p>{{ stem }}</p>
              <ol type="A">
                <li v-for="option in previewOptions" :key="option">{{ option }}</li>
              </ol>
              <strong>答案：A</strong>
            </section>

            <section class="review-card">
              <h3>审核流</h3>
              <div class="review-step done">自动初评</div>
              <div class="review-step active">命题教师初审</div>
              <div class="review-step">专业专家复审</div>
              <div class="review-step">题库终审</div>
            </section>
          </section>
        </aside>
      </section>
    </main>
  </div>

  <div class="toast" :class="{ show: toast }" role="status" aria-live="polite">{{ toast }}</div>
</template>
