# adesc-desktop

App de escritorio de AdminDescargas (PLAN.md, sección 8): una ventana con la
misma Web UI de `web/`, icono en la bandeja con la velocidad total,
notificaciones del sistema, diálogos nativos, monitor del portapapeles,
arrastrar y soltar, autoarranque, instancia única y esquema `adesc://`.

Es un **módulo Go aparte** (`admindescargas/cmd/adesc-desktop`, con
`replace admindescargas => ../..`), así Wails y sus dependencias nunca entran
en el `go.mod` raíz y `go build ./...` del daemon lo ignora. La lógica que no
depende de Wails vive en el módulo raíz, en `internal/desktop` (conexión,
eventos SSE, notificaciones, `adesc://`, tema, autoarranque) e
`internal/desktop/clipboard` (monitor del portapapeles), y se prueba con el
resto del proyecto.

## Versión de Wails: v2.17.1 (plan B)

La app se escribió con Wails v2 porque, cuando el proyecto usaba Go 1.24,
todas las versiones de Wails v3 (y v2 ≥ 2.13) exigían Go ≥ 1.25. Con Go 1.27
ya se podría migrar a v3; mientras tanto se usa **Wails v2.17.1**, la última
v2. Lo que v2 no trae se cubre así:

| Función | Implementación |
|---|---|
| Bandeja | `fyne.io/systray` v1.12.2 (D-Bus StatusNotifierItem en Linux; hilo propio en Windows; bucle externo en el hilo principal de AppKit en macOS) |
| Notificaciones | las de Wails v2 (`runtime.SendNotification*`), con botones «Añadir» / «Ignorar». No hace falta `gen2brain/beeep` |
| Instancia única | `options.SingleInstanceLock` de Wails (reenvía los argumentos) |
| `adesc://` | Windows y Linux: el sistema lanza la app con la URL como argumento y la instancia única la reenvía; macOS: `mac.Options.OnUrlOpen`. Registro: `info.protocols` de `wails.json` (NSIS y `Info.plist`) y `MimeType=x-scheme-handler/adesc` en el `.desktop` |
| Autoarranque | `emersion/go-autostart` en Linux y macOS; en Windows la clave `HKCU\...\Run` (go-autostart necesita cgo en Windows) |
| Portapapeles | `golang.design/x/clipboard` v0.11 (Go puro: Windows, macOS, X11/XWayland y Wayland con *data-control*); `wl-paste --watch` como alternativa |

Cuando el proyecto suba a Go ≥ 1.26 se puede migrar a v3 (bandeja,
notificaciones y ventanas múltiples nativas) sin tocar el contrato con la UI.

## Modos

`[desktop].mode` en `config.toml`:

- **`integrated`** (por defecto): arranca el núcleo en el propio proceso con
  `internal/app` y sirve la API en `server.listen` (127.0.0.1:7979), así la CLI
  y la extensión funcionan con la app abierta. Si el puerto está ocupado usa
  uno aleatorio de loopback (queda en `instance.json`). Si un `adesc daemon` ya
  tiene la carpeta de datos (`app.ErrAlreadyRunning`), la app lo detecta por
  `instance.json` + `/api/v1/health` y **ofrece conectarse a él como cliente**
  (sin preguntar si arrancó oculta por el autoarranque).
- **`remote`**: cliente de un daemon de otra máquina (`remote_url` y
  `remote_token`), sin núcleo local. Si no responde, la app ofrece volver al
  modo integrado. En el daemon remoto hay que permitir el origen del webview
  (ver abajo) en `server.allowed_origins`.

El modo se cambia desde la UI con `PUT /__adesc/desktop`, que guarda
`config.toml`, reconecta y recarga la ventana.

## Contrato con la Web UI

La UI siempre habla con la API por HTTP + SSE con el token Bearer, también
dentro de la app. El servidor de assets de Wails sirve `web.Dist` y añade
estos endpoints del mismo origen (JSON; rechazan cualquier `Origin` que no sea
el del webview):

