// Background (service worker in Chrome, event page in Firefox): context menu,
// download capture and the badge with the active download count.
import { ext } from './lib/browser.js';
import { MENU_TITLE, buildLinksPayload, countAdded, shouldCapture } from './lib/core.js';
import { apiRequest, isSettingsChange, loadSettings, notify, refreshBadge } from './lib/api.js';

const MENU_ID = 'adesc-send';
const ALARM = 'adesc-stats';
// URLs we gave back to the browser after a failed hand-off: never capture
// them again for a while (byExtensionId is the primary guard).
const GUARD_MS = 2 * 60 * 1000;
const restarted = new Map();

let settingsCache = null;
async function settings() {
  if (!settingsCache) settingsCache = await loadSettings();
  return settingsCache;
}

async function setup() {
  try {
    await ext.contextMenus.removeAll();
    ext.contextMenus.create({
      id: MENU_ID,
      title: MENU_TITLE,
      contexts: ['link', 'selection', 'page', 'image', 'video', 'audio'],
    });
  } catch {
    /* duplicate id on racing startups */
  }
  await ext.alarms.create(ALARM, { periodInMinutes: 1, delayInMinutes: 0.1 });
  refreshBadge().catch(() => {});
}

ext.runtime.onInstalled.addListener(() => {
  setup();
});
ext.runtime.onStartup.addListener(() => {
  setup();
});

ext.alarms.onAlarm.addListener((alarm) => {
  if (alarm.name === ALARM) refreshBadge().catch(() => {});
});

ext.storage.onChanged.addListener((changes, area) => {
  if (!isSettingsChange(changes, area)) return;
  settingsCache = null;
  refreshBadge().catch(() => {});
});

// ------------------------------------------------------------ context menu

async function sendLinks(payload) {
  const s = await settings();
  try {
    const resp = await apiRequest('POST', '/links', payload, s);
    const n = countAdded(resp);
    const what = n === null ? 'Enlaces enviados' : n === 1 ? '1 enlace enviado' : `${n} enlaces enviados`;
    await notify('AdminDescargas', `${what} al LinkGrabber.`);
    refreshBadge(s).catch(() => {});
    return true;
  } catch (err) {
    await notify('AdminDescargas: error', err.message);
    return false;
  }
}

ext.contextMenus.onClicked.addListener(async (info) => {
  if (info.menuItemId !== MENU_ID) return;
  const s = await settings();
  const payload = buildLinksPayload(info, { autoConfirm: s.autoConfirm });
  if (!payload) {
    await notify('AdminDescargas', 'No hay ningún enlace que enviar.');
    return;
  }
  await sendLinks(payload);
});

// ------------------------------------------------------------ capture

function guarded(url) {
  const now = Date.now();
  for (const [u, t] of restarted) if (now - t > GUARD_MS) restarted.delete(u);
  return restarted.has(url);
}

async function takeOver(item) {
  const s = await settings();
  const d = shouldCapture(item, s, ext.runtime.id);
  if (!d.capture || guarded(d.url)) return false;
  try {
    await ext.downloads.cancel(item.id);
  } catch {
    return false; // already finished or not ours to cancel
  }
  try {
    await ext.downloads.erase({ id: item.id });
  } catch {
    /* keep the cancelled entry in the list */
  }
  const body = { urls: [d.url], source: 'extension' };
  if (s.autoConfirm) body.auto_confirm = true;
  try {
    await apiRequest('POST', '/links', body, s);
    await notify('AdminDescargas', 'Descarga capturada y enviada a AdminDescargas.');
    refreshBadge(s).catch(() => {});
  } catch (err) {
    restarted.set(d.url, Date.now());
    try {
      await ext.downloads.download({ url: d.url });
      await notify('AdminDescargas: error', `${err.message} Se descarga en el navegador.`);
    } catch {
      await notify('AdminDescargas: error', err.message);
    }
  }
  return true;
}

if (ext.downloads.onDeterminingFilename) {
  // Chrome: the file name (from Content-Disposition) is known here, which
  // makes the extension rule more accurate than in onCreated. The listener
  // must always call suggest(); returning true makes it asynchronous.
  ext.downloads.onDeterminingFilename.addListener((item, suggest) => {
    takeOver(item)
      .catch(() => false)
      .finally(() => {
        try {
          suggest();
        } catch {
          /* download already cancelled */
        }
      });
    return true;
  });
} else {
  ext.downloads.onCreated.addListener((item) => {
    takeOver(item).catch(() => {});
  });
}
