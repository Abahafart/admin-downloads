import { ext } from './lib/browser.js';
import {
  HOSTERS,
  DEFAULT_SETTINGS,
  dedupeCookies,
  hosterById,
  hosterOrigins,
  invalidHostRules,
  normalizeDaemonUrl,
  originPattern,
  parseExtensions,
  parseList,
  toNetscape,
} from './lib/core.js';
import { apiRequest, loadSettings, refreshBadge, saveSettings } from './lib/api.js';

const $ = (id) => document.getElementById(id);

function status(el, msg, kind) {
  el.textContent = msg;
  el.className = 'status' + (kind ? ' ' + kind : '');
}

let current = null;

async function init() {
  current = await loadSettings();
  $('daemonUrl').value = current.daemonUrl;
  $('token').value = current.token;
  $('autoConfirm').checked = Boolean(current.autoConfirm);
  $('origin').textContent = new URL(ext.runtime.getURL('')).origin;

  const c = current.capture;
  $('capEnabled').checked = Boolean(c.enabled);
  $('capExt').value = parseExtensions(c.extensions).join(', ');
  $('capMin').value = String(c.minSizeMB ?? DEFAULT_SETTINGS.capture.minSizeMB);
  $('capInclude').value = parseList(c.includeHosts).join('\n');
  $('capExclude').value = parseList(c.excludeHosts).join('\n');
  $('capSkipIncognito').checked = c.skipIncognito !== false;

  for (const h of HOSTERS) {
    const o = document.createElement('option');
    o.value = h.id;
    o.textContent = `${h.label} (${h.domains[0]})`;
    $('hoster').append(o);
  }
  onHosterChange();
}

function onHosterChange() {
  const h = hosterById($('hoster').value);
  $('hosterWarn').hidden = !h?.sensitive;
  $('label').placeholder = `${h?.label ?? ''} (navegador)`;
}

// ------------------------------------------------------------ connection

function onSave() {
  const el = $('connStatus');
  let url;
  try {
    url = normalizeDaemonUrl($('daemonUrl').value);
  } catch (err) {
    status(el, err.message, 'err');
    return;
  }
  // permissions.request must run synchronously inside the click handler
  // (user gesture), before any await.
  ext.permissions
    .request({ origins: [originPattern(url)] })
    .then(async (granted) => {
      current = {
        ...current,
        daemonUrl: url,
        token: $('token').value.trim(),
        autoConfirm: $('autoConfirm').checked,
      };
      await saveSettings(current);
      $('daemonUrl').value = url;
      if (!granted) {
        status(el, 'Guardado, pero sin permiso para acceder a esa dirección: la extensión no podrá conectarse.', 'err');
        return;
      }
      status(el, 'Guardado.', 'ok');
    })
    .catch((err) => status(el, `No se pudo guardar: ${err.message}`, 'err'));
}

async function onTest() {
  const el = $('connStatus');
  status(el, 'Probando…');
  let url;
  try {
    url = normalizeDaemonUrl($('daemonUrl').value);
  } catch (err) {
    status(el, err.message, 'err');
    return;
  }
  const s = { ...current, daemonUrl: url, token: $('token').value.trim() };
  try {
    await apiRequest('GET', '/health', undefined, s);
  } catch (err) {
    status(el, err.message, 'err');
    return;
  }
  try {
    const st = await refreshBadge(s);
    status(el, `Conectado. ${st.active} descargas activas, ${st.queued} en cola. Recuerda guardar.`, 'ok');
  } catch (err) {
    status(el, `El daemon responde, pero: ${err.message}`, 'err');
  }
}

// ------------------------------------------------------------ capture

async function onSaveCapture() {
  const el = $('capStatus');
  const include = parseList($('capInclude').value);
  const exclude = parseList($('capExclude').value);
  const bad = invalidHostRules([...include, ...exclude]);
  if (bad.length) {
    status(el, `Expresión regular no válida: ${bad.join(', ')}`, 'err');
    return;
  }
  const min = Number($('capMin').value);
  if (!Number.isFinite(min) || min < 0) {
    status(el, 'El tamaño mínimo debe ser un número positivo.', 'err');
    return;
  }
  current = {
    ...current,
    capture: {
      enabled: $('capEnabled').checked,
      extensions: parseExtensions($('capExt').value),
      minSizeMB: min,
      includeHosts: include,
      excludeHosts: exclude,
      skipIncognito: $('capSkipIncognito').checked,
    },
  };
  try {
    await saveSettings(current);
    status(el, current.capture.enabled ? 'Reglas guardadas. Captura activada.' : 'Reglas guardadas. Captura desactivada.', 'ok');
  } catch (err) {
    status(el, `No se pudo guardar: ${err.message}`, 'err');
  }
}

// ------------------------------------------------------------ cookies

async function getAllCookies(domain) {
  try {
    return await ext.cookies.getAll({ domain });
  } catch (err) {
    // Firefox with first-party isolation requires firstPartyDomain.
    if (/firstPartyDomain/i.test(String(err?.message))) {
      return ext.cookies.getAll({ domain, firstPartyDomain: null });
    }
    throw err;
  }
}

function onExport() {
  const el = $('cookieStatus');
  const h = hosterById($('hoster').value);
  if (!h) return;
  const label = $('label').value.trim() || `${h.label} (navegador)`;
  const perms = { permissions: ['cookies'], origins: hosterOrigins(h) };
  const btn = $('export');
  btn.disabled = true;
  // Request at the moment of the explicit action (user gesture).
  ext.permissions
    .request(perms)
    .then(async (granted) => {
      if (!granted) {
        status(el, 'Permiso denegado: no se ha leído ninguna cookie.', 'err');
        return;
      }
      try {
        const lists = await Promise.all(h.domains.map(getAllCookies));
        const cookies = dedupeCookies(lists.flat());
        if (!cookies.length) {
          status(el, `No hay cookies de ${h.label}. Inicia sesión en el hoster y vuelve a intentarlo.`, 'err');
          return;
        }
        status(el, 'Enviando…');
        await apiRequest('POST', '/accounts/import-cookies', {
          hoster: h.id,
          label,
          cookies: toNetscape(cookies),
        });
        status(el, `Se enviaron ${cookies.length} cookies de ${h.label} a la cuenta «${label}».`, 'ok');
      } finally {
        if ($('revoke').checked) {
          await ext.permissions.remove(perms).catch(() => {});
        }
      }
    })
    .catch((err) => status(el, err.message, 'err'))
    .finally(() => {
      btn.disabled = false;
    });
}

$('save').addEventListener('click', onSave);
$('test').addEventListener('click', onTest);
$('saveCapture').addEventListener('click', onSaveCapture);
$('hoster').addEventListener('change', onHosterChange);
$('export').addEventListener('click', onExport);
init().catch((err) => status($('connStatus'), err.message, 'err'));
