// State -> always-textual label (PLAN.md 7.1: never color alone).
// Styles choose how to render `tone`; the text is the same everywhere.
import type { Download, Package, State } from '../api/types';
import { formatCountdown, formatETA } from './format';

export type Tone = 'accent' | 'warn' | 'err' | 'muted' | 'text' | 'paused' | 'done';
export type QuotaScope = 'ip' | 'account' | 'file' | '';

export interface StatusInfo {
  state: State;
  /** Full label: "Descargando · 1:24", "Cuota por IP · 04:12", "Fallida · cuota del archivo". */
  label: string;
  /** Label without the time/reason part: "Descargando", "Cuota por IP", "Fallida". */
  short: string;
  /** Time or reason after the dot ("1:24", "04:12", "cuota del archivo"). */
  detail: string;
  tone: Tone;
  /** Progress bar fill tone. */
  bar: Tone;
  quotaScope: QuotaScope;
  /** Drive per-file quota: rotating the IP does not help (show DriveQuotaHelp). */
  fileQuota: boolean;
  /** "no reanudable" badge. */
  notResumable: boolean;
  canPause: boolean;
  canResume: boolean;
  canRetry: boolean;
}

/**
 * The quota scope of a download: the backend's quota_scope (Download and
 * Progress). Only when it is missing on a quota error (older daemons) it
 * falls back to the hoster's usual scope: per file on Google Drive, per IP
 * elsewhere.
 */
export function quotaScopeOf(d: Pick<Download, 'error_kind' | 'quota_scope' | 'hoster'>): QuotaScope {
  const s = d.quota_scope;
  if (s === 'ip' || s === 'account' || s === 'file') return s;
  if (d.error_kind !== 'quota') return '';
  return d.hoster === 'gdrive' ? 'file' : 'ip';
}

const FAIL_REASON: Record<string, string> = {
  not_found: 'no encontrado',
  needs_auth: 'requiere cuenta',
  expired: 'enlace caducado',
  temporary: 'error de red',
  fatal: 'error',
};

const QUOTA_NAME: Record<Exclude<QuotaScope, ''>, string> = {
  ip: 'Cuota por IP',
  account: 'Cuota de la cuenta',
  file: 'Cuota del archivo',
};

function join(short: string, detail: string): string {
  return detail ? `${short} · ${detail}` : short;
}

export function statusOf(d: Download, now: number): StatusInfo {
  const scope = quotaScopeOf(d);
  const base = {
    state: d.state,
    quotaScope: scope,
    fileQuota: scope === 'file',
    notResumable: d.resumable === false,
    canPause: ['queued', 'resolving', 'waiting', 'downloading'].includes(d.state),
    canResume: d.state === 'paused',
    canRetry: d.state === 'failed',
  };
  let short = '';
  let detail = '';
  let tone: Tone = 'text';
  let bar: Tone = 'accent';
  switch (d.state) {
    case 'downloading':
      short = 'Descargando';
      detail = formatETA(d.eta);
      break;
    case 'queued':
      short = 'En cola';
      tone = 'muted';
      bar = 'muted';
      break;
    case 'resolving':
      short = 'Preparando';
      tone = 'muted';
      bar = 'muted';
      break;
    case 'waiting':
      short = scope ? QUOTA_NAME[scope] : 'En espera';
      detail = formatCountdown(d.waiting_until, now);
      tone = 'warn';
      bar = 'warn';
      break;
    case 'verifying':
      short = 'Verificando';
      break;
    case 'postprocessing':
      short = 'Extrayendo';
      break;
    case 'completed':
      short = 'Completada';
      tone = 'muted';
      bar = 'done';
      break;
    case 'paused':
      short = 'Pausada';
      tone = 'muted';
      bar = 'paused';
      break;
    case 'failed':
      short = 'Fallida';
      detail = d.error_kind === 'quota' ? (scope === 'file' ? 'cuota del archivo' : scope === 'account' ? 'cuota de la cuenta' : 'cuota') : (FAIL_REASON[d.error_kind ?? ''] ?? '');
      tone = 'err';
      bar = 'err';
      break;
  }
  return { ...base, short, detail, label: join(short, detail), tone, bar };
}

/** Aggregate label for a folder package (uses the package's own state/eta). */
export function packageStatusOf(p: Package, children: Download[], now: number): StatusInfo {
  const waiting = children.find((c) => c.state === 'waiting');
  const failed = children.find((c) => c.state === 'failed');
  const proxy: Download = {
    id: -p.id, package_id: p.id, hoster: p.hoster ?? '', url: p.source_url ?? '', name: p.name,
    dest_path: p.dest_dir, state: p.state, size: p.size, done: p.done, speed: p.speed, eta: p.eta,
    connections: 0, attempts: 0, priority: p.priority, created_at: p.created_at,
    error_kind: (waiting ?? failed)?.error_kind, error: (waiting ?? failed)?.error,
    quota_scope: (waiting ?? failed)?.quota_scope, waiting_until: waiting?.waiting_until,
  };
  const st = statusOf(proxy, now);
  if (p.state === 'downloading' && p.files_failed > 0) {
    st.detail = st.detail ? `${st.detail} · ${p.files_failed} fallidas` : `${p.files_failed} fallidas`;
    st.label = join(st.short, st.detail);
  }
  st.canRetry = p.files_failed > 0 || p.state === 'failed';
  st.notResumable = children.some((c) => c.resumable === false);
  return st;
}

/** Filter buckets used by the Descargas views (pill filters, tabs...). */
export type Bucket = 'todas' | 'activas' | 'espera' | 'pausadas' | 'fallidas' | 'completadas' | 'cola';

export function bucketOf(state: State): Exclude<Bucket, 'todas'> {
  switch (state) {
    case 'downloading':
    case 'resolving':
    case 'verifying':
    case 'postprocessing':
      return 'activas';
    case 'waiting':
      return 'espera';
    case 'paused':
      return 'pausadas';
    case 'failed':
      return 'fallidas';
    case 'completed':
      return 'completadas';
    default:
      return 'cola';
  }
}

export const BUCKETS: { id: Bucket; label: string }[] = [
  { id: 'todas', label: 'Todas' },
  { id: 'activas', label: 'Activas' },
  { id: 'espera', label: 'En espera' },
  { id: 'pausadas', label: 'Pausadas' },
  { id: 'fallidas', label: 'Fallidas' },
  { id: 'completadas', label: 'Completadas' },
];
