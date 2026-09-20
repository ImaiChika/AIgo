import test from "node:test";
import assert from "node:assert/strict";
import { navigationGroups, visibleNavigation, navigationItemActive, permissionAllowed, generationRouteRedirect } from "./navigation.js";

const all = () => true;
const permissions = (...values) => value => values.includes(value);

test("all existing destinations belong to five distinct groups; batch shares the generation entry", () => {
  const groups = visibleNavigation(all);
  assert.equal(groups.length, 5);
  const paths = groups.flatMap(g => g.items.map(i => i.path));
  assert.equal(new Set(paths).size, 17);
  assert.deepEqual(new Set(paths), new Set(["/my", "/settings", "/knowledge", "/generate", "/new-questions", "/my-revisions", "/share-requests", "/review", "/review-decisions", "/review-results", "/review-flows", "/bank", "/stats", "/users", "/roles", "/system/ai-providers", "/audit"]));
  const generate = groups.flatMap(g => g.items).find(i => i.id === "generate");
  for (const path of ["/generate", "/generate/batch", "/batch"]) assert.equal(navigationItemActive(generate, path), true);
  assert.equal(navigationItemActive(generate, "/bank"), false);
});

test("permission filtering removes inaccessible children and empty categories", () => {
  assert.deepEqual(visibleNavigation(() => false).map(g => g.id), ["personal", "authoring"]);
  const reviewer = visibleNavigation(permissions("review:do"));
  assert.deepEqual(reviewer.map(g => g.id), ["personal", "authoring", "review"]);
  assert.deepEqual(reviewer.find(g => g.id === "review").items.map(i => i.id), ["review"]);
  const finalReviewer = visibleNavigation(permissions("review:final"));
  assert.deepEqual(finalReviewer.find(g => g.id === "review").items.map(i => i.id), ["review-decisions"]);
  assert.equal(permissionAllowed(["review:do", "review:final"], permissions("review:final")), true);
});

test("teacher and reviewer identities expose mutually exclusive work navigation", () => {
  // 数据统计与题库同口径（任一题库分层查看权限即可见）；审题老师另有
  // review:view_results 查看“我的审核记录”。命题与送审入口仍互斥。
  const reviewerIds = visibleNavigation(permissions("question:view", "review:do", "review:view_results"))
    .flatMap(group => group.items.map(item => item.id));
  for (const expected of ["my-dashboard", "personal-settings", "knowledge", "review", "review-results", "bank", "stats"]) {
    assert.equal(reviewerIds.includes(expected), true, `missing reviewer navigation item ${expected}`);
  }
  for (const forbidden of ["generate", "new-questions", "my-revisions"]) {
    assert.equal(reviewerIds.includes(forbidden), false, `reviewer should not see ${forbidden}`);
  }

  const teacherIds = visibleNavigation(permissions(
    "question:view", "question:edit", "question:generate", "batch:run", "question:share", "review:submit",
  )).flatMap(group => group.items.map(item => item.id));
  for (const expected of ["generate", "new-questions", "my-revisions", "bank"]) assert.equal(teacherIds.includes(expected), true);
  assert.equal(teacherIds.includes("review"), false);
  assert.equal(visibleNavigation(permissions("question:generate")).flatMap(g => g.items).some(i => i.id === "new-questions"), false);
});

test("single and batch permissions have explicit navigation behavior", () => {
  const singleOnly = permissions("question:generate");
  assert.deepEqual(generationRouteRedirect("generation-single", singleOnly), null);
  assert.deepEqual(generationRouteRedirect("generation-batch", singleOnly), {
    path: "/generate", query: { notice: "batch-permission" },
  });
  assert.equal(visibleNavigation(singleOnly).find(g => g.id === "authoring").items.find(i => i.id === "generate").path, "/generate");

  const batchOnly = permissions("batch:run");
  assert.equal(generationRouteRedirect("generation-single", batchOnly), "/generate/batch");
  assert.equal(generationRouteRedirect("generation-batch", batchOnly), null);
  assert.equal(visibleNavigation(batchOnly).find(g => g.id === "authoring").items.find(i => i.id === "generate").path, "/generate/batch");

  const neither = permissions();
  assert.equal(generationRouteRedirect("generation-batch", neither), "/my");
});

test("batch-only accounts retain a valid generation destination without gaining single generation permission", () => {
  const batchOnly = visibleNavigation(permissions("batch:run"));
  assert.equal(batchOnly.find(g => g.id === "authoring").items.find(i => i.id === "generate").path, "/generate/batch");
  assert.equal(permissionAllowed("question:generate", permissions("batch:run")), false);
  assert.equal(visibleNavigation(permissions("question:generate")).find(g => g.id === "authoring").items.find(i => i.id === "generate").path, "/generate");
  assert.equal(navigationGroups.find(g => g.id === "authoring").items.find(i => i.id === "generate").path, "/generate", "filtering must not mutate the shared definition");
});
