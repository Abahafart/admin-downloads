// Pure event reducer: applies core.Event messages to the data state. It
// mutates the given object in place (works on Svelte $state proxies and on
// plain objects in tests) and returns the side effects the store must run.
import type { Account, CoreEvent, Download, EgressStatus, GrabberEntry, NoticeLevel, Package, SecretsStatus, Stats } from '../api/types';

export interface Notice {
  id: number;
  level: NoticeLevel;
  message: string;
  time: string;
}

export interface DataState {
  downloads: Record<number, Download>;
  packages: Record<number, Package>;
  grabber: Record<number, GrabberEntry>;
  egress: Record<string, EgressStatus>;
  /** Accounts by id (GET /accounts + account.* events). */
  accounts: Record<string, Account>;
  /** Secret store status (GET /secrets); null until loaded or when the API has none (501). */
  secrets: SecretsStatus | null;
  stats: Stats;
  /** Egress ids with a rotation in progress ("" = global rotation). */
  rotating: Record<string, boolean>;
  notices: Notice[];
  lastSeq: number;
}

export interface Effects {
  /** Seq gap or server restart: reload every snapshot. */
  resync?: boolean;
  /** egress.updated carries only the id: reload GET /egress. */
  refetchEgress?: boolean;
  /** rotation finished: refresh the public IP. */
  refetchIP?: boolean;
  /** secrets.updated: reload GET /secrets (the event only says locked/unlocked). */
  refetchSecrets?: boolean;
  /** The vault was unlocked: account statuses may change (re-read GET /accounts). */
  refetchAccounts?: boolean;
  notice?: Notice;
}

export function emptyStats(): Stats {
  return { speed: 0, active: 0, queued: 0, waiting: 0, failed: 0, completed: 0, grabber_size: 0, paused: false };
}

export function emptyData(): DataState {
  return {
    downloads: {}, packages: {}, grabber: {}, egress: {}, accounts: {}, secrets: null, stats: emptyStats(), rotating: {}, notices: [], lastSeq: 0,
  };
}

const MAX_NOTICES = 50;

export function applyEvent(s: DataState, ev: CoreEvent): Effects {
  const fx: Effects = {};
  if (typeof ev.seq === 'number' && ev.seq > 0) {
    if (s.lastSeq > 0 && ev.seq === s.lastSeq) return fx; // duplicate (replay)
    if (s.lastSeq > 0 && (ev.seq > s.lastSeq + 1 || ev.seq < s.lastSeq)) fx.resync = true;
    s.lastSeq = ev.seq;
  }
  switch (ev.type) {
    case 'download.added':
    case 'download.updated':
      if (ev.download) s.downloads[ev.download.id] = ev.download;
      break;
    case 'download.removed': {
      const id = ev.id ?? ev.download?.id;
      if (id != null) delete s.downloads[id];
      break;
    }
    case 'progress':
      for (const p of ev.progress ?? []) {
        const d = s.downloads[p.id];
        if (!d) {
          fx.resync = true; // progress for an unknown download: we missed its "added"
          continue;
        }
        d.state = p.state;
        d.done = p.done;
        d.size = p.size;
        d.speed = p.speed;
        d.eta = p.eta;
        d.connections = p.connections;
        if (p.egress_id !== undefined) d.egress_id = p.egress_id;
        if (p.public_ip !== undefined) d.public_ip = p.public_ip;
        d.waiting_until = p.waiting_until;
        // The scope comes with every progress while waiting; any other active
        // state means the quota is over. failed keeps download.updated's value.
        if (p.quota_scope) d.quota_scope = p.quota_scope;
        else if (p.state !== 'waiting' && p.state !== 'failed') d.quota_scope = undefined;
      }
      break;
    case 'package.added':
    case 'package.updated':
      if (ev.package) s.packages[ev.package.id] = ev.package;
      break;
    case 'package.removed': {
      const id = ev.id ?? ev.package?.id;
      if (id != null) {
        delete s.packages[id];
        for (const d of Object.values(s.downloads)) if (d.package_id === id) delete s.downloads[d.id];
      }
      break;
    }
    case 'linkgrabber.added':
    case 'linkgrabber.updated':
      if (ev.linkgrabber) s.grabber[ev.linkgrabber.id] = ev.linkgrabber;
      break;
    case 'linkgrabber.removed': {
      const id = ev.id ?? ev.linkgrabber?.id;
      if (id != null) delete s.grabber[id];
      break;
    }
    case 'stats':
      if (ev.stats) s.stats = ev.stats;
      break;
    case 'egress.updated':
      fx.refetchEgress = true;
      break;
    case 'rotation': {
      const id = ev.egress_id ?? '';
      const msg = (ev.message ?? '').toLowerCase();
      if (msg.startsWith('start')) s.rotating[id] = true;
      else {
        delete s.rotating[id];
        fx.refetchEgress = true;
        fx.refetchIP = true;
      }
      break;
    }
    case 'account.updated':
      if (ev.account?.id) s.accounts[ev.account.id] = ev.account;
      break;
    case 'account.removed': {
      const id = ev.account_id ?? ev.account?.id;
      if (id) delete s.accounts[id];
      break;
    }
    case 'secrets.updated': {
      const m = (ev.message ?? '').toLowerCase();
      // Apply the known part right away (banner), then refetch the details.
      if (s.secrets && (m === 'locked' || m === 'unlocked')) {
        s.secrets.locked = m === 'locked';
        if (m === 'unlocked') s.secrets.initialized = true;
      }
      fx.refetchSecrets = true;
      if (m !== 'locked') fx.refetchAccounts = true;
      break;
    }
    case 'resync':
      fx.resync = true;
      break;
    case 'notice': {
      const n: Notice = { id: ev.seq || Date.now(), level: ev.level ?? 'info', message: ev.message ?? '', time: ev.time };
      s.notices.push(n);
      if (s.notices.length > MAX_NOTICES) s.notices.splice(0, s.notices.length - MAX_NOTICES);
      fx.notice = n;
      break;
    }
  }
  return fx;
}

/** Replaces a snapshot collection (after GET /downloads etc.). */
export function replaceAll<T extends { id: number | string }>(target: Record<string | number, T>, items: T[] | null | undefined): void {
  const seen = new Set<string>();
  for (const it of items ?? []) {
    seen.add(String(it.id));
    target[it.id] = it;
  }
  for (const k of Object.keys(target)) if (!seen.has(k)) delete target[k];
}
