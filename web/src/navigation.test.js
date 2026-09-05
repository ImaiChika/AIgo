import test from "node:test";
import assert from "node:assert/strict";
import { navigationGroups, visibleNavigation, navigationItemActive, permissionAllowed } from "./navigation.js";

const all = () => true;
const permissions = (...values) => value => values.includes(value);

test("all existing destinations belong to five distinct groups; batch shares the generation entry", () => {
  const groups = visibleNavigation(all);
  assert.equal(groups.length, 5);
  const paths = groups.flatMap(g => g.items.map(i => i.path));
  assert.equal(new Set(paths).size, 13);
  assert.deepEqual(new Set(paths), new Set(["/knowledge", "/generate", "/ai-check", "/review", "/review-decisions", "/review-results", "/review-flows", "/bank", "/banks", "/stats", "/users", "/roles", "/audit"]));
  const generate = groups.flatMap(g => g.items).find(i => i.id === "generate");
  for (const path of ["/generate", "/generate/batch", "/batch"]) assert.equal(navigationItemActive(generate, path), true);
  assert.equal(navigationItemActive(generate, "/bank"), false);
});

test("permission filtering removes inaccessible children and empty categories", () => {
  assert.deepEqual(visibleNavigation(() => false).map(g => g.id), ["data"]);
  const reviewer = visibleNavigation(permissions("review:do"));
  assert.deepEqual(reviewer.map(g => g.id), ["data", "review"]);
  assert.deepEqual(reviewer[1].items.map(i => i.id), ["review", "review-results"]);
  assert.equal(permissionAllowed(["review:do", "review:final"], permissions("review:final")), true);
});

test("batch-only accounts retain a valid generation destination without gaining single generation permission", () => {
  const batchOnly = visibleNavigation(permissions("batch:run"));
  assert.equal(batchOnly.find(g => g.id === "authoring").items[0].path, "/generate/batch");
  assert.equal(permissionAllowed("question:generate", permissions("batch:run")), false);
  assert.equal(visibleNavigation(permissions("question:generate")).find(g => g.id === "authoring").items[0].path, "/generate");
  assert.equal(navigationGroups.find(g => g.id === "authoring").items[0].path, "/generate", "filtering must not mutate the shared definition");
});
