import test from "node:test";
import assert from "node:assert/strict";
import { normalizeRoleIDs, rolesAfterRoleChange } from "./userRoles.js";

test("single-identity upgrade keeps the original job as additional identity", () => {
  // 审题老师升级管理员：expert 保留为附加身份，账号可随时切回原岗位。
  assert.deepEqual(rolesAfterRoleChange(["expert"], "expert", "admin"), ["admin", "expert"]);
});

test("leaving a role removes it from additional identities", () => {
  // 主身份从管理员换为命题教师：管理员附加勾选自动取消，原岗位 expert 保留。
  assert.deepEqual(
    rolesAfterRoleChange(["expert", "admin"], "admin", "teacher"),
    ["expert", "teacher"],
  );
});

test("rotation among attached identities replaces the previous primary", () => {
  assert.deepEqual(
    rolesAfterRoleChange(["expert", "teacher"], "teacher", "expert"),
    ["expert"],
  );
});

test("revoking a granted role via dropdown returns to the remaining set", () => {
  assert.deepEqual(
    rolesAfterRoleChange(["expert", "admin"], "admin", "expert"),
    ["expert"],
  );
});

test("tolerates duplicates, blanks and legacy single role field", () => {
  assert.deepEqual(rolesAfterRoleChange(["", "expert", "expert"], "expert", "admin"), ["admin", "expert"]);
  assert.deepEqual(rolesAfterRoleChange([], "", "teacher"), ["teacher"]);
});

test("normalizeRoleIDs falls back to the legacy single role field", () => {
  assert.deepEqual(normalizeRoleIDs("expert", []), ["expert"]);
  assert.deepEqual(normalizeRoleIDs("expert", ["teacher", "expert"]), ["teacher", "expert"]);
});
