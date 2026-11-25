// Injected by adesc-desktop into index.html (see assets.go). Drag & drop of
// links, text and small text files anywhere in the window goes to the
// LinkGrabber; a WireGuard .conf is imported. A UI component that handles a
// drop itself must call event.preventDefault() and this script ignores it.
(function () {
  'use strict';
  if (window.__adescDesktop) return;
  window.__adescDesktop = { version: 2 };

  // Compatibility bridge: web/src/lib/store/app.svelte.ts listens for
  // window.runtime.EventsOn('adesc:navigate', ...).
  if (!window.runtime) window.runtime = {};
  if (!window.runtime.EventsOn) {
    var listeners = {};
    window.runtime.EventsOn = function (name, cb) {
      if (!listeners[name]) listeners[name] = [];
      listeners[name].push(cb);
    };
    window.__adescNavigate = function (screen) {
      var cbs = listeners['adesc:navigate'];
      if (cbs) {
        for (var i = 0; i < cbs.length; i++) cbs[i](screen);
      }
    };
    import('/wails/runtime.js').then(function (wails) {
      if (wails && wails.Events && wails.Events.On) {
        for (var name in listeners) {
          (function (n, cbs) {
            wails.Events.On(n, function (ev) {
              var val = ev && ev.data !== undefined ? ev.data : ev;
              for (var j = 0; j < cbs.length; j++) cbs[j](val);
            });
          })(name, listeners[name]);
        }
      }
    }).catch(function () {});
  }

  var MAX_FILE = 4 * 1024 * 1024;

  function has(types, t) {
    if (!types) return false;
    for (var i = 0; i < types.length; i++) if (types[i] === t) return true;
    return false;
  }

  function accepted(dt) {
    return dt && (has(dt.types, 'Files') || has(dt.types, 'text/uri-list') || has(dt.types, 'text/plain'));
  }

  // Without this the webview navigates to a dropped link or opens the file.
  window.addEventListener('dragover', function (e) {
    if (e.defaultPrevented || !accepted(e.dataTransfer)) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = 'copy';
  });

  function readText(file) {
    return new Promise(function (resolve) {
      if (file.size > MAX_FILE) return resolve(null);
      var r = new FileReader();
      r.onload = function () { resolve({ name: file.name, text: String(r.result || '') }); };
      r.onerror = function () { resolve(null); };
      r.readAsText(file);
    });
  }

  window.addEventListener('drop', function (e) {
    if (e.defaultPrevented || !accepted(e.dataTransfer)) return;
    e.preventDefault();
    var dt = e.dataTransfer;
    var text = dt.getData('text/uri-list') || dt.getData('text/plain') || '';
    var files = Array.prototype.slice.call(dt.files || []);
    Promise.all(files.map(readText)).then(function (read) {
      var body = { text: text, files: read.filter(Boolean) };
      if (!body.text && body.files.length === 0) return;
      fetch('/__adesc/drop', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body)
      }).catch(function () {});
    });
  });
})();
