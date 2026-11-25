// View model shared by every DownloadsView: top-level items (folder
// packages and loose downloads), filter counts and the flattened rows.
import type { Download, Package, State } from '../api/types';
import { bucketOf, type Bucket } from './status';

export interface PackageItem {
  kind: 'package';
  key: string; // "p:<id>"
  pkg: Package;
  children: Download[];
}

export interface DownloadItem {
  kind: 'download';
  key: string; // "d:<id>"
  dl: Download;
  /** The folder package when this is a child row. */
  parent?: Package;
}

export type Item = PackageItem | DownloadItem;

export const pkey = (id: number) => `p:${id}`;
export const dkey = (id: number) => `d:${id}`;

export function parseKey(key: string | null | undefined): { kind: 'package' | 'download'; id: number } | null {
  const m = /^([pd]):(\d+)$/.exec(key ?? '');
  if (!m) return null;
  return { kind: m[1] === 'p' ? 'package' : 'download', id: Number(m[2]) };
}

const ORDER: Record<State, number> = {
  downloading: 0, verifying: 1, postprocessing: 1, resolving: 2, waiting: 3, paused: 4, failed: 5, queued: 6, completed: 7,
};

function childSort(a: Download, b: Download): number {
  return (a.rel_path || a.name).localeCompare(b.rel_path || b.name, 'es', { numeric: true });
}

function itemState(it: Item): State {
  return it.kind === 'package' ? it.pkg.state : it.dl.state;
}

function itemCreated(it: Item): string {
  return it.kind === 'package' ? it.pkg.created_at : it.dl.created_at;
}

/** All top-level items in display order (active first, completed last). */
export function buildItems(downloads: Record<number, Download>, packages: Record<number, Package>): Item[] {
  const byPkg = new Map<number, Download[]>();
  for (const d of Object.values(downloads)) {
    const list = byPkg.get(d.package_id);
    if (list) list.push(d);
    else byPkg.set(d.package_id, [d]);
  }
  const items: Item[] = [];
  for (const [pid, list] of byPkg) {
    const pkg = packages[pid];
    if (pkg?.is_folder) {
      items.push({ kind: 'package', key: pkey(pid), pkg, children: list.sort(childSort) });
    } else {
      for (const d of list) items.push({ kind: 'download', key: dkey(d.id), dl: d });
    }
  }
  // Folder packages without loaded downloads still show up.
  for (const pkg of Object.values(packages)) {
    if (pkg.is_folder && !byPkg.has(pkg.id)) items.push({ kind: 'package', key: pkey(pkg.id), pkg, children: [] });
  }
  items.sort((a, b) => ORDER[itemState(a)] - ORDER[itemState(b)] || itemCreated(a).localeCompare(itemCreated(b)) || a.key.localeCompare(b.key));
  return items;
}

export function countBuckets(items: Item[]): Record<Bucket, number> {
  const c: Record<Bucket, number> = { todas: 0, activas: 0, espera: 0, pausadas: 0, fallidas: 0, completadas: 0, cola: 0 };
  for (const it of items) {
    c.todas++;
    c[bucketOf(itemState(it))]++;
  }
  return c;
}

function matches(d: { name: string; rel_path?: string; hoster?: string }, q: string, hosterName: (h: string) => string): boolean {
  return (
    d.name.toLowerCase().includes(q) ||
    (d.rel_path ?? '').toLowerCase().includes(q) ||
    (d.hoster ? hosterName(d.hoster).toLowerCase().includes(q) || d.hoster.includes(q) : false)
  );
}

/**
 * Applies the bucket filter and the search text. A package matches if its
 * name/hoster matches (all children kept) or any child matches (only those
 * children kept).
 */
export function filterItems(items: Item[], bucket: Bucket, search: string, hosterName: (h: string) => string): Item[] {
  const q = search.trim().toLowerCase();
  const out: Item[] = [];
  for (const it of items) {
    if (bucket !== 'todas' && bucketOf(itemState(it)) !== bucket) continue;
    if (!q) {
      out.push(it);
      continue;
    }
    if (it.kind === 'download') {
      if (matches(it.dl, q, hosterName)) out.push(it);
      continue;
    }
    if (matches({ name: it.pkg.name, hoster: it.pkg.hoster }, q, hosterName)) {
      out.push(it);
      continue;
    }
    const kids = it.children.filter((c) => matches(c, q, hosterName));
    if (kids.length) out.push({ ...it, children: kids });
  }
  return out;
}

export interface Aggregate {
  size: number;
  done: number;
  speed: number;
  eta: number;
  filesTotal: number;
  filesDone: number;
  filesFailed: number;
  /** Exit used by most active children (or the first child's). */
  egressId: string;
}

/**
 * Live package totals computed from its children (progress events update
 * downloads, not packages); falls back to the package's own fields.
 */
export function aggregate(pkg: Package, children: Download[]): Aggregate {
  if (!children.length) {
    return { size: pkg.size, done: pkg.done, speed: pkg.speed, eta: pkg.eta, filesTotal: pkg.files_total, filesDone: pkg.files_done, filesFailed: pkg.files_failed, egressId: '' };
  }
  let size = 0;
  let done = 0;
  let speed = 0;
  let filesDone = 0;
  let filesFailed = 0;
  const eg = new Map<string, number>();
  for (const c of children) {
    if (c.size > 0) size += c.size;
    done += c.done;
    if (c.state === 'downloading') {
      speed += c.speed;
      if (c.egress_id) eg.set(c.egress_id, (eg.get(c.egress_id) ?? 0) + 1);
    }
    if (c.state === 'completed') filesDone++;
    if (c.state === 'failed') filesFailed++;
  }
  let egressId = children.find((c) => c.egress_id)?.egress_id ?? '';
  let max = 0;
  for (const [id, n] of eg) if (n > max) [egressId, max] = [id, n];
  return {
    size, done, speed, eta: speed > 0 ? Math.round((size - done) / speed) : -1,
    filesTotal: Math.max(children.length, pkg.files_total), filesDone, filesFailed, egressId,
  };
}
