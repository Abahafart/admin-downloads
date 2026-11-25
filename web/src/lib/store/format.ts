// Spanish number/size/time formatting (decimal comma, SI units like the
// designs: "4,2 GB", "18,4 MB/s", "1:24", "04:12").

const UNITS = ['B', 'KB', 'MB', 'GB', 'TB'];

function num(n: number, decimals: number): string {
  return n.toFixed(decimals).replace('.', ',');
}

/** 14 MB, 380 MB, 1,8 GB; "—" when unknown (< 0). */
export function formatBytes(n: number | null | undefined): string {
  if (n == null || n < 0 || !Number.isFinite(n)) return '—';
  let i = 0;
  let v = n;
  while (v >= 1000 && i < UNITS.length - 1) {
    v /= 1000;
    i++;
  }
  if (i === 0) return `${Math.round(v)} B`;
  return `${num(v, v < 10 ? 1 : 0)} ${UNITS[i]}`;
}

/** 18,4 MB/s; "—" for 0. */
export function formatSpeed(bps: number | null | undefined): string {
  if (!bps || bps <= 0) return '—';
  let i = 0;
  let v = bps;
  while (v >= 1000 && i < UNITS.length - 1) {
    v /= 1000;
    i++;
  }
  return `${num(v, i === 0 ? 0 : 1)} ${UNITS[i]}/s`;
}

const pad = (n: number) => String(n).padStart(2, '0');

/** Remaining time: 1:24 (m:ss) or 1:02:03; "" when unknown. */
export function formatETA(sec: number | null | undefined): string {
  if (sec == null || sec < 0 || !Number.isFinite(sec)) return '';
  const s = Math.round(sec);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  return h > 0 ? `${h}:${pad(m)}:${pad(s % 60)}` : `${m}:${pad(s % 60)}`;
}

/** Countdown to an RFC 3339 instant: 04:12 (mm:ss) or 1:04:12. */
export function formatCountdown(until: string | undefined, now: number): string {
  if (!until) return '';
  const t = Date.parse(until);
  if (Number.isNaN(t)) return '';
  const s = Math.max(0, Math.round((t - now) / 1000));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  return h > 0 ? `${h}:${pad(m)}:${pad(s % 60)}` : `${pad(m)}:${pad(s % 60)}`;
}

/** 0..100 integer, 0 when the size is unknown. */
export function percent(done: number, size: number): number {
  if (!size || size <= 0) return 0;
  return Math.max(0, Math.min(100, Math.floor((done / size) * 100)));
}

export function plural(n: number, one: string, many: string): string {
  return `${n} ${n === 1 ? one : many}`;
}

/** "hace 3 min" style relative time for checked_at fields. */
export function formatAgo(iso: string | undefined, now: number): string {
  if (!iso) return 'nunca';
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return 'nunca';
  const s = Math.max(0, Math.round((now - t) / 1000));
  if (s < 60) return 'hace un momento';
  if (s < 3600) return `hace ${Math.floor(s / 60)} min`;
  if (s < 86400) return `hace ${Math.floor(s / 3600)} h`;
  return `hace ${Math.floor(s / 86400)} d`;
}

export function formatDate(iso: string | undefined): string {
  if (!iso) return '';
  const t = new Date(iso);
  if (Number.isNaN(t.getTime())) return '';
  return t.toLocaleDateString('es-ES', { day: 'numeric', month: 'short', year: 'numeric' });
}
