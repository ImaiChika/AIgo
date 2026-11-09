// Display in the viewer's local time, with a stable Chinese-interface format
// independent of the browser's default language. Backend timestamps stay UTC.
export function formatDateTime(value, fallback = '—') {
  if (value == null || value === '' || String(value).startsWith('0001-')) return fallback;
  const date = value instanceof Date ? value : new Date(value);
  if (!Number.isFinite(date.getTime())) return fallback;
  const pad = n => String(n).padStart(2, '0');
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`;
}