| Endpoint | Respuesta |
|---|---|
| `GET /__adesc/connection` | `{"baseURL": "http://127.0.0.1:7979", "token": "…", "mode": "integrated\|local-daemon\|remote"}`. Espera a que arranque el núcleo (hasta 30 s); `503 {"error"}` si falla. En un navegador normal este endpoint no existe (404): así sabe la UI que no está en escritorio |
| `GET /__adesc/desktop`, `PUT /__adesc/desktop` | ajustes de escritorio: `mode`, `remote_url`, `remote_token`, `close_to_tray`, `autostart`, `notifications`, `start_hidden` (+ `connection`, `tray_available`, `platform`, `version`) |
| `GET /__adesc/clipboard`, `PUT /__adesc/clipboard` | estado del monitor (`available`, `backend`, `error`) y `config.ClipboardConfig` (`enabled`, `action` ask\|add\|linkgrabber, `hosters`, `ignore_known`, `use_wl_paste`). Los ajustes del portapapeles son de la máquina de escritorio, no del daemon |
| `POST /__adesc/appearance` | `{"estilo","tema","esquema"}`: cambia el fondo de la ventana al `--bg` del tema activo al momento (además se lee de `GET /api/v1/settings` → `appearance` al conectar, y se recuerda en `desktop-state.json` para el siguiente arranque) |
| `POST /__adesc/dialog/download-folder` | `{"path"}` con el diálogo nativo (204 si se cancela); la UI lo guarda con `PUT /api/v1/settings` |
| `POST /__adesc/import/proxies`, `…/wireguard`, `…/cookies?hoster=<plugin>&label=<etiqueta>` | diálogo nativo para elegir el archivo, que se envía a `/api/v1/egress/import`, `/api/v1/egress/wireguard` o `/api/v1/accounts/import-cookies`; devuelve la respuesta de la API (204 si se cancela) |
| `POST /__adesc/drop` | lo usa el script inyectado de arrastrar y soltar |
| `POST /__adesc/window/hide`, `…/quit` | ocultar a la bandeja / salir |

Los mismos métodos existen como binding de Wails (`window.go.main.Bindings`:
`Connection()`, `Desktop()`, `SetDesktop()`, `Clipboard()`, `SetClipboard()`,
`SetAppearance()`, `PickDownloadFolder()`, `ImportProxies()`,
`ImportWireGuard()`, `ImportCookies(hoster, label)`, `HideWindow()`, `Quit()`),
pero el transporte recomendado es HTTP.

**Orígenes del webview** (cabecera `Origin` de las peticiones a la API):
`wails://wails` en Linux y macOS, `http://wails.localhost` en Windows
(`http://localhost:34115` con `wails dev`). En modo integrado la app los añade
a `server.allowed_origins` de la configuración en memoria que recibe el núcleo.

**Eventos de Wails** que la UI puede escuchar (`window.runtime.EventsOn`):
`adesc:navigate` con la pantalla a mostrar (`"linkgrabber"` tras añadir
enlaces, `"red"` tras importar un `.conf`).

**Arrastrar y soltar:** el `index.html` servido lleva inyectado
`/__adesc/desktop.js`. Al soltar enlaces, texto o archivos de texto pequeños
en cualquier parte de la ventana se envían al LinkGrabber (un `.conf` de
WireGuard se importa). Un componente de la UI que gestione su propio `drop`
debe llamar a `event.preventDefault()` y el script lo respeta.

## Funciones nativas

- **Bandeja:** velocidad total (también en el título junto al icono en macOS),
  «Abrir AdminDescargas», «Pausar todo» / «Reanudar todo»
  (`/api/v1/pause-all`, `/api/v1/resume-all`), «Monitor del portapapeles» y
  «Salir». Clic en el icono abre la ventana (en macOS abre el menú). En Linux
  hace falta un host StatusNotifierItem (KDE, XFCE, Cinnamon, GNOME con la
  extensión AppIndicator); si no lo hay, cerrar la ventana cierra la app en
  vez de esconderla.
- **Cerrar a la bandeja** (`close_to_tray`): las descargas siguen.
- **Notificaciones** de descarga completada, fallida o en espera por cuota
  (agrupadas: «12 descargas completadas»), más los `notice` del núcleo.
