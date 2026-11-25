// Pure view helpers shared by the Terminal, Medianoche, Monocromo and
// Enfoque styles. They only reshape what the store already exposes (items,
// status, aggregates); no state and no API access here.
import type { Download, EgressStatus, Package } from '../../lib/api/types';
import { aggregate, dkey, type Item } from '../../lib/store/rows';
import { packageStatusOf, statusOf, type StatusInfo } from '../../lib/store/status';
import { formatBytes, formatSpeed, percent, plural } from '../../lib/store/format';
import { egressName, kindFromId } from '../../lib/store/labels';
import type { Bucket } from '../../lib/store/status';

/** One row/card of a Descargas view: a folder package or a single download. */
export interface RowVM {
  key: string;
  kind: 'package' | 'download';
  name: string;
  hoster: string;
  size: number;
  done: number;
  speed: number;
  eta: number;
  pct: number;
  st: StatusInfo;
  egressId: string;
  filesTotal: number;
  children: Download[];
  /** Package id (for packages and for downloads, their package). */
  packageId: number;
  dl?: Download;
  pkg?: Package;
  /** Error text of the download (or of the failed/waiting child). */
  error: string;
  errorKind: string;
  connections: number;
}

export function downloadVM(d: Download, now: number): RowVM {
  return {
    key: dkey(d.id), kind: 'download', name: d.name, hoster: d.hoster, size: d.size, done: d.done, speed: d.state === 'downloading' ? d.speed : 0,
    eta: d.eta, pct: d.state === 'completed' ? 100 : percent(d.done, d.size), st: statusOf(d, now), egressId: d.egress_id ?? '',
    filesTotal: 1, children: [], packageId: d.package_id, dl: d, error: d.error ?? '', errorKind: d.error_kind ?? '', connections: d.connections,
  };
}

export function itemVM(it: Item, now: number): RowVM {
  if (it.kind === 'download') return downloadVM(it.dl, now);
  const ag = aggregate(it.pkg, it.children);
  const st = packageStatusOf({ ...it.pkg, eta: ag.eta, speed: ag.speed }, it.children, now);
  const bad = it.children.find((c) => c.state === 'waiting') ?? it.children.find((c) => c.state === 'failed');
  return {
    key: it.key, kind: 'package', name: it.pkg.name, hoster: it.pkg.hoster ?? '', size: ag.size, done: ag.done, speed: ag.speed, eta: ag.eta,
    pct: it.pkg.state === 'completed' ? 100 : percent(ag.done, ag.size), st, egressId: ag.egressId, filesTotal: ag.filesTotal,
    children: it.children, packageId: it.pkg.id, pkg: it.pkg, error: bad?.error ?? '', errorKind: bad?.error_kind ?? '',
    connections: it.children.reduce((n, c) => n + (c.state === 'downloading' ? c.connections : 0), 0),
  };
}

/** Item first shown big in Enfoque: downloading, else waiting/verifying. */
export function focusItem(items: RowVM[]): RowVM | undefined {
  return (
    items.find((v) => v.st.state === 'downloading') ??
    items.find((v) => v.st.state === 'verifying' || v.st.state === 'postprocessing') ??
    items.find((v) => v.st.state === 'waiting' || v.st.state === 'resolving')
  );
}

// ---------- Terminal

const UNIT = ['B', 'K', 'M', 'G', 'T'];

function scale(n: number): [number, string] {
  let i = 0;
  let v = n;
  while (v >= 1000 && i < UNIT.length - 1) {
    v /= 1000;
    i++;
  }
  return [v, UNIT[i]];
}

/** CLI sizes like the design: 4.2G, 380M, 14M ("—" when unknown). */
export function compactBytes(n: number | null | undefined): string {
  if (n == null || n < 0 || !Number.isFinite(n)) return '—';
  const [v, u] = scale(n);
  if (u === 'B') return `${Math.round(v)}B`;
  return `${v < 10 ? v.toFixed(1) : Math.round(v)}${u}`;
}

/** CLI speeds: 18.4M/s, 850K/s ("—" when idle). */
export function compactSpeed(bps: number | null | undefined): string {
  if (!bps || bps <= 0) return '—';
  const [v, u] = scale(bps);
  return `${v < 100 && u !== 'B' ? v.toFixed(1) : Math.round(v)}${u}/s`;
}

/** Character progress bar: 62% over 19 cells = 12 "█" + 7 "░". */
export function charBar(pct: number, width = 19): { fill: string; empty: string } {
  const p = Math.max(0, Math.min(100, pct));
  const n = Math.max(0, Math.min(width, Math.round((p * width) / 100)));
  return { fill: '█'.repeat(n), empty: '░'.repeat(width - n) };
}

/** Percentage right-aligned in 4 cells: "  0%", " 62%", "100%". */
export function padPct(pct: number): string {
  return `${Math.round(pct)}%`.padStart(4, ' ');
}

/** Status glyph of the Terminal "st" column (text, aria-hidden there). */
export function glyph(state: StatusInfo['state']): string {
  switch (state) {
    case 'downloading':
    case 'verifying':
    case 'postprocessing':
      return '▸';
    case 'waiting':
      return '◷';
    case 'paused':
      return '‖';
    case 'failed':
      return '✕';
    case 'completed':
      return '✓';
    default:
      return '·';
  }
}

const SHORT_KIND: Record<string, string> = {
  direct: 'directa', tor: 'tor', wireguard: 'wg', socks5: 'socks5', http: 'http', router: 'router', systemvpn: 'vpn',
};

