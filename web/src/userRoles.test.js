import test from "node:test";
import assert from "node:assert/strict";
import { normalizeRoleIDs, rolesAfterPrimarySwitch } from "./userRoles.js";

test("primary switch keeps existing identities and adds the new one", () => {
  // 审题老师切为主管理员：expert 身份必须保留，否则切回原身份会报“未分配角色模板”。
  assert.deepEqual(rolesAfterPrimarySwitch(["expert"], "admin"), ["expert", "admin"]);
});

test("primary switch to an already-assigned role leaves the set unchanged", () => {
  assert.deepEqual(rolesAfterPrimarySwitch(["expert", "teacher"], "teacher"), ["expert", "teacher"]);
});

test("primary switch tolerates duplicates, blanks and legacy single role", () => {
  assert.deepEqual(rolesAfterPrimarySwitch(["expert", "", "expert"], "admin"), ["expert", "admin"]);
  assert.deepEqual(rolesAfterPrimarySwitch([], "expert"), ["expert"]);
});

test("empty target role keeps the current set", () => {
  assert.deepEqual(rolesAfterPrimarySwitch(["expert"], ""), ["expert"]);
});

test("normalizeRoleIDs falls back to the legacy single role field", () => {
  assert.deepEqual(normalizeRoleIDs("expert", []), ["expert"]);
  assert.deepEqual(normalizeRoleIDs("expert", ["teacher", "expert"]), ["teacher", "expert"]);
});
