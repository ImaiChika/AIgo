import test from "node:test";
import assert from "node:assert/strict";
import {
  clearGenerationWorkspace,
  loadGenerationWorkspace,
  saveGenerationWorkspace,
} from "./generationWorkspaceState.js";

function memoryStorage() {
  const values = new Map();
  return {
    getItem: (key) => values.get(key) ?? null,
    setItem: (key, value) => values.set(key, value),
    removeItem: (key) => values.delete(key),
  };
}

test("generation workspace is isolated by user and survives reload", () => {
  const storage = memoryStorage();
  assert.equal(saveGenerationWorkspace("user-a", { runId: "gen-1", generatedQuestions: [{ id: "q-1" }] }, storage), true);
  assert.equal(loadGenerationWorkspace("user-a", storage).runId, "gen-1");
  assert.equal(loadGenerationWorkspace("user-b", storage), null);
});

test("clearing a workspace only removes the selected user", () => {
  const storage = memoryStorage();
  saveGenerationWorkspace("user-a", { runId: "gen-a" }, storage);
  saveGenerationWorkspace("user-b", { runId: "gen-b" }, storage);
  clearGenerationWorkspace("user-a", storage);
  assert.equal(loadGenerationWorkspace("user-a", storage), null);
  assert.equal(loadGenerationWorkspace("user-b", storage).runId, "gen-b");
});
