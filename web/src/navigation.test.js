import test from "node:test";
import assert from "node:assert/strict";
import { navigationGroups, visibleNavigation, navigationItemActive, permissionAllowed, generationRouteRedirect } from "./navigation.js";

const all = () => true;
const permissions = (...values) => value => values.includes(value);

test("all existing destinations belong to six distinct groups; batch shares the generation entry", () => {
  const groups = visibleNavigation(all);
  assert.equal(groups.length, 6);
  const paths = groups.flatMap(g => g.items.map(i => i.path));
  assert.equal(new Set(paths).size, 17);
  assert.deepEqual(new Set(paths), new Set(["/my", "/settings", "/knowledge", "/generate", "/my-revisions", "/share-requests", "/review", "/review-decisions", "/review-results", "/review-flows", "/bank", "/banks", "/stats", "/users", "/roles", "/system/ai-providers", "/audit"]));
  const generate = groups.flatMap(g => g.items).find(i => i.id === "generate");
  for (const path of ["/generate", "/generate/batch", "/batch"]) assert.equal(navigationItemActive(generate, path), true);
  assert.equal(navigationItemActive(generate, "/bank"), false);
});

test("permission filtering removes inaccessible children and empty categories", () => {
  assert.deepEqual(visibleNavigation(() => false).map(g => g.id), ["personal", "data"]);
  const reviewer = visibleNavigation(permissions("review:do"));
  assert.deepEqual(reviewer.map(g => g.id), ["personal", "data", "review"]);
  assert.deepEqual(reviewer.find(g => g.id === "review").items.map(i => i.id), ["review"]);
  const finalReviewer = visibleNavigation(permissions("review:final"));
  assert.deepEqual(finalReviewer.find(g => g.id === "review").items.map(i => i.id), ["review-decisions"]);
  assert.equal(permissionAllowed(["review:do", "review:final"], permissions("review:final")), true);
});

test("medical experts keep personal authoring and bank pages without management summaries", () => {
  const expert = visibleNavigation(permissions(
    "question:view", "question:edit", "question:generate", "question:share", "review:do",
  ));
  const ids = expert.flatMap(group => group.items.map(item => item.id));
  for (const expected of ["my-dashboard", "personal-settings", "knowledge", "generate", "my-revisions", "share-requests", "review", "bank"]) {
    assert.equal(ids.includes(expected), true, `missing expert navigation item ${expected}`);
  }
  for (const forbidden of ["review-results", "stats", "banks", "users", "roles", "audit"]) {
    assert.equal(ids.includes(forbidden), false, `expert should not see ${forbidden}`);
  }
});

test("single and batch permissions have explicit navigation behavior", () => {
  const singleOnly = permissions("question:generate");
  assert.deepEqual(generationRouteRedirect("generation-single", singleOnly), null);
  assert.deepEqual(generationRouteRedirect("generation-batch", singleOnly), {
    path: "/generate", query: { notice: "batch-permission" },
  });
  assert.equal(visibleNavigation(singleOnly).find(g => g.id === "authoring").items[0].path, "/generate");

  const batchOnly = permissions("batch:run");
  assert.equal(generationRouteRedirect("generation-single", batchOnly), "/generate/batch");
  assert.equal(generationRouteRedirect("generation-batch", batchOnly), null);
  assert.equal(visibleNavigation(batchOnly).find(g => g.id === "authoring").items[0].path, "/generate/batch");

  const neither = permissions();
  assert.equal(generationRouteRedirect("generation-batch", neither), "/my");
});

test("batch-only accounts retain a valid generation destination without gaining single generation permission", () => {
  const batchOnly = visibleNavigation(permissions("batch:run"));
  assert.equal(batchOnly.find(g => g.id === "authoring").items[0].path, "/generate/batch");
  assert.equal(permissionAllowed("question:generate", permissions("batch:run")), false);
  assert.equal(visibleNavigation(permissions("question:generate")).find(g => g.id === "authoring").items[0].path, "/generate");
  assert.equal(navigationGroups.find(g => g.id === "authoring").items[0].path, "/generate", "filtering must not mutate the shared definition");
});
