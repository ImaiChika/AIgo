export function completionMode(value) {
  return value?.completion_mode || 'external_final';
}

// Mode and round entries come from the submitted task snapshot, never a later
// edited flow. Older payloads without enough round metadata keep a generic label.
export function reviewCompletionLabel(value, total = 0) {
  const rounds = Number(total) || Number(value?.round_count) || value?.rounds?.length || value?.round_results?.length || 0;
  const prefix = rounds > 0 ? `${rounds}轮` : '';
  if (completionMode(value) === 'on_last_round') return `${prefix}末轮完成`;
  if (completionMode(value) === 'external_final') return `${prefix || '轮审'}审核+最终决断`.replace('轮审审核', '轮审');
  return '未知完成方式（请核对流程）';
}
