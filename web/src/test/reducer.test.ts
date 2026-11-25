import { describe, expect, it } from 'vitest';
import { applyEvent, emptyData, replaceAll } from '../lib/store/reducer';
import type { Account, CoreEvent, Download, Package } from '../lib/api/types';

const dl = (id: number, p: Partial<Download> = {}): Download => ({
  id, package_id: 1, hoster: 'mega', url: 'u', name: `f${id}`, dest_path: '/x', state: 'queued', size: 100, done: 0, speed: 0, eta: -1,
  connections: 0, attempts: 0, priority: 0, created_at: '2026-01-01T00:00:00Z', ...p,
});
const pkg = (id: number, p: Partial<Package> = {}): Package => ({
  id, name: `p${id}`, dest_dir: '/x', is_folder: true, state: 'queued', size: 0, done: 0, speed: 0, eta: -1, files_total: 0, files_done: 0,
  files_failed: 0, priority: 0, exists_policy: 'skip', created_at: '2026-01-01T00:00:00Z', ...p,
});
const acc = (id: string, p: Partial<Account> = {}): Account => ({
  id, hoster: 'mega', label: id, kind: 'password', username: '', enabled: true, allow_rotation: false, status: 'ok', premium: false,
  quota_total: -1, quota_used: -1, created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z', ...p,
});
let seq = 0;
const ev = (e: Omit<CoreEvent, 'seq' | 'time'>, s = ++seq): CoreEvent => ({ seq: s, time: '2026-01-01T00:00:00Z', ...e }) as CoreEvent;

