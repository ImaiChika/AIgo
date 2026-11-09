export function initialBatchJob(jobs, ownerId) {
  const own = jobs.filter(job => job.owner_id === ownerId);
  const running = job => ["validating", "in_progress", "finalizing", "cancelling"].includes(job.status);
  const complete = job => ["completed", "complete"].includes(job.status);
  return own.find(running) || own.find(job => complete(job) && !job.imported_at && job.tracked) || null;
}
