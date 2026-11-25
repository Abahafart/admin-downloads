// Realistic fixtures matching docs/design (Descargas screen) plus data for
// the other screens. Times are relative to `now`.
import type { Account, Download, EgressStatus, GrabberEntry, GrabberFile, Package, Settings } from '../api/types';

const MB = 1_000_000;
const GB = 1_000_000_000;

export interface MockDB {
  downloads: Map<number, Download>;
  packages: Map<number, Package>;
  grabber: Map<number, GrabberEntry>;
  accounts: Account[];
  egress: Map<string, EgressStatus>;
  settings: Settings;
  paused: boolean;
  nextId: number;
}

const iso = (t: number) => new Date(t).toISOString();

export function seed(now: number): MockDB {
  const created = (minAgo: number) => iso(now - minAgo * 60_000);
  const downloads = new Map<number, Download>();
  const packages = new Map<number, Package>();
  let id = 100;

  const dl = (p: Partial<Download> & Pick<Download, 'package_id' | 'hoster' | 'name' | 'size' | 'state'>): Download => {
    const d: Download = {
      id: ++id, url: `https://example.invalid/${p.name}`, dest_path: `/home/usuario/Descargas/${p.rel_path ?? p.name}`,
      done: 0, speed: 0, eta: -1, connections: 0, attempts: 0, priority: 0, created_at: created(30), ...p,
    } as Download;
    downloads.set(d.id, d);
    return d;
  };
  const pkg = (p: Partial<Package> & Pick<Package, 'id' | 'name'>): Package => {
    const v: Package = {
      dest_dir: `/home/usuario/Descargas/${p.name}`, is_folder: false, state: 'queued', size: 0, done: 0, speed: 0, eta: -1,
      files_total: 1, files_done: 0, files_failed: 0, priority: 0, exists_policy: 'skip', created_at: created(30), ...p,
    };
    packages.set(v.id, v);
    return v;
  };

  // Curso_Fotografia/: MEGA folder, 12 files, 4,2 GB, 62 %, 18,4 MB/s.
  pkg({ id: 1, name: 'Curso_Fotografia', hoster: 'mega', is_folder: true, source_url: 'https://mega.nz/folder/AbCdEf#clave', files_total: 12, created_at: created(45) });
  const curso: [string, number, Download['state'], number, number?][] = [
    ['01 Exposicion/clase_01.mp4', 380, 'downloading', 0.88, 9.6],
    ['01 Exposicion/clase_02.mp4', 410, 'completed', 1],
    ['01 Exposicion/ejercicios.pdf', 12, 'completed', 1],
    ['02 Composicion/clase_03.mp4', 395, 'downloading', 0.55, 8.8],
    ['02 Composicion/clase_04.mp4', 402, 'completed', 1],
    ['02 Composicion/clase_05.mp4', 388, 'completed', 1],
    ['03 Luz/clase_06.mp4', 420, 'completed', 1],
    ['03 Luz/clase_07.mp4', 415, 'completed', 1],
    ['03 Luz/material/plantillas.zip', 96, 'queued', 0],
    ['04 Edicion/clase_08.mp4', 430, 'queued', 0],
    ['04 Edicion/clase_09.mp4', 445, 'queued', 0],
    ['Proyecto final/proyecto.mp4', 407, 'queued', 0],
  ];
  for (const [rel, size, state, frac, speed] of curso) {
    dl({
      package_id: 1, hoster: 'mega', name: rel.split('/').pop()!, rel_path: rel, size: size * MB, done: Math.round(size * MB * frac), state,
      speed: speed ? speed * MB : 0, connections: state === 'downloading' ? 4 : 0, egress_id: 'wg:mullvad-se', public_ip: '203.0.113.24',
      resumable: true, created_at: created(45), completed_at: state === 'completed' ? created(10) : undefined,
    });
  }

  pkg({ id: 2, name: 'dataset_v2.zip', hoster: 'gdrive', created_at: created(40) });
  dl({ package_id: 2, hoster: 'gdrive', name: 'dataset_v2.zip', size: 1.8 * GB, done: 0.34 * 1.8 * GB, state: 'downloading', speed: 6.1 * MB, connections: 2, egress_id: 'direct', public_ip: '198.51.100.7', resumable: true, created_at: created(40) });

  pkg({ id: 3, name: 'Album_2019.rar', hoster: 'mediafire', created_at: created(35) });
  dl({
    package_id: 3, hoster: 'mediafire', name: 'Album_2019.rar', size: 640 * MB, done: 0.47 * 640 * MB, state: 'waiting', egress_id: 'tor',
    error_kind: 'quota', quota_scope: 'ip', error: 'quota (ip, retry after 5m0s): límite de descarga de MediaFire para esta IP',
    waiting_until: iso(now + 252_000), resumable: true, attempts: 1, created_at: created(35),
  });

  pkg({ id: 4, name: 'backup_fotos.7z', hoster: 'terabox', state: 'paused', created_at: created(33) });
  dl({ package_id: 4, hoster: 'terabox', name: 'backup_fotos.7z', size: 2.3 * GB, done: 0.18 * 2.3 * GB, state: 'paused', egress_id: 'socks5:proxy-1', resumable: true, created_at: created(33) });

  pkg({ id: 5, name: 'video_presentacion.mp4', hoster: 'gdrive', state: 'failed', files_failed: 1, created_at: created(31) });
  dl({
    package_id: 5, hoster: 'gdrive', name: 'video_presentacion.mp4', size: 920 * MB, state: 'failed', egress_id: 'direct',
    error_kind: 'quota', quota_scope: 'file', error: 'quota (file): se superó la cuota de descargas de este archivo en Google Drive',
    attempts: 5, resumable: true, created_at: created(31),
  });

  pkg({ id: 6, name: 'instalador.iso', hoster: 'generic', created_at: created(20) });
  dl({ package_id: 6, hoster: 'generic', name: 'instalador.iso', size: 3.1 * GB, state: 'queued', egress_id: 'direct', resumable: false, created_at: created(20) });

  pkg({ id: 7, name: 'manual.pdf', hoster: 'mediafire', state: 'completed', files_done: 1, created_at: created(60) });
  dl({ package_id: 7, hoster: 'mediafire', name: 'manual.pdf', size: 14 * MB, done: 14 * MB, state: 'completed', egress_id: 'direct', resumable: true, created_at: created(60), completed_at: created(55) });

  // LinkGrabber: 3 entries (one still being listed).
  const grabber = new Map<number, GrabberEntry>();
  const files = (list: [string, number][], known: number[] = []): GrabberFile[] =>
    list.map(([rel, size], i) => ({ index: i, name: rel.split('/').pop()!, rel_path: rel, size, known: known.includes(i) || undefined }));
  grabber.set(1, {
    id: 1, url: 'https://mega.nz/folder/XyZ123#k3y', hoster: 'mega', state: 'resolving', package_name: 'Series_Docs', is_folder: true,
    files: [], total_size: -1, source: 'clipboard', added_at: iso(now - 20_000),
  });
  grabber.set(2, {
    id: 2, url: 'https://www.mediafire.com/folder/p4rt1tur4s/Partituras', hoster: 'mediafire', state: 'ready', package_name: 'Partituras', is_folder: true,
    files: files([
      ['Piano/Chopin_Nocturno_op9.pdf', 2.4 * MB], ['Piano/Satie_Gymnopedie_1.pdf', 1.1 * MB], ['Piano/ensayo_piano.mp3', 48 * MB],
      ['Guitarra/Clasica/Tarrega_Lagrima.pdf', 0.9 * MB], ['Guitarra/Clasica/Sor_Estudio_6.pdf', 1.3 * MB], ['Guitarra/acordes.pdf', 0.6 * MB],
      ['Guitarra/ensayo_guitarra.mp3', 52 * MB], ['LEEME.txt', 0.004 * MB],
    ], [5]),
    total_size: 106.3 * MB, source: 'api', added_at: iso(now - 120_000),
  });
  grabber.set(3, {
    id: 3, url: 'https://pixeldrain.com/u/dEm0R33l', hoster: 'pixeldrain', state: 'ready', package_name: 'demo_reel.mkv', is_folder: false,
    files: files([['demo_reel.mkv', 1.2 * GB]]), total_size: 1.2 * GB, source: 'extension', added_at: iso(now - 60_000),
  });

  const accounts: Account[] = [
    { id: 'acc-mega-1', hoster: 'mega', label: 'Personal', kind: 'password', username: 'ana@example.com', enabled: true, allow_rotation: false, status: 'ok', premium: false, quota_total: 5 * GB, quota_used: 1.2 * GB, checked_at: iso(now - 600_000), created_at: created(9000), updated_at: created(10) },
    { id: 'acc-mega-2', hoster: 'mega', label: 'Trabajo', kind: 'password', username: 'ana.trabajo@example.com', enabled: true, allow_rotation: false, status: 'needs_2fa', status_msg: 'MEGA pide el código de verificación en dos pasos', premium: true, premium_until: iso(now + 86400_000 * 40), quota_total: -1, quota_used: -1, created_at: created(2000), updated_at: created(5) },
    { id: 'acc-gdrive-1', hoster: 'gdrive', label: 'Google · ana', kind: 'oauth', username: 'ana@gmail.example', enabled: true, allow_rotation: false, status: 'ok', premium: false, quota_total: 15 * GB, quota_used: 9.8 * GB, checked_at: iso(now - 3600_000), created_at: created(8000), updated_at: created(60) },
    { id: 'acc-terabox-1', hoster: 'terabox', label: 'TeraBox', kind: 'cookie', username: '', enabled: true, allow_rotation: false, status: 'expired', status_msg: 'La cookie ndus caducó: vuelve a importarla', premium: false, quota_total: -1, quota_used: -1, checked_at: iso(now - 7200_000), created_at: created(5000), updated_at: created(120) },
    { id: 'acc-pd-1', hoster: 'pixeldrain', label: 'Pixeldrain Pro', kind: 'apikey', username: '', enabled: true, allow_rotation: true, status: 'ok', premium: true, premium_until: iso(now + 86400_000 * 120), quota_total: -1, quota_used: -1, checked_at: iso(now - 86400_000), created_at: created(3000), updated_at: created(1440) },
  ];

  const egress = new Map<string, EgressStatus>();
  const eg = (e: EgressStatus) => egress.set(e.id, e);
  eg({ id: 'direct', kind: 'direct', scope: 'per-connection', label: 'Conexión directa', healthy: true, public_ip: '198.51.100.7', in_use: 2, checked_at: iso(now - 30_000) });
  eg({ id: 'tor', kind: 'tor', scope: 'per-connection', label: 'Tor', healthy: true, public_ip: '185.220.101.4', in_use: 1, checked_at: iso(now - 60_000), exhausted: { mediafire: iso(now + 252_000), terabox: iso(now + 1500_000) } });
  eg({ id: 'wg:mullvad-se', kind: 'wireguard', scope: 'per-connection', label: 'Mullvad Suecia', healthy: true, public_ip: '203.0.113.24', in_use: 2, checked_at: iso(now - 45_000) });
  eg({ id: 'socks5:proxy-1', kind: 'socks5', scope: 'per-connection', label: 'proxy-1 · 192.0.2.10:1080', healthy: true, public_ip: '192.0.2.10', in_use: 0, checked_at: iso(now - 90_000), exhausted: { mega: iso(now + 3000_000) } });
  eg({ id: 'socks5:proxy-2', kind: 'socks5', scope: 'per-connection', label: 'proxy-2 · 192.0.2.77:1080', healthy: false, in_use: 0, last_error: 'conexión rechazada', checked_at: iso(now - 120_000) });
  eg({ id: 'router', kind: 'router', scope: 'global', label: 'Router (UPnP)', healthy: true, public_ip: '198.51.100.7', in_use: 0, checked_at: iso(now - 300_000) });

  const settings: Settings = {
    download_dir: '/home/usuario/Descargas',
    downloads: { max_active: 3, max_per_hoster: 2, conns_per_file: 4, speed_limit: 0, max_retries: 5, resume_on_start: true, exists_policy: 'skip' },
    rotation: { enabled: true, strategy: 'round-robin', global_rotation: true },
    clipboard: { enabled: true, action: 'ask' },
    postprocess: { verify_checksums: true, extract: false, passwords: [], delete_archives: false },
    desktop: { close_to_tray: true, autostart: false, notifications: true, start_hidden: false },
    appearance: { estilo: 'grafito', tema: 'sistema', acento: '' },
    server: { listen: '127.0.0.1:7979', password_set: false, tls: false, allowed_origins: [], allowed_hosts: [] },
    network: {
      direct: true, tor_enabled: true, proxies: 2, wireguard: ['mullvad-se'], health_interval: '5m',
      router: { enabled: true, method: 'upnp', script: [] },
      system_vpn: [{ name: 'proton', connect: ['protonvpn-cli', 'connect', '--fastest'], rotate: ['protonvpn-cli', 'reconnect'], disconnect: ['protonvpn-cli', 'disconnect'] }],
    },
    restart_required: false,
  };

  return { downloads, packages, grabber, accounts, egress, settings, paused: false, nextId: 1000 };
}

/** Files a pasted folder link expands to in the mock. */
export function fakeFolderFiles(name: string): [string, number][] {
  return [
    [`Temporada 1/${name}_01.mkv`, 720 * MB], [`Temporada 1/${name}_02.mkv`, 705 * MB], [`Temporada 1/${name}_03.mkv`, 698 * MB],
    [`Temporada 2/${name}_04.mkv`, 731 * MB], [`Temporada 2/${name}_05.mkv`, 712 * MB], ['Subtitulos/es.srt', 0.08 * MB],
    ['Subtitulos/en.srt', 0.07 * MB], ['caratula.jpg', 1.4 * MB],
  ];
}

export const SERIES_FILES: [string, number][] = Array.from({ length: 24 }, (_, i) => {
  const t = Math.floor(i / 8) + 1;
  const e = String(i + 1).padStart(2, '0');
  return i % 8 === 7 ? [`Temporada ${t}/notas_T${t}.txt`, 0.02 * MB] : [`Temporada ${t}/Documental_T${t}E${e}.mkv`, (650 + (i * 37) % 200) * MB];
});
