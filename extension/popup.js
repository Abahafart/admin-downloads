import { ext } from './lib/browser.js';
import { buildLinksPayload, countAdded, formatSpeed } from './lib/core.js';
import { apiRequest, loadSettings, refreshBadge } from './lib/api.js';

const $ = (id) => document.getElementById(id);

function show(msg, kind) {
  $('state').textContent = msg;
  $('state').className = 'status' + (kind ? ' ' + kind : '');
}

async function refresh(keepStatus = false) {
  try {
    const s = await refreshBadge();
    $('active').textContent = s.active;
    $('queued').textContent = s.queued;
    $('waiting').textContent = s.waiting;
    $('failed').textContent = s.failed;
    $('completed').textContent = s.completed;
    $('grabber').textContent = s.grabberSize;
    $('grid').hidden = false;
    if (!keepStatus) show(s.paused ? 'Conectado · todo en pausa' : `Conectado · ${formatSpeed(s.speed)}`, 'ok');
  } catch (err) {
    $('grid').hidden = true;
    show(err.message, 'err');
  }
}

$('sendPage').addEventListener('click', async () => {
  const [tab] = await ext.tabs.query({ active: true, currentWindow: true });
  const settings = await loadSettings();
  const payload = buildLinksPayload({ pageUrl: tab?.url }, { autoConfirm: settings.autoConfirm });
  if (!payload) {
    show('Esta página no tiene una URL http(s).', 'err');
    return;
  }
  try {
    const n = countAdded(await apiRequest('POST', '/links', payload, settings));
    show(n === null ? 'Enviada.' : `Enviada (${n} ${n === 1 ? 'entrada' : 'entradas'}).`, 'ok');
    refresh(true);
  } catch (err) {
    show(err.message, 'err');
  }
});

$('options').addEventListener('click', () => {
  ext.runtime.openOptionsPage();
  window.close();
});

refresh();
