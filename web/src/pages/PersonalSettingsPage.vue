<script setup>
import { ref, watch } from "vue";
import { api } from "../api.js";
import { currentUser, getToken, roleName, setAuth } from "../auth.js";

const displayName = ref("");
const oldPassword = ref("");
const newPassword = ref("");
const confirmPassword = ref("");
const profileSaving = ref(false);
const passwordSaving = ref(false);
const profileMessage = ref("");
const profileError = ref("");
const passwordMessage = ref("");
const passwordError = ref("");

watch(currentUser, (value) => {
  displayName.value = value?.display_name || "";
}, { immediate: true });

async function saveProfile() {
  const value = displayName.value.trim();
  profileMessage.value = "";
  profileError.value = "";
  if (!value) {
    profileError.value = "昵称不能为空";
    return;
  }
  profileSaving.value = true;
  try {
    const user = await api.updateProfile(value);
    setAuth(getToken(), user);
    profileMessage.value = "已保存";
  } catch (error) {
    profileError.value = error.message;
  } finally {
    profileSaving.value = false;
  }
}

async function changePassword() {
  passwordMessage.value = "";
  passwordError.value = "";
  if (!oldPassword.value) {
    passwordError.value = "请输入当前密码";
    return;
  }
  if (newPassword.value.length < 8) {
    passwordError.value = "新密码至少8位";
    return;
  }
  if (newPassword.value !== confirmPassword.value) {
    passwordError.value = "两次输入不一致";
    return;
  }
  passwordSaving.value = true;
  try {
    await api.changePassword(oldPassword.value, newPassword.value);
    oldPassword.value = "";
    newPassword.value = "";
    confirmPassword.value = "";
    passwordMessage.value = "密码已修改";
  } catch (error) {
    passwordError.value = error.message;
  } finally {
    passwordSaving.value = false;
  }
}
</script>

<template>
  <div class="settings-page">
    <section class="panel account-panel">
      <div class="section-heading">
        <span class="dot teal"></span>
        <h2>账号信息</h2>
      </div>
      <dl class="account-list">
        <div><dt>用户名</dt><dd>{{ currentUser?.username }}</dd></div>
        <div><dt>角色</dt><dd>{{ roleName(currentUser?.role) }}</dd></div>
        <div><dt>权限</dt><dd>{{ currentUser?.permissions?.length || 0 }} 项</dd></div>
      </dl>
    </section>

    <div class="settings-grid">
      <section class="panel form-panel">
        <div class="section-heading">
          <span class="dot blue"></span>
          <h2>昵称</h2>
        </div>
        <form @submit.prevent="saveProfile">
          <label for="personal-display-name">显示名</label>
          <input id="personal-display-name" v-model="displayName" autocomplete="name" />
          <p v-if="profileMessage" class="form-message success" role="status">{{ profileMessage }}</p>
          <p v-if="profileError" class="form-message error" role="alert">{{ profileError }}</p>
          <button class="primary-button" type="submit" :disabled="profileSaving">
            {{ profileSaving ? "保存中" : "保存" }}
          </button>
        </form>
      </section>

      <section class="panel form-panel">
        <div class="section-heading">
          <span class="dot blue"></span>
          <h2>修改密码</h2>
        </div>
        <form @submit.prevent="changePassword">
          <label for="current-password">当前密码</label>
          <input id="current-password" v-model="oldPassword" type="password" autocomplete="current-password" />
          <label for="new-password">新密码</label>
          <input id="new-password" v-model="newPassword" type="password" autocomplete="new-password" />
          <label for="confirm-password">确认新密码</label>
          <input id="confirm-password" v-model="confirmPassword" type="password" autocomplete="new-password" />
          <p v-if="passwordMessage" class="form-message success" role="status">{{ passwordMessage }}</p>
          <p v-if="passwordError" class="form-message error" role="alert">{{ passwordError }}</p>
          <button class="primary-button" type="submit" :disabled="passwordSaving">
            {{ passwordSaving ? "修改中" : "修改密码" }}
          </button>
        </form>
      </section>
    </div>
  </div>
</template>

<style scoped>
.settings-page {
  display: grid;
  gap: 16px;
  min-width: 0;
  max-width: 980px;
}

.account-panel {
  min-width: 0;
}

.account-list {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 8px;
  margin: 0;
}

.account-list > div {
  min-width: 0;
  padding: 11px 12px;
  border: 1px solid #e5ebe8;
  border-radius: 6px;
  background: #fafcfb;
}

.account-list dt {
  margin-bottom: 5px;
  color: #8a9691;
  font-size: 11px;
}

.account-list dd {
  margin: 0;
  overflow: hidden;
  color: #2e433b;
  font-size: 13px;
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.settings-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 16px;
}

.form-panel {
  min-width: 0;
}

.form-panel form {
  display: grid;
  gap: 8px;
}

.form-panel label {
  margin-top: 3px;
  color: #68766f;
  font-size: 12px;
  font-weight: 600;
}

.form-panel input {
  width: 100%;
  min-width: 0;
  height: 38px;
  padding: 0 11px;
  border: 1px solid #dfe6e3;
  border-radius: 6px;
  color: #263a33;
  background: #fff;
  outline: none;
}

.form-panel input:focus {
  border-color: #6e9b89;
  box-shadow: 0 0 0 3px rgba(76, 138, 112, .11);
}

.form-panel .primary-button {
  justify-self: start;
  margin-top: 5px;
  min-width: 96px;
}

.form-message {
  margin: 2px 0 0;
  font-size: 12px;
}

.form-message.success { color: #287657; }
.form-message.error { color: #b34351; }

@media (max-width: 700px) {
  .settings-grid {
    grid-template-columns: minmax(0, 1fr);
  }

  .account-list {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