describe('applyEvent', () => {
  it('adds, updates and removes downloads', () => {
    const s = emptyData();
    seq = 0;
    applyEvent(s, ev({ type: 'download.added', download: dl(1) }));
    expect(s.downloads[1].state).toBe('queued');
    applyEvent(s, ev({ type: 'download.updated', download: dl(1, { state: 'paused' }) }));
    expect(s.downloads[1].state).toBe('paused');
    applyEvent(s, ev({ type: 'download.removed', id: 1 }));
    expect(s.downloads[1]).toBeUndefined();
  });

  it('applies progress batches in place (state, bytes, speed, egress, countdown)', () => {
    const s = emptyData();
    seq = 0;
    applyEvent(s, ev({ type: 'download.added', download: dl(1) }));
    const before = s.downloads[1];
    const fx = applyEvent(s, ev({ type: 'progress', progress: [{ id: 1, package_id: 1, state: 'downloading', done: 50, size: 100, speed: 10, eta: 5, connections: 4, egress_id: 'tor', public_ip: '1.2.3.4' }] }));
    expect(fx.resync).toBeFalsy();
    expect(s.downloads[1]).toBe(before); // same object: fine-grained updates
    expect(s.downloads[1]).toMatchObject({ state: 'downloading', done: 50, speed: 10, eta: 5, connections: 4, egress_id: 'tor', public_ip: '1.2.3.4' });
    applyEvent(s, ev({ type: 'progress', progress: [{ id: 1, package_id: 1, state: 'waiting', done: 50, size: 100, speed: 0, eta: -1, connections: 0, waiting_until: '2026-01-01T00:04:12Z' }] }));
    expect(s.downloads[1].waiting_until).toBe('2026-01-01T00:04:12Z');
    expect(s.downloads[1].egress_id).toBe('tor'); // omitted egress keeps the last one
  });

  it('asks for a resync on progress for an unknown download', () => {
    const s = emptyData();
    seq = 0;
    const fx = applyEvent(s, ev({ type: 'progress', progress: [{ id: 9, package_id: 1, state: 'downloading', done: 1, size: 2, speed: 1, eta: 1, connections: 1 }] }));
    expect(fx.resync).toBe(true);
  });

  it('detects seq gaps, restarts and ignores duplicates', () => {
    const s = emptyData();
    expect(applyEvent(s, ev({ type: 'stats' }, 5)).resync).toBeFalsy();
    expect(applyEvent(s, ev({ type: 'stats' }, 6)).resync).toBeFalsy();
    expect(applyEvent(s, ev({ type: 'download.added', download: dl(1) }, 6))).toEqual({}); // duplicate replay
    expect(s.downloads[1]).toBeUndefined();
    expect(applyEvent(s, ev({ type: 'stats' }, 9)).resync).toBe(true); // gap 7-8
    expect(applyEvent(s, ev({ type: 'stats' }, 1)).resync).toBe(true); // server restarted
    expect(s.lastSeq).toBe(1);
  });

  it('package.removed removes its downloads too', () => {
    const s = emptyData();
    seq = 0;
    applyEvent(s, ev({ type: 'package.added', package: pkg(1) }));
    applyEvent(s, ev({ type: 'download.added', download: dl(1) }));
    applyEvent(s, ev({ type: 'download.added', download: dl(2, { package_id: 2 }) }));
    applyEvent(s, ev({ type: 'package.removed', id: 1 }));
    expect(s.packages[1]).toBeUndefined();
    expect(Object.keys(s.downloads)).toEqual(['2']);
  });

  it('linkgrabber entries grow while resolving', () => {
    const s = emptyData();
    seq = 0;
    const g = { id: 3, url: 'u', state: 'resolving' as const, package_name: 'F', is_folder: true, files: [], total_size: -1, added_at: '' };
    applyEvent(s, ev({ type: 'linkgrabber.added', linkgrabber: g }));
    applyEvent(s, ev({ type: 'linkgrabber.updated', linkgrabber: { ...g, files: [{ index: 0, name: 'a', size: 1 }] } }));
    expect(s.grabber[3].files).toHaveLength(1);
    applyEvent(s, ev({ type: 'linkgrabber.removed', id: 3 }));
    expect(s.grabber[3]).toBeUndefined();
  });

  it('stats, egress, rotation and notices', () => {
    const s = emptyData();
    seq = 0;
    applyEvent(s, ev({ type: 'stats', stats: { speed: 5, active: 1, queued: 0, waiting: 0, failed: 0, completed: 0, grabber_size: 0, paused: true } }));
    expect(s.stats.paused).toBe(true);
    expect(applyEvent(s, ev({ type: 'egress.updated', egress_id: 'tor' })).refetchEgress).toBe(true);
    applyEvent(s, ev({ type: 'rotation', egress_id: 'tor', message: 'started' }));
    expect(s.rotating.tor).toBe(true);
    const fx = applyEvent(s, ev({ type: 'rotation', egress_id: 'tor', message: 'finished' }));
    expect(s.rotating.tor).toBeUndefined();
    expect(fx).toMatchObject({ refetchEgress: true, refetchIP: true });
    const n = applyEvent(s, ev({ type: 'notice', level: 'warning', message: 'Cuota' }));
    expect(n.notice?.message).toBe('Cuota');
    expect(s.notices).toHaveLength(1);
  });

  it('quota_scope follows progress: set while waiting, cleared once downloading', () => {
    const s = emptyData();
    seq = 0;
    applyEvent(s, ev({ type: 'download.added', download: dl(1, { state: 'downloading' }) }));
    const p = { id: 1, package_id: 1, done: 5, size: 100, speed: 0, eta: -1, connections: 0 };
    applyEvent(s, ev({ type: 'progress', progress: [{ ...p, state: 'waiting', quota_scope: 'account', waiting_until: '2026-01-01T00:01:00Z' }] }));
    expect(s.downloads[1].quota_scope).toBe('account');
    applyEvent(s, ev({ type: 'progress', progress: [{ ...p, state: 'waiting', waiting_until: '2026-01-01T00:01:00Z' }] }));
    expect(s.downloads[1].quota_scope).toBe('account'); // omitted while still waiting: keep
    applyEvent(s, ev({ type: 'progress', progress: [{ ...p, state: 'downloading', speed: 10 }] }));
    expect(s.downloads[1].quota_scope).toBeUndefined();
  });

  it('account.updated upserts and account.removed deletes, live', () => {
    const s = emptyData();
    seq = 0;
    applyEvent(s, ev({ type: 'account.updated', account_id: 'a', account: acc('a', { status: 'unknown' }) }));
    applyEvent(s, ev({ type: 'account.updated', account_id: 'b', account: acc('b') }));
    expect(Object.keys(s.accounts).sort()).toEqual(['a', 'b']);
    const fx = applyEvent(s, ev({ type: 'account.updated', account_id: 'a', account: acc('a', { status: 'quota', quota_used: 5 }) }));
    expect(fx).toEqual({});
    expect(s.accounts.a).toMatchObject({ status: 'quota', quota_used: 5 });
    applyEvent(s, ev({ type: 'account.removed', account_id: 'b' }));
    expect(s.accounts.b).toBeUndefined();
    applyEvent(s, ev({ type: 'account.removed', account_id: 'zzz' })); // unknown id: no-op
    applyEvent(s, ev({ type: 'account.updated' })); // no account: ignored
    expect(Object.keys(s.accounts)).toEqual(['a']);
  });

  it('secrets.updated flips the lock at once and asks to refetch the status', () => {
    const s = emptyData();
    seq = 0;
    // Status not loaded yet: only the refetch.
    expect(applyEvent(s, ev({ type: 'secrets.updated', message: 'locked' }))).toEqual({ refetchSecrets: true });
    s.secrets = { backend: 'vault', lockable: true, initialized: false, locked: true };
    const un = applyEvent(s, ev({ type: 'secrets.updated', message: 'unlocked' }));
    expect(un).toEqual({ refetchSecrets: true, refetchAccounts: true });
    expect(s.secrets).toMatchObject({ locked: false, initialized: true });
    const lk = applyEvent(s, ev({ type: 'secrets.updated', message: 'locked' }));
    expect(lk).toEqual({ refetchSecrets: true });
    expect(s.secrets.locked).toBe(true);
    // Unknown message: leave the state, refetch.
    expect(applyEvent(s, ev({ type: 'secrets.updated' })).refetchSecrets).toBe(true);
    expect(s.secrets.locked).toBe(true);
  });

  it('the API resync event asks for a full reload', () => {
    const s = emptyData();
    expect(applyEvent(s, { seq: 40, type: 'resync', time: '' }).resync).toBe(true);
  });

  it('replaceAll drops stale entries', () => {
    const t: Record<number, Download> = { 1: dl(1), 2: dl(2) };
    replaceAll(t, [dl(2, { state: 'paused' }), dl(3)]);
    expect(Object.keys(t)).toEqual(['2', '3']);
    expect(t[2].state).toBe('paused');
  });
});
