import test from "node:test";
import assert from "node:assert/strict";
import { initialBatchJob } from "./batchHistory.js";

test("opening batch page leaves imported history unselected", () => {
  assert.equal(initialBatchJob([{ owner_id: "me", status: "completed", imported_at: "yesterday", tracked: true }], "me"), null);
});

test("opening batch page resumes own running job before older incomplete imports", () => {
  const jobs = [
    { job_id: "older", owner_id: "me", status: "completed", tracked: true },
    { job_id: "someone", owner_id: "other", status: "in_progress" },
    { job_id: "running", owner_id: "me", status: "in_progress" },
  ];
  assert.equal(initialBatchJob(jobs, "me")?.job_id, "running");
  assert.equal(initialBatchJob(jobs.slice(0, 2), "me")?.job_id, "older");
});
