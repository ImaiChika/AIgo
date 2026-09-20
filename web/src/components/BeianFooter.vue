<template>
  <footer class="beian-footer">
    <a href="https://beian.miit.gov.cn/" rel="noreferrer" target="_blank">皖ICP备2026031767号</a>
    <a href="https://beian.mps.gov.cn/#/query/webSearch?code=34128202020226" rel="noreferrer" target="_blank">
      <img src="/beian.png" alt="公安联网备案图标" />
      <span>皖公网安备34128202020226号</span>
    </a>
  </footer>
</template>

<style scoped>
.beian-footer {
  margin-top: 28px;
  display: flex;
  align-items: center;
  justify-content: center;
  flex-wrap: wrap;
  gap: 6px 14px;
  text-align: center;
}

.beian-footer a {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  font-size: 12px;
  color: var(--muted);
  text-decoration: none;
}

.beian-footer a:hover {
  color: var(--blue-dark);
}

.beian-footer img {
  display: block;
  width: 18px;
  height: 20px;
}
</style>
