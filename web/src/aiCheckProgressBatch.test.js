import test from "node:test";
import assert from "node:assert/strict";
import { splitQuestionIds, combineProgressResponses } from "./aiCheckProgressBatch.js";

test("large task progress is split within the API limit and recombined", () => {
  const ids = Array.from({ length: 1201 }, (_, i) => `q-${i}`);
  const chunks = splitQuestionIds(ids);
  assert.deepEqual(chunks.map(chunk => chunk.length), [500, 500, 201]);
  const combined = combineProgressResponses([
    { total: 500, counts: { passed: 400, running: 100 }, items: [{ question_id: "a" }] },
    { total: 500, counts: { passed: 500 }, items: [{ question_id: "b" }] },
    { total: 201, counts: { discarded: 1, passed: 200 }, items: [{ question_id: "c" }] },
  ]);
  assert.equal(combined.total, 1201);
  assert.deepEqual(combined.counts, { passed: 1100, running: 100, discarded: 1 });
  assert.equal(combined.items.length, 3);
});