- **Portapapeles** (sección 9.2): extrae las URLs del texto copiado, las pasa
  por el `Match` de los plugins (sin el genérico) y, según `action`, pregunta
  con una notificación «3 enlaces de MEGA detectados» (Añadir / Ignorar; si no
  hay notificaciones, un diálogo), las añade a la cola o las deja en el
  LinkGrabber. Ignora el mismo texto dos veces, enlaces ya vistos y copias
  marcadas como sensibles por gestores de contraseñas. El texto copiado nunca
  se guarda ni se escribe en los logs.
- **Instancia única:** abrir la app otra vez (o un enlace `adesc://`) enfoca la
  ventana existente y le pasa los enlaces.
- **`adesc://add?url=<enlace>[&url=…][&text=…]`**: solo se aceptan enlaces
  `http(s)`.
- **Autoarranque** con `--hidden` (arranca en la bandeja).

## Compilar

Requisitos comunes: Go 1.27, Node ≥ 18 para la Web UI y la CLI de Wails:

```sh
go install github.com/wailsapp/wails/v2/cmd/wails@v2.17.1
make web                 # desde la raíz: compila web/dist (embebido)
cd cmd/adesc-desktop
```

### Linux (Debian/Ubuntu)

```sh
sudo apt install pkg-config libgtk-3-dev libwebkit2gtk-4.1-dev
wails build -s -skipbindings -tags webkit2_41
# o sin la CLI de Wails:
go build -tags desktop,production,webkit2_41 -o build/bin/adesc-desktop .
# .deb y AppImage (dependen de GTK 3 y WebKitGTK 4.1 del sistema):
build/linux/package.sh 0.1.0 amd64
```

El tag `webkit2_41` es obligatorio para WebKitGTK 4.1 (sin él Wails usa la
4.0). Opcional: `wl-clipboard` para el portapapeles en Wayland sin
*data-control* ni XWayland. Las distribuciones que solo traen WebKitGTK 4.0
necesitan `libwebkit2gtk-4.0-dev` y compilar sin el tag.

### Windows

No necesita cgo ni compilador de C (WebView2). Para el instalador hace falta
NSIS (`makensis` en el `PATH`):

```sh
wails build -s -skipbindings -nsis
```

El instalador NSIS descarga e instala el runtime de WebView2 si falta y
registra `adesc://`. También se puede compilar desde Linux:
`GOOS=windows go build -tags desktop,production -ldflags -H=windowsgui .`

### macOS

Necesita las Command Line Tools de Xcode:

```sh
wails build -s -skipbindings -platform darwin/universal
hdiutil create -volname AdminDescargas -srcfolder build/bin/AdminDescargas.app -ov -format UDZO build/dist/AdminDescargas.dmg
```

`build/darwin/Info.plist` fija el identificador `com.admindescargas.desktop` y
registra `adesc://`. Sin cuenta de Apple Developer la app no va firmada ni
notarizada: la primera vez hay que abrirla con clic derecho › Abrir.

## Ejecutar y desarrollar

```
adesc-desktop [--config <config.toml>] [--hidden] [enlaces o adesc://add?url=…]
```

- Logs: `<data_dir>/logs/adesc-desktop.log` (o `log.file`);
  `ADESC_LOG_STDERR=1` los manda a la consola.
- `wails dev -s` abre la app con la UI embebida. Para recarga en caliente de la
  UI, `npm run dev` en `web/` contra un daemon.
- Tests sin GTK: `go test -tags bindings ./...`; con GTK instalado:
  `go test -tags desktop,production,webkit2_41 ./...`. La lógica compartida se
  prueba desde la raíz: `go test ./internal/desktop/...`.
- Iconos: `go run tools/genicons/main.go` regenera `build/appicon.png` y los de
  la bandeja (`icons/`).

## CI

El job `desktop` de `.github/workflows/ci.yml` compila en Ubuntu, Windows y
macOS (`wails build`), genera el `.deb` y el AppImage, el instalador NSIS y el
`.dmg`, los sube como artefactos y en Linux hace una prueba con `xvfb-run`:
arranca la app con una configuración temporal y comprueba que la API
integrada responde en `/api/v1/health` y acepta el token.
