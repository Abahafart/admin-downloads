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

## Versión de Wails: v3.1.0

La app de escritorio está migrada a **Wails v3.1.0**. Aprovecha las capacidades
nativas de la API v3:

| Función | Implementación en Wails v3 |
|---|---|
| Bandeja | Bandeja nativa de Wails v3 (`app.SystemTray.New()` con `app.NewMenu()`). En Linux comprueba disponibilidad de StatusNotifierItem vía D-Bus (`tray_linux.go`). Se eliminó `fyne.io/systray` y el bucle manual de AppKit en macOS |
| Notificaciones | Servicio de notificaciones nativo de Wails v3 (`github.com/wailsapp/wails/v3/pkg/services/notifications`), con botones «Añadir» / «Ignorar» |
| Instancia única | `application.SingleInstanceOptions` en `application.New()` (reenvía argumentos y enfoca la ventana existente) |
| `adesc://` | `events.Common.ApplicationLaunchedWithUrl` de Wails v3 en todas las plataformas y `events.Mac.ApplicationShouldHandleReopen` en macOS. Registro: `protocols` en `wails.yaml`, `Info.plist` y `MimeType=x-scheme-handler/adesc` en el `.desktop` |
| Diálogos | `app.Dialog.OpenFile()`, `app.Dialog.Question()` y `app.Dialog.Error()` |
| Ocultar a bandeja | Hook nativo `events.Common.WindowClosing` en la ventana principal |
| Autoarranque | `emersion/go-autostart` en Linux y macOS; en Windows la clave `HKCU\...\Run` (go-autostart necesita cgo en Windows) |
| Portapapeles | `golang.design/x/clipboard` v0.11 (Go puro: Windows, macOS, X11/XWayland y Wayland con *data-control*); `wl-paste --watch` como alternativa |

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

Requisitos comunes: Go 1.27, Node ≥ 18 para la Web UI y opcionalmente la CLI de Wails v3 (`wails3`):

```sh
go install github.com/wailsapp/wails/v3/cmd/wails3@v3.1.0
make web                 # desde la raíz: compila web/dist (embebido)
cd cmd/adesc-desktop
```

Desde la raíz del repositorio se puede compilar directamente con Make:

```sh
make desktop             # compila cmd/adesc-desktop/build/bin/adesc-desktop
make lint-desktop        # ejecuta golangci-lint en cmd/adesc-desktop con los tags de escritorio
```

### Linux (Debian/Ubuntu)

```sh
sudo apt install pkg-config libgtk-3-dev libwebkit2gtk-4.1-dev
# Compilación directa con tags desktop,production,gtk3:
go build -tags desktop,production,gtk3 -o build/bin/adesc-desktop .
# .deb y AppImage (dependen de GTK 3 y WebKitGTK 4.1 del sistema):
build/linux/package.sh 0.1.0 amd64
```

El tag `gtk3` indica a Wails v3 que utilice GTK 3 / WebKitGTK 4.1 en lugar de GTK 4.
Opcional: `wl-clipboard` para el portapapeles en Wayland sin *data-control* ni XWayland.

### Windows

No necesita cgo ni compilador de C (WebView2). Para el instalador hace falta
NSIS (`makensis` en el `PATH`):

```sh
go build -tags desktop,production -ldflags "-s -w -H=windowsgui" -o build/bin/adesc-desktop.exe .
```

También se puede compilar desde Linux para Windows sin cgo:
`GOOS=windows go build -tags desktop,production -ldflags -H=windowsgui -o build/bin/adesc-desktop.exe .`

### macOS

Necesita las Command Line Tools de Xcode:

```sh
mkdir -p "build/bin/AdminDescargas.app/Contents/MacOS" "build/bin/AdminDescargas.app/Contents/Resources"
cp build/darwin/Info.plist "build/bin/AdminDescargas.app/Contents/Info.plist"
go build -tags desktop,production -ldflags "-s -w" -o "build/bin/AdminDescargas.app/Contents/MacOS/adesc-desktop" .
hdiutil create -volname AdminDescargas -srcfolder build/bin/AdminDescargas.app -ov -format UDZO build/dist/AdminDescargas.dmg
```

`build/darwin/Info.plist` fija el identificador `io.admindescargas.desktop` y
registra `adesc://`. Sin cuenta de Apple Developer la app no va firmada ni
notarizada: la primera vez hay que abrirla con clic derecho › Abrir.

## Ejecutar y desarrollar

```
adesc-desktop [--config <config.toml>] [--hidden] [enlaces o adesc://add?url=…]
```

- Logs: `<data_dir>/logs/adesc-desktop.log` (o `log.file`);
  `ADESC_LOG_STDERR=1` los manda a la consola.
- Tests del módulo: `go test -tags desktop,production,gtk3 ./...`. Necesitan
  GTK 3 y WebKitGTK 4.1 instalados: Wails v3 no tiene un modo sin GTK como el
  tag `bindings` de v2, y sin el tag `gtk3` intenta compilar contra GTK 4 /
  WebKitGTK 6.0. La lógica compartida, que no depende de Wails, se prueba sin
  GTK desde la raíz: `go test ./internal/desktop/...`.
- Iconos: `go run tools/genicons/main.go` regenera `build/appicon.png` y los de
  la bandeja (`icons/`).

## CI

El job `desktop` de `.github/workflows/ci.yml` compila en Ubuntu, Windows y
macOS, genera el `.deb` y el AppImage, el ejecutable / instalador y el
`.dmg`, corre `golangci-lint` sobre el módulo de escritorio, los sube como
artefactos y en Linux hace una prueba con `xvfb-run`: arranca la app con una
configuración temporal y comprueba que la API integrada responde en
`/api/v1/health` y acepta el token Bearer y el origen `wails://localhost`.
