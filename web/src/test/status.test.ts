// The texts of the designs: a state is never shown by color alone.
import { describe, expect, it } from 'vitest';
import { packageStatusOf, quotaScopeOf, statusOf } from '../lib/store/status';
import { formatBytes, formatCountdown, formatETA, formatSpeed, percent } from '../lib/store/format';
import type { Download, Package } from '../lib/api/types';

const NOW = Date.parse('2026-11-24T12:00:00Z');
const d = (p: Partial<Download>): Download => ({
  id: 1, package_id: 1, hoster: 'mega', url: '', name: 'x', dest_path: '', state: 'queued', size: 100, done: 0, speed: 0, eta: -1,
  connections: 0, attempts: 0, priority: 0, created_at: '', ...p,
});

describe('statusOf', () => {
  it('matches the design labels', () => {
    expect(statusOf(d({ state: 'downloading', eta: 84 }), NOW).label).toBe('Descargando · 1:24');
    expect(statusOf(d({ state: 'queued' }), NOW).label).toBe('En cola');
    expect(statusOf(d({ state: 'paused' }), NOW).label).toBe('Pausada');
    expect(statusOf(d({ state: 'completed' }), NOW).label).toBe('Completada');
    const q = statusOf(d({ state: 'waiting', error_kind: 'quota', quota_scope: 'ip', waiting_until: new Date(NOW + 252_000).toISOString() }), NOW);
    expect(q.label).toBe('Cuota por IP · 04:12');
    expect(q.tone).toBe('warn');
    const f = statusOf(d({ state: 'failed', hoster: 'gdrive', error_kind: 'quota', quota_scope: 'file', error: 'quota (file): exceeded' }), NOW);
    expect(f.label).toBe('Fallida · cuota del archivo');
    expect(f.fileQuota).toBe(true);
    expect(f.canRetry).toBe(true);
    expect(statusOf(d({ resumable: false }), NOW).notResumable).toBe(true);
    expect(statusOf(d({ state: 'failed', error_kind: 'not_found' }), NOW).label).toBe('Fallida · no encontrado');
  });

  it('reads the quota scope from quota_scope; the hoster is only a fallback when it is missing', () => {
    expect(quotaScopeOf({ error_kind: 'quota', quota_scope: 'account', hoster: 'mega' })).toBe('account');
    expect(quotaScopeOf({ error_kind: 'quota', quota_scope: 'ip', hoster: 'gdrive' })).toBe('ip'); // backend wins over the hoster
    expect(quotaScopeOf({ quota_scope: 'file', hoster: 'gdrive' })).toBe('file'); // waiting: error_kind may lag behind
    expect(quotaScopeOf({ error_kind: 'quota', hoster: 'gdrive' })).toBe('file');
    expect(quotaScopeOf({ error_kind: 'quota', quota_scope: '', hoster: 'mediafire' })).toBe('ip');
    expect(quotaScopeOf({ error_kind: 'not_found', hoster: 'gdrive' })).toBe('');
  });

  it('a waiting download labels its quota from the scope', () => {
    const w = (quota_scope: Download['quota_scope']) => statusOf(d({ state: 'waiting', error_kind: 'quota', quota_scope, waiting_until: new Date(NOW + 60_000).toISOString() }), NOW).short;
    expect(w('account')).toBe('Cuota de la cuenta');
    expect(w('file')).toBe('Cuota del archivo');
    expect(statusOf(d({ state: 'waiting', waiting_until: new Date(NOW + 60_000).toISOString() }), NOW).short).toBe('En espera');
  });

  it('package labels use the package eta and children', () => {
    const p: Package = { id: 1, name: 'C', dest_dir: '', is_folder: true, state: 'downloading', size: 10, done: 5, speed: 1, eta: 84, files_total: 2, files_done: 1, files_failed: 0, priority: 0, exists_policy: 'skip', created_at: '' };
    expect(packageStatusOf(p, [d({ resumable: false })], NOW)).toMatchObject({ label: 'Descargando · 1:24', notResumable: true });
  });
});

describe('format (es-ES, like the designs)', () => {
  it('sizes, speeds and times', () => {
    expect(formatBytes(4.2e9)).toBe('4,2 GB');
    expect(formatBytes(380e6)).toBe('380 MB');
    expect(formatBytes(14e6)).toBe('14 MB');
    expect(formatBytes(-1)).toBe('—');
    expect(formatSpeed(18.4e6)).toBe('18,4 MB/s');
    expect(formatSpeed(0)).toBe('—');
    expect(formatETA(190)).toBe('3:10');
    expect(formatETA(3723)).toBe('1:02:03');
    expect(formatETA(-1)).toBe('');
    expect(formatCountdown(new Date(NOW + 252_000).toISOString(), NOW)).toBe('04:12');
    expect(formatCountdown(new Date(NOW - 1000).toISOString(), NOW)).toBe('00:00');
    expect(percent(62, 100)).toBe(62);
    expect(percent(1, -1)).toBe(0);
  });
});