/** Short lowercase exit for Terminal: directa, tor, wg, socks5… */
export function shortEgress(id: string | undefined, list: Record<string, EgressStatus>, rotating: Record<string, boolean> = {}): string {
  if (!id) return '';
  const kind = list[id]?.kind ?? kindFromId(id);
  const name = kind ? SHORT_KIND[kind] ?? kind : id;
  return rotating[id] || rotating[''] ? `${name}→rotando` : name;
}

/** Terminal state text: "eta 1:24", "cuota ip 04:12", "pausada", "en cola"… */
export function termState(st: StatusInfo): string {
  switch (st.state) {
    case 'downloading':
      return st.detail ? `eta ${st.detail}` : 'descargando';
    case 'waiting': {
      const q = st.quotaScope === 'ip' ? 'cuota ip' : st.quotaScope === 'account' ? 'cuota cuenta' : st.quotaScope === 'file' ? 'cuota archivo' : 'espera';
      return st.detail ? `${q} ${st.detail}` : q;
    }
    case 'failed':
      return st.detail ? `fallida: ${st.detail}` : 'fallida';
    default:
      return st.short.toLowerCase();
  }
}

// ---------- Medianoche / Enfoque / Monocromo sentences

/** Exit as part of a sentence: "directa", "Tor", "proxy SOCKS5", "WireGuard". */
export function egressPhrase(id: string | undefined, list: Record<string, EgressStatus>): string {
  const n = egressName(id, list);
  if (n === 'Directa') return 'directa';
  if (n.startsWith('Proxy ')) return 'proxy ' + n.slice(6);
  return n;
}

/** "2,6" of "2,6 de 4,2 GB": the done amount in the unit of the total. */
export function doneOf(done: number, size: number): { done: string; total: string } {
  const total = formatBytes(size);
  const unit = total.split(' ')[1];
  const pow = { B: 0, KB: 1, MB: 2, GB: 3, TB: 4 }[unit as 'B'] ?? 0;
  const v = done / 1000 ** pow;
  const d = (v < 10 && pow > 0 ? v.toFixed(1) : String(Math.round(v))).replace('.', ',');
  return { done: d, total };
}

/** One sentence explaining why a download failed and what to do. */
export function failSentence(vm: RowVM, hasDrive: boolean): string {
  if (vm.st.fileQuota) {
    return hasDrive
      ? 'Cuota del archivo excedida. Cambiar de IP no ayuda: reintenta y se copiará a tu cuenta de Drive.'
      : 'Cuota del archivo excedida. Cambiar de IP no ayuda: conecta una cuenta de Drive para copiarlo.';
  }
  if (vm.st.quotaScope === 'account') return 'La cuenta agotó su cuota de hoy. Espera o añade otra cuenta en Cuentas.';
  if (vm.st.quotaScope === 'ip') return 'El hoster limitó esta IP. Reintenta y se rotará la salida.';
  switch (vm.errorKind) {
    case 'not_found':
      return 'El archivo ya no existe en el hoster: el enlace está roto o lo borraron.';
    case 'needs_auth':
      return 'Este archivo solo se descarga con una cuenta: añádela en Cuentas.';
    case 'expired':
      return 'El enlace caducó: vuelve a copiarlo desde la página del hoster.';
    case 'temporary':
      return 'La conexión falló varias veces seguidas. Reintenta en un rato.';
  }
  return vm.error ? `Falló: ${vm.error}` : 'La descarga falló. Reintenta para volver a intentarlo.';
}

/** Waiting sentence: "Cuota por IP agotada · rotando circuito". */
export function waitSentence(vm: RowVM, list: Record<string, EgressStatus>, rotating: Record<string, boolean>): string {
  const scope = vm.st.quotaScope;
  const what = scope === 'ip' ? 'Cuota por IP agotada' : scope === 'account' ? 'Cuota de la cuenta agotada' : scope === 'file' ? 'Cuota del archivo agotada' : 'En espera';
  if (scope === 'file') return `${what} · rotar la IP no ayuda`;
  const kind = list[vm.egressId]?.kind ?? kindFromId(vm.egressId);
  if (rotating[vm.egressId] || rotating['']) return `${what} · ${kind === 'tor' ? 'rotando circuito' : 'rotando la IP'}`;
  return scope === 'ip' ? `${what} · reintenta al acabar la cuenta atrás` : what;
}

/** Remaining time in words: "1 min 24 s", "45 s", "1 h 2 min" ("—" unknown). */
export function humanDuration(sec: number | null | undefined): string {
  if (sec == null || sec < 0 || !Number.isFinite(sec)) return '—';
  const s = Math.round(sec);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (h > 0) return m ? `${h} h ${m} min` : `${h} h`;
  if (m > 0) return `${m} min ${s % 60} s`;
  return `${s} s`;
}

/** "24,5 MB/s" -> { num: "24,5", unit: "MB/s" } (0 MB/s when idle). */
export function splitSpeed(bps: number): { num: string; unit: string } {
  const s = formatSpeed(bps);
  if (s === '—') return { num: '0', unit: 'MB/s' };
  const i = s.lastIndexOf(' ');
  return { num: s.slice(0, i), unit: s.slice(i + 1) };
}

/** "2 activas · 1 en espera · 3 detenidas" (zeros left out). */
export function ongoingSummary(c: Record<Bucket, number>): string {
  const stopped = c.pausadas + c.fallidas + c.cola;
  const parts: string[] = [];
  if (c.activas) parts.push(`${c.activas} ${c.activas === 1 ? 'activa' : 'activas'}`);
  if (c.espera) parts.push(`${c.espera} en espera`);
  if (stopped) parts.push(`${stopped} ${stopped === 1 ? 'detenida' : 'detenidas'}`);
  return parts.join(' · ') || 'Nada en curso';
}

/** "carpeta · 12 archivos" for packages. */
export function filesPhrase(n: number): string {
  return plural(n, 'archivo', 'archivos');
}
