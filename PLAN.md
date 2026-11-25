# Plan: AdminDescargas, un gestor de descargas multi-hoster en Go

## Contexto

Queremos un gestor de descargas al estilo de JDownloader o Internet Download Manager que descargue de MediaFire, MEGA, TeraBox, Google Drive y otros hosters. Requisitos:
- Iniciar sesión con credenciales cuando el usuario las tenga.
- Saltar los límites de descarga por IP rotando la IP con proxies, Tor, WireGuard, reconexión del router o una VPN del sistema.
- Reanudar o reiniciar las descargas que no terminaron.
- Detectar los enlaces de los hosters soportados cuando el usuario los copia.
- Descargar carpetas completas, con su estructura.

Decisiones ya tomadas con el usuario:
- **Lenguaje: Go** (empezó con 1.24; ahora 1.27). Elegido por:
  - **MEGA:** existe `go-mega`.
  - **Google Drive:** existe un cliente oficial.
  - **WireGuard:** `wireguard-go` con `netstack` crea túneles sin root dentro del proceso.
  - **UPnP:** existe `goupnp`.
  - **Tor:** existe `bine`.
  - **SQLite:** `modernc.org/sqlite` está escrito en Go puro, así que no hace falta cgo.
  - **Concurrencia:** las goroutines encajan con descargas por segmentos.
  - **Distribución:** compila a un solo binario multiplataforma.
  - **Rust:** el instalado es 1.72 y está desactualizado.
- **Interfaz:** tres formas de usarlo, todas sobre el mismo núcleo y la misma interfaz Svelte:
  - una **app de escritorio** (`adesc-desktop`, con Wails) con ventana nativa, icono en la bandeja, notificaciones y diálogos del sistema, al estilo IDM/JDownloader;
  - un daemon con API REST y SSE y una Web UI embebida, para servidores o uso remoto;
  - una CLI.
- **Rotación de IP:** se implementan los cuatro métodos:
  - pool de proxies HTTP/SOCKS5 más Tor;
  - WireGuard en espacio de usuario;
  - reconexión del router por UPnP o script;
  - VPN del sistema mediante comandos.
- **Plataformas:** Linux, Windows y macOS desde el día 1, con CI en los tres.

Al empezar, el directorio del proyecto estaba vacío y no era un repo git.
- **Herramientas instaladas:** Go 1.27.4 (antes 1.24.1), Node 18.20, npm 10, git y openvpn.
- **No instalado:** tor.
- **Node:** se recomienda actualizar a la LTS 22 o 24. Si se queda en 18, hay que fijar Vite 6.

Este plan vive en `PLAN.md`, en la raíz del repo.

---

## 1. Arquitectura

```
 ┌─ adesc-desktop (Wails) ───────┐   ┌─ adesc (daemon + CLI) ────────┐
 │ ventana webview, bandeja,     │   │ servicio en segundo plano     │
 │ notificaciones, diálogos      │   │ y CLI, CGO_ENABLED=0          │
 └───────────────┬───────────────┘   └───────────────┬───────────────┘
                 └─── internal/app ─────────┬────────┘  (los dos arrancan el mismo núcleo)
                                            │
                    ┌─ núcleo ──────────────┴───────────────────────┐
 Navegador ──HTTP──▶│ api/ (REST + SSE, token, Web UI embebida)     │
 CLI ───────HTTP──▶ │        │                                      │
 Extensión ─HTTP──▶ │ core/ (cola, scheduler, máquina de estados)   │
                    │   ├── linkgrabber/ (parseo, carpetas, dedupe) │
                    │   ├── hosters/  (plugins: resolve/prepare)    │
                    │   ├── downloader/ (segmentos, rangos, resume) │
                    │   ├── accounts/ (cuentas + secret store)      │
                    │   └── rotation/ (cuotas, cooldowns, rotar)    │
                    │ netx/ (egress: direct/proxy/tor/wg/system)    │
                    │ store/ (SQLite)  config/ (TOML)               │
                    └───────────────────────────────────────────────┘
```

Estructura del repo:

```
cmd/adesc/main.go          # cobra: daemon, add, ls, pause, resume, accounts, egress, ip, selftest (CGO_ENABLED=0)
cmd/adesc-desktop/         # app de escritorio Wails: main.go, wails.json, build/ (iconos, instaladores)
internal/app/              # arranque compartido del núcleo (config, store, engine, api); lo usan los dos binarios
internal/desktop/          # bandeja, notificaciones, diálogos, autoarranque, instancia única, portapapeles
internal/core/             # Engine, Job, Package, scheduler, eventos
internal/downloader/       # motor segmentado
internal/hosters/          # interfaz + registry
  generic/ mediafire/ mega/ gdrive/ terabox/        (ola 1)
  pixeldrain/ gofile/ dropbox/ onedrive/ onefichier/ yandex/  (ola 2)
internal/netx/             # Egress + implementaciones (direct, httpproxy, socks5, tor, wireguard, system)
internal/rotation/         # RotationManager, QuotaBook, estrategias, reconexión global
internal/accounts/         # AccountManager, SecretStore (keyring | vault cifrado)
internal/linkgrabber/      # extracción de URLs, expansión de carpetas, clipboard (opcional)
internal/postprocess/      # checksums, extracción zip/7z/rar
internal/store/            # SQLite + migraciones embebidas (goose)
internal/api/              # handlers, auth, SSE; sirve la Web UI desde web.Dist
internal/config/
web/                       # Svelte 5 + Vite + TS -> web/dist; la comparten la Web UI y la app de escritorio
web/embed.go               # package web con //go:embed all:dist (go:embed no admite "..", por eso vive junto a dist)
extension/                 # extensión MV3 (Chrome/Firefox), fase final
testdata/                  # fixtures HTML/JSON grabados por hoster
```

## 2. Interfaces clave

```go
// hosters
type Hoster interface {
    Name() string
    Match(u *url.URL) bool
    Resolve(ctx context.Context, s *Session, link string) ([]FileInfo, error)  // carpeta -> N archivos
    Prepare(ctx context.Context, s *Session, f FileInfo) (*DownloadSpec, error) // enlace directo
    Classify(resp *http.Response, body []byte, err error) Outcome               // Ok | Quota{Scope,RetryAfter} | NeedsAuth | NotFound | Temporary | Fatal
    Caps() Caps                                                                // MaxConnsPerFile, MaxParallel, Resumable, AccountKinds...
}

type FileInfo struct {
    ID       string     // id del archivo en el hoster
    Name     string
    RelPath  string     // ruta dentro de la carpeta compartida, p. ej. "Temporada 1/ep01.mkv"; vacío si es un archivo suelto
    Size     int64
    Checksum *Checksum
    Link     string     // enlace propio del archivo, para volver a resolverlo al reanudar
}

type Session struct {               // se construye por (hoster, egress, cuenta)
    Client  *http.Client            // Transport.DialContext = egress.DialContext
    Jar     http.CookieJar          // un jar por egress y hoster, para no mezclar identidades
    Account *accounts.Account       // nil = anónimo
    Egress  netx.Egress
}

type DownloadSpec struct {
    URL       string
    Header    http.Header
    Size      int64
    Resumable bool
    MaxConns  int
    Expires   time.Time                                   // si caduca (403/410), se llama otra vez a Prepare y se sigue desde el offset
    Decryptor func(offset int64) (cipher.Stream, error)   // MEGA: AES-CTR posicionable
    Checksum  *Checksum                                   // sha256 de MediaFire, md5 de Drive, MAC de MEGA
}

// netx
type Egress interface {
    ID() string
    Kind() Kind                 // Direct, HTTPProxy, SOCKS5, Tor, WireGuard, SystemGlobal
    Scope() Scope               // PerConnection (varias a la vez) | Global (cambia la IP de toda la máquina)
    DialContext(ctx context.Context, network, addr string) (net.Conn, error)
    Rotate(ctx context.Context) error   // Tor NEWNYM, cambio de peer WG, reconexión del router o VPN
    PublicIP(ctx context.Context) (netip.Addr, error)
    Healthy() bool
    Close() error
}
```

La abstracción `Egress` existe desde la fase 1, con solo `Direct`, para que todo el código HTTP pase por ella desde el principio.

## 3. Motor de descarga (`internal/downloader`)

- **Segmentos:** el archivo se divide en N segmentos (por defecto 8, limitado por `Caps.MaxConnsPerFile`). Cada segmento hace su petición `Range` en una goroutine y escribe con `WriteAt` en `archivo.part`, que se preasigna con `Truncate`.
- **Segmentación dinámica al estilo IDM:** cuando un segmento termina, el segmento pendiente más grande se parte en dos.
- **Persistencia:** los offsets de cada segmento se guardan en SQLite en lotes cada 1 o 2 segundos. Así se reanuda tras un crash, una pausa o un cambio de IP.
- **Reintentos:** backoff exponencial con jitter. Antes de reintentar, `Classify` decide la acción:
  - `Quota`: se avisa a `rotation`, que devuelve un egress nuevo o una espera.
  - `Expired`: se vuelve a llamar a `Prepare`.
  - `Temporary`: se reintenta.
- **Límite de velocidad:** global y por descarga con `golang.org/x/time/rate`.
- **Sin `Range`:** si el servidor no lo soporta, se descarga en un solo stream sin reanudación y se marca en la UI.
- **Al terminar:** se verifica el checksum, se renombra el `.part` y se lanza el post-proceso.
- **Estados:** `queued → resolving → waiting(countdown) → downloading → verifying → postprocessing → completed | failed | paused`.
- **Scheduler:** máximo global de descargas, máximo por hoster, máximo por egress, prioridades y paquetes al estilo JDownloader.

## 4. Hosters (ola 1)

| Hoster | Resolución | Límite real | Qué lo salta |
|---|---|---|---|
| **MediaFire** | Página `/file/<key>`: leer `a#downloadButton` (`href` o `data-scrambled-url` en base64). Metadatos con `api/1.5/file/get_info.php` (nombre, tamaño, sha256). Carpetas con `api/1.5/folder/get_content.php`, recursivo y paginado. | No hay cuota fuerte. Responde 429 o captcha si se hacen muchas peticiones. | Rotar el egress ante 429 y limitar la concurrencia por IP. |
| **MEGA** | API `https://g.api.mega.co.nz/cs`. Archivo: `{"a":"g","g":1,"p":handle}`. Carpeta: `?n=<handle>` con `{"a":"f","c":1,"r":1}`. La clave del `#fragment` desempaqueta la clave AES y el nonce, y descifra los atributos con AES-CBC. | Cuota de transferencia por IP para anónimos y cuentas free: HTTP **509** en el storage o `-17 EOVERQUOTA`. | Rotar la IP. El cifrado AES-128-CTR permite descifrar desde cualquier offset, así que la descarga se **reanuda con la IP nueva** y admite segmentos paralelos. Una cuenta Pro da más cuota. |
| **Google Drive** | Sin auth: `drive.usercontent.google.com/download?id=..&export=download`. Si responde con el HTML de "virus scan", se parsea el form (`confirm`, `uuid`) y se reenvía. Con OAuth: Drive API v3 `files.get?alt=media` (admite Range) y `files.list` para carpetas. | "Cuota de descarga excedida" **por archivo, no por IP**. | Rotar la IP **no ayuda**. Con OAuth se usa `files.copy` para copiar el archivo a tu Drive, se descarga la copia y se borra (configurable). |
| **TeraBox** (terabox.com, 1024terabox, teraboxapp, freeterabox, 4funbox, mirrobox, nephobox… la lista de dominios va en config) | Se carga la página de share con la cookie `ndus`. Del HTML se extraen `jsToken` y `dp-logid`. Luego `GET /share/list?shorturl=..&root=1&jsToken=..` devuelve `dlink`, y `dlink` redirige (302) al CDN. | Muchos archivos exigen sesión. La velocidad free es limitada. Hay bloqueos o verificación por abuso. | Cookie de cuenta, rotación de cuentas y de IP ante bloqueos, o premium para la velocidad. Es el plugin **más frágil**: los selectores y parámetros van aislados en `terabox/endpoints.go` y se valida con `adesc selftest terabox`. |
| **Genérico** | HTTP, HTTPS y FTP directo. El nombre sale de `Content-Disposition`. | — | — |

Ola 2, misma interfaz:
- **Pixeldrain:** API y API key. El límite free es por IP, así que se rota.
- **Gofile:** token de invitado con `POST /accounts`; el `wt` se extrae de su JS.
- **Dropbox:** `dl=1`.
- **OneDrive:** API `shares/u!<b64>`.
- **1fichier:** la espera free es por IP, así que se rota. Premium con API key.
- **Yandex Disk:** API pública.

Cada plugin lleva fixtures grabados en `testdata/` para tests offline, y tests en vivo con el build tag `live`.

## 5. Cuentas y credenciales (`internal/accounts`)

- **Tipos de cuenta:** `password`, `cookie`, `oauth` y `apikey`.
  - MEGA: password, con login v1 y v2 (PBKDF2-SHA512) y 2FA.
  - Google: OAuth.
  - TeraBox: cookie.
  - MediaFire: cookie (opcional).
  - Pixeldrain y 1fichier: apikey.
- **Secretos:** van al keyring del sistema con `zalando/go-keyring` (Secret Service en Linux, Keychain en macOS, Credential Manager en Windows). Si no hay keyring, como en un servidor sin pantalla, se usa un **vault cifrado**: AES-256-GCM con clave Argon2id derivada de una contraseña maestra. En SQLite solo se guardan los metadatos y una referencia al secreto.
- **MEGA:** el login devuelve un `sid`. Se guarda la sesión para no reenviar la contraseña cada vez. El 2FA se pide desde la UI.
- **Google OAuth:** el usuario registra su propio OAuth client de tipo "Desktop" y el flujo usa un loopback servido por el daemon. El scope `drive` hace falta para el truco de copiar el archivo.
- **Importar cookies:** se puede pegar un `cookies.txt` en formato Netscape o un JSON. Más adelante, la extensión del navegador las enviará automáticamente.
- **Comprobar cuentas:** se valida cada cuenta y se muestra su cuota o estado premium (MEGA `uq`, Drive `about.get`, etc.).
- **Rotación de cuentas:** el scheduler elige la cuenta habilitada con más cuota. Una `Quota{Scope: Account}` hace saltar a la siguiente cuenta.
- **Logs:** los secretos se redactan siempre.

## 6. Rotación de IP (`internal/netx` + `internal/rotation`)

**Egresses por conexión.** Puede haber varios activos a la vez, y cada descarga se fija a uno:
- **Proxies HTTP/HTTPS/SOCKS5:**
  - Se importan desde un archivo o una URL con líneas `scheme://user:pass@host:port`. Admite proveedores residenciales con sesión fija mediante un parámetro en el usuario.
  - SOCKS5 usa `x/net/proxy` con DNS remoto (socks5h) para evitar fugas de DNS.
  - Un health check periódico deshabilita los proxies caídos y deduplica los que salen por la misma IP.
- **Tor:**
  - Usa el SOCKS en `127.0.0.1:9050`.
  - Con **IsolateSOCKSAuth**, cada descarga recibe un usuario y contraseña SOCKS distintos, y por tanto un circuito y una IP distintos al mismo tiempo.
  - `Rotate` envía `SIGNAL NEWNYM` por el puerto de control con `cretz/bine`, que también puede lanzar `tor` si se configura su ruta.
  - Aviso en la UI: MEGA y Google pueden bloquear nodos de salida.
- **WireGuard sin root:**
  - Se parsean archivos `.conf` de wg-quick (Mullvad, Proton, etc.).
  - Cada `.conf` crea un túnel en espacio de usuario con `wireguard-go` y `tun/netstack`, y `DialContext` usa su `tnet`. El DNS va por el túnel.
  - Solo el tráfico de las descargas pasa por el túnel; la red del sistema no se toca.
  - Se pueden tener varios túneles a la vez, hasta el límite de conexiones simultáneas del proveedor, que es configurable.

**Egresses globales.** Cambian la IP de toda la máquina:
- **Reconexión del router:**
  - Por UPnP IGD con `huin/goupnp`: `WANIPConnection1` o `WANPPPConnection1.ForceTermination` y después `RequestConnection`.
  - Alternativa: un script del usuario.
- **VPN del sistema:**
  - Plantillas de comandos `connect`, `rotate` y `disconnect`, por ejemplo `mullvad reconnect`, `nordvpn connect`, `protonvpn-cli c -r` o `openvpn --config X`.
  - Se ejecutan con `exec` y argv, sin shell, para que funcionen igual en los tres sistemas.
- **Seguridad:** los comandos y scripts **solo se definen en `config.toml`**. La API o la Web UI no pueden editarlos, para que la API no se convierta en un RCE.

**Algoritmo del RotationManager:**
1. Un plugin devuelve `Quota{Scope: IP, RetryAfter}`. `RetryAfter` sale del valor del hoster o de un valor por defecto por hoster.
2. El `QuotaBook` marca `(hoster, egressID)` como agotado hasta `now + RetryAfter`. La marca se guarda en SQLite para que sobreviva a reinicios.
3. Se elige otro egress **por conexión** que esté sano y no agotado para ese hoster. La estrategia es configurable: round-robin, menos usado, aleatorio o fijo. La sesión se reconstruye con ese egress, se llama de nuevo a `Prepare` y se **reanuda desde el offset**.
4. Si no queda ninguno libre y hay un egress global configurado:
   - se pausan todas las descargas;
   - se ejecuta `Rotate`;
   - se verifica que la IP pública cambió, consultando varios servicios de eco de IP;
   - se reanuda todo.
   Hay un intervalo mínimo entre reconexiones globales.
5. Si no hay ninguna opción, la descarga pasa a `waiting` con una cuenta atrás visible en la UI, como en JDownloader.
6. Según el `Scope` de la cuota:
   - `Account`: se cambia de cuenta.
   - `File` (Drive): se aplica el truco de copiar el archivo si hay OAuth; si no, se espera.

## 7. API, Web UI y CLI

- **REST** bajo `/api/v1`:
  - `links` (añadir al LinkGrabber) y `linkgrabber/confirm`.
  - `downloads`, con `pause`, `resume`, `restart`, `retry` y `delete`.
  - `packages`, con las mismas acciones para una carpeta entera y `rescan` para buscar archivos nuevos en ella.
  - `accounts`, con `check`.
  - `egress` (con `test`) y `rotation/rotate`.
  - `settings`.
- **Progreso en vivo:** `GET /api/v1/events` por SSE con progreso, velocidad, ETA, egress e IP usada, y la cuenta atrás de cuota.
- **Seguridad de la API:**
  - Escucha en `127.0.0.1` por defecto.
  - La CLI y la extensión usan un token Bearer aleatorio que se genera en el primer arranque.
  - La Web UI hace login con contraseña y recibe una cookie HttpOnly con `SameSite=Strict` y un token CSRF.
  - Se validan las cabeceras `Host` y `Origin` contra DNS rebinding.
  - Si se escucha en `0.0.0.0`, la contraseña es obligatoria y se recomienda TLS.
- **Web UI** (Svelte 5 + Vite + TS, embebida con `go:embed`). Pantallas:
  - Descargas: tabla de paquetes y archivos.
  - LinkGrabber: pegar texto, expandir carpetas, elegir destino.
  - Cuentas.
  - Red/Rotación: estado de cada egress y su IP pública, importar proxies y `.conf` de WireGuard, botón para rotar.
  - Ajustes.
- **CLI** (`cobra`): es un cliente de la API, salvo `adesc daemon`. Comandos:
  - `adesc add <url>…` (también lee enlaces de stdin), `adesc ls`, `adesc pause <id>`, `adesc resume <id>`, `adesc restart <id>` y `adesc retry <id>`;
  - `adesc accounts add mega`;
  - `adesc egress import proxies.txt` y `adesc egress test`;
  - `adesc ip`;
  - `adesc selftest <hoster>`;
  - `adesc service install`, que usa `kardianos/service` para crear el servicio de systemd, launchd o Windows.

### 7.1 Diseño visual: 5 estilos, cada uno en oscuro y claro

- **Referencia:** lienzo de diseño aprobado por el usuario, con la pantalla Descargas en los 10 diseños: https://claude.ai/artifact/ABsfSXKDarSkaPk95FJuVN. Fila de arriba: oscuro. Fila de abajo: claro. Es privado del usuario. Hay una copia local de los 10 diseños en `docs/design/` (los `.dc.html` del lienzo, con todos los valores de color); los agentes trabajan con esa copia.
- **Los 10 diseños se implementan.** La interfaz tiene dos ajustes independientes en Ajustes › Apariencia:
  - **Estilo:** `grafito` (por defecto), `terminal`, `medianoche`, `monocromo` o `enfoque`.
  - **Tema:** `sistema` (por defecto, sigue `prefers-color-scheme`), `oscuro` o `claro`.
  - **Acento:** los 4 acentos que cada estilo tiene en el lienzo. Por defecto se usa el de la tabla de tokens según el tema.

    | Estilo | Acentos |
    |---|---|
    | Grafito | `#6ea8ff`, `#1f5fd1`, `#7cc4b4`, `#0f7a68` |
    | Terminal | `#f0b45a`, `#9a5a00`, `#8fd18a`, `#2f6b2a` |
    | Medianoche | `#9aa1ff`, `#4b52d6`, `#7fd0e0`, `#1d7a8c` |
    | Monocromo | `#ffffff`, `#0a0a0a`, `#ff5a36`, `#c6ff3d` |
    | Enfoque | `#62cdb4`, `#0d6e5e`, `#f0a35e`, `#a4532a` |

    Cada acento debe cumplir 3:1 contra `--bg` y `--surface` (y 4,5:1 donde se use como texto), y `--on-accent` se elige entre blanco y negro según cuál dé más contraste. Un acento que no cumple con el tema activo (por ejemplo, `#c6ff3d` en Monocromo claro) se muestra deshabilitado con el motivo; si estaba elegido, se usa el acento por defecto de ese tema.
- **Atajos de teclado en los 5 estilos:** `p` pausar o reanudar, `r` reintentar, `R` reiniciar, `i` rotar IP, `/` buscar y `ctrl+k` para la barra de comandos (`/pausa`, `/rotar`, `/reintentar fallidas` o pegar enlaces). Actúan sobre la descarga seleccionada y no se disparan mientras se escribe en un campo. Terminal los muestra siempre en su barra inferior; los demás estilos, en una ayuda que se abre con `?`.
- **Los estilos:**

  | Estilo | Idea | Navegación | Vista de Descargas | Fuente |
  |---|---|---|---|---|
  | A · Grafito | clásico, al estilo IDM | barra lateral con velocidad, salida, IP e interruptor del portapapeles abajo | tabla con paquetes desplegables, filtros en píldoras | Inter |
  | B · Terminal | para usuarios de la CLI | enlaces de texto arriba (`[descargas]`) y barra de atajos abajo (`p`, `r`, `R`, `i`, `/`) | filas monoespaciadas con barras de caracteres (`█░`) y barra de comandos (`/rotar`, `/pausa`, `/reintentar fallidas`) | JetBrains Mono |
  | C · Medianoche | amigable | pestañas arriba | tarjetas por descarga; los errores se explican en una frase; completadas resumidas abajo | DM Sans |
  | D · Monocromo | sobrio, OLED | columna izquierda con la velocidad total en grande | lista con líneas finas; sin color de estado: los estados se distinguen por texto, cursiva, peso y tachado | Geist y Geist Mono |
  | E · Enfoque | calmado | enlaces de texto arriba | la descarga activa en grande, con velocidad, restante, descargado y salida, y debajo la lista compacta de "Después" | Manrope |

- **Tokens de color** (todos cumplen contraste WCAG AA 4,5:1 para texto; cualquier token nuevo también debe cumplirlo):

  | Estilo · tema | Fondo | Superficie | Borde | Texto | Secundario | Acento | Aviso (cuota) | Error |
  |---|---|---|---|---|---|---|---|---|
  | Grafito · oscuro | `#0e0f11` | `#111316` | `#202327` | `#e8eaed` | `#9aa0a6` | `#6ea8ff` | `#e0a64a` | `#f08a7e` |
  | Grafito · claro | `#f6f7f8` | `#ffffff` | `#e1e4e8` | `#15171a` | `#5c636a` | `#1f5fd1` | `#8f5400` | `#b3261e` |
  | Terminal · oscuro | `#0a0a09` | `#11110f` | `#1f1e1b` | `#ebe6dc` | `#8f8a80` | `#f0b45a` | `#d9c7a0` | `#f2917f` |
  | Terminal · claro | `#f4f1e9` | `#fbf9f4` | `#e2ddd0` | `#1d1b17` | `#635d52` | `#9a5a00` | `#7a5c14` | `#ad2b1b` |
  | Medianoche · oscuro | `#0b0d17` | `#12152a` | `#1d2140` | `#e9eaf5` | `#9a9fbf` | `#9aa1ff` | `#e9b26a` | `#f3958e` |
  | Medianoche · claro | `#f4f5fb` | `#ffffff` | `#e1e3ef` | `#14162b` | `#575c78` | `#4b52d6` | `#8a5300` | `#b0262a` |
  | Monocromo · oscuro | `#000000` | `#000000` | `#1c1c1c` | `#f5f5f5` | `#9b9b9b` | `#ffffff` | (sin color) | (sin color) |
  | Monocromo · claro | `#ffffff` | `#ffffff` | `#e8e8e8` | `#0a0a0a` | `#5e5e5e` | `#0a0a0a` | (sin color) | (sin color) |
  | Enfoque · oscuro | `#131211` | `#1d1b19` | `#262421` | `#efebe5` | `#a09a91` | `#62cdb4` | `#eaa860` | `#f2937f` |
  | Enfoque · claro | `#faf8f5` | `#efebe5` | `#e6e1d9` | `#1c1a17` | `#625c53` | `#0d6e5e` | `#8c4f00` | `#ae2d22` |

  El resto de valores (superficie elevada, borde fuerte, pista de la barra de progreso, pausada, completada y texto sobre el acento) está en cada diseño, en el objeto `t` de su `renderVals()` dentro de `docs/design/`.
- **Cómo se implementa en `web/`:**
  - **Tokens semánticos como variables CSS** (`--bg`, `--surface`, `--raised`, `--border`, `--border-strong`, `--text`, `--text-2`, `--muted`, `--track`, `--accent`, `--on-accent`, `--warn`, `--err`, `--paused`, `--done`, `--font`, `--font-mono`), un archivo por estilo en `web/src/theme/`, seleccionados con `data-estilo` y `data-tema` en `<html>`. Ningún componente usa un color literal.
  - **`color-scheme: dark` o `light`** según el tema, para que las barras de scroll y los controles nativos combinen.
  - **Los estilos cambian también la estructura**, no solo los colores. Se separan datos y presentación: un único store (SSE y API) y las mismas acciones; cada estilo aporta su *shell* (barra lateral, pestañas o enlaces de texto) y su vista de Descargas (tabla, filas monoespaciadas, tarjetas, lista o foco). Las piezas comunes (barra de progreso, etiqueta de estado, botón Reintentar, campo de añadir enlaces) se comparten.
  - **Fuentes empaquetadas localmente**, por ejemplo con Fontsource. Nunca desde Google Fonts: la Web UI va embebida y la app de escritorio tiene que funcionar sin internet. Las cinco son OFL.
  - **Sin parpadeo al arrancar:** la preferencia se guarda en `settings` y se copia en `localStorage`; los atributos se aplican antes de montar Svelte. En escritorio, el color de fondo de la ventana de Wails coincide con `--bg` del tema activo.
- **Reglas comunes a los 10 diseños:**
  - Un estado nunca se distingue solo por color: siempre lleva texto (Descargando, En cola, Pausada, Cuota por IP · 04:12, Fallida · cuota del archivo, Completada, "no reanudable").
  - La cuota de Google Drive por archivo se explica en la UI: "rotar la IP no ayuda", con la salida que sí sirve (conectar una cuenta de Drive para copiarlo).
  - En cada descarga activa se ven la salida (Directa, Tor, WireGuard, Proxy SOCKS5) y, en algún lugar fijo de la pantalla, la velocidad total, la IP pública y el estado del monitor del portapapeles.
  - Una carpeta es un paquete desplegable con sus archivos dentro.
  - Controles reales (`<button>`, `<a>`, `<input>` con `<label>`), `aria-label` en los botones de solo icono, objetivos táctiles de al menos 36 px (44 px en los botones principales), iconos SVG de trazo y nunca emoji.
  - Minimalismo: un solo color de acento a la vez, sin degradados ni sombras decorativas.
- **Las demás pantallas** (LinkGrabber con el árbol de carpetas, Cuentas, Red y rotación, y Ajustes) se derivan sin diseño previo: mismos tokens, fuentes y shell de cada estilo, y el mismo lenguaje que su vista de Descargas (tabla en Grafito, filas monoespaciadas en Terminal, tarjetas en Medianoche, líneas finas en Monocromo, foco en Enfoque). El usuario las revisa después y se ajustan.

## 8. Versión de escritorio (`adesc-desktop`)

- **Tecnología:** Wails, que usa el webview del sistema (WebView2 en Windows, WebKit en macOS y WebKitGTK en Linux) y reutiliza tal cual la interfaz Svelte de `web/`.
  - Se prefiere **Wails v3**, porque trae de serie bandeja del sistema, notificaciones, varias ventanas e instancia única.
  - Al empezar la fase de escritorio se comprueba si v3 ya tiene versión estable. Si no, el plan B es Wails v2 con `fyne.io/systray` para la bandeja y `gen2brain/beeep` para las notificaciones.
  - **Estado:** se implementó el plan B (Wails v2, hoy v2.17.1) porque v3 exigía Go ≥ 1.25 y el proyecto usaba Go 1.24. La migración a v3 está pendiente: ver sección 16.1.
- **Dos modos:**
  - **Integrado (por defecto):** la app arranca el núcleo dentro de su propio proceso con `internal/app`, igual que el daemon. También levanta la API en `127.0.0.1`, así que la CLI y la extensión del navegador funcionan mientras la app está abierta.
  - **Cliente remoto:** se conecta por URL y token a un `adesc daemon` de otra máquina, como un servidor o un NAS, sin arrancar un núcleo local.
- **Un solo núcleo por carpeta de datos:** un lock de archivo (`gofrs/flock`) impide que el daemon y la app abran la misma base SQLite a la vez. Si la app detecta un daemon local en marcha, ofrece conectarse a él como cliente.
- **Un solo transporte:** la interfaz siempre habla con la API por HTTP y SSE, también dentro de la app.
  - En escritorio, un único binding de Wails, `Connection()`, le entrega la URL base y el token. Así la lógica no se duplica en bindings.
  - La validación de `Origin` de la API acepta el origen del webview de Wails.
- **Funciones nativas:**
  - icono en la bandeja con la velocidad total y las acciones de pausar o reanudar todo, abrir la ventana y salir;
  - al cerrar la ventana, la app se queda en la bandeja y las descargas siguen;
  - notificaciones del sistema cuando una descarga termina, falla o entra en espera por cuota;
  - diálogos nativos para elegir la carpeta de destino e importar proxies, archivos `.conf` de WireGuard o `cookies.txt`;
  - monitor del portapapeles que detecta los enlaces copiados de MEGA, MediaFire y el resto de hosters (sección 9.2);
  - arrastrar y soltar enlaces o texto sobre la ventana;
  - autoarranque al iniciar sesión, opcional (`emersion/go-autostart`);
  - instancia única: abrir la app otra vez enfoca la ventana existente y le pasa los enlaces recibidos;
  - esquema de URL `adesc://add?url=...` para que la extensión o una web abran la app, si la versión de Wails elegida lo soporta.
- **Empaquetado:**
  - Windows: instalador NSIS que instala el runtime de WebView2 si falta.
  - macOS: `.app` universal (amd64 + arm64) dentro de un `.dmg`.
  - Linux: AppImage y `.deb`, que dependen de WebKitGTK 4.1.

## 9. Reanudar descargas, detectar enlaces copiados y descargar carpetas

### 9.1 Reanudar o reiniciar las descargas que no terminaron

- **Reanudar es lo normal.** Una descarga sin terminar sigue desde el último byte guardado, sin importar por qué se cortó:
  - una pausa manual;
  - el cierre de la app o del daemon, un apagado o un crash. Al arrancar, las descargas que estaban activas vuelven a la cola y se reanudan solas; se puede desactivar en ajustes;
  - una caída de la red. Se espera a que vuelva la conexión y se reintenta con backoff;
  - un enlace caducado. Se pide un enlace nuevo con `Prepare` y se continúa desde donde iba;
  - una cuota agotada o un cambio de IP. Igual, desde donde iba.
- **El progreso guardado nunca va por delante de lo que hay en disco.** Primero se hace `fsync` del `.part` y solo después se guardan los offsets en SQLite. Además se escribe un archivo pequeño, `archivo.part.adesc`, con el mapa de segmentos. Con él se puede recuperar la descarga aunque se pierda la base de datos o se mueva la carpeta a otro equipo.
- **Se comprueba que el archivo remoto no cambió.** Al empezar se guardan el tamaño, el `ETag`, el `Last-Modified` y el hash si el hoster lo da. Al reanudar, las peticiones llevan `If-Range`. Si el archivo cambió o el servidor ignora el `Range` y responde 200, la descarga se reinicia desde cero y se avisa al usuario.
- **Reiniciar desde cero:** el botón "Reiniciar" o `adesc restart <id>` borra el `.part` y el progreso y vuelve a descargar. Se hace solo cuando el servidor no admite `Range`, porque entonces no hay forma de continuar. La UI marca esas descargas como "no reanudable" antes de empezar.
- **Reintentar las fallidas:** el botón "Reintentar", la acción "Reintentar todas las fallidas" o `adesc retry <id>`. Antes de marcar una descarga como fallida se hacen varios reintentos automáticos; el máximo es configurable.

### 9.2 Detectar enlaces copiados (monitor del portapapeles)

- **Qué hace:** la app de escritorio vigila el portapapeles. Cuando copias texto con enlaces de MEGA, MediaFire, Google Drive, TeraBox o cualquier hoster soportado, los detecta y los manda al LinkGrabber, como hace JDownloader.
- **Cómo los reconoce:**
  - se extraen todas las URLs del texto copiado, aunque sea un párrafo entero con varios enlaces mezclados con texto;
  - cada URL se pasa por el `Match` de los plugins, así que un hoster nuevo se vuelve detectable sin tocar el monitor;
  - cada plugin reconoce los formatos antiguos y nuevos de su hoster, por ejemplo `mega.nz/#!…` y `mega.nz/file/…`.
- **Qué pasa al detectarlos** (configurable):
  - **preguntar, por defecto:** aparece una notificación como "3 enlaces de MEGA detectados" con los botones Añadir e Ignorar;
  - añadirlos directamente a la cola;
  - dejarlos en el LinkGrabber sin avisar.
- **Duplicados:** se ignoran los enlaces que ya están en la cola o en el historial, y también el mismo texto copiado dos veces.
- **Control:** el monitor se activa o se pausa desde la bandeja, y se puede elegir qué hosters detectar.
- **Privacidad:** el texto copiado solo se procesa en memoria. Nunca se guarda ni se escribe en los logs; solo se guardan los enlaces aceptados.
- **Implementación:**
  - `golang.design/x/clipboard` y su función `Watch` en Windows, macOS y Linux con X11, también bajo XWayland. Necesita cgo, que la app de escritorio ya usa;
  - en Wayland sin XWayland, `wl-paste --watch`, si está instalado el paquete `wl-clipboard`;
  - si Wails v3 ya trae eventos de portapapeles suficientes, se usan esos.
- **Sin app de escritorio:** un daemon en un servidor no tiene portapapeles. Ahí los enlaces llegan por la extensión del navegador, con el menú del clic derecho, o por `adesc add`, que también los lee de stdin.

### 9.3 Descargar carpetas completas

- **Qué se puede pegar:**
  - carpetas de MEGA, MediaFire, Google Drive y TeraBox en la ola 1;
  - carpetas de Gofile, OneDrive y Yandex, y listas de Pixeldrain, en la ola 2;
  - también una subcarpeta concreta (`mega.nz/folder/<id>#<clave>/folder/<sub>`) o un archivo dentro de una carpeta (`…/file/<id>`).
- **Cómo se lista cada hoster:**

  | Hoster | Listado |
  |---|---|
  | MEGA | Una sola llamada `{"a":"f","c":1,"r":1}` con `?n=<handle>` devuelve el árbol entero. Las rutas se reconstruyen con el padre (`p`) de cada nodo y los nombres se descifran con la clave de la carpeta. |
  | MediaFire | `folder/get_content.php` con `content_type=folders` y `content_type=files`, recursivo y paginado con `chunk`. |
  | Google Drive | Con OAuth o API key: `files.list` con `'<id>' in parents`, recursivo y paginado, incluidas las unidades compartidas. Sin credenciales se lee la vista embebida de la carpeta, que puede no listar carpetas muy grandes (verificar al implementar). |
  | TeraBox | `/share/list` con el parámetro `dir` para entrar en cada subcarpeta. |

- **Se respeta la estructura:** cada archivo lleva su `RelPath` y se guarda en `destino/<nombre de la carpeta>/<RelPath>`. La carpeta raíz se convierte en un paquete, como en JDownloader.
- **Elegir qué bajar:**
  - el LinkGrabber muestra el árbol con casillas para marcar o desmarcar archivos y subcarpetas, y el tamaño total;
  - hay filtros por extensión, por tamaño o por expresión regular, por ejemplo "solo `*.mkv`";
  - el árbol se va mostrando a medida que se lista, sin esperar a que termine.
- **Progreso:** se ve por archivo y por carpeta, con bytes y archivos completados y el tiempo restante del paquete. Pausar, reanudar o reiniciar una carpeta se aplica a todos sus archivos.
- **Archivos que ya tienes:** si un archivo ya existe en el destino con el mismo tamaño, y el mismo hash si el hoster lo da, se salta. La política es configurable: saltar, sobrescribir o renombrar.
- **Buscar archivos nuevos:** vuelve a listar la carpeta y añade solo lo que falte. Sirve para carpetas que su autor sigue ampliando.
- **Carpetas grandes:**
  - el listado se pide por páginas y con un límite de peticiones por segundo, para no provocar bloqueos del hoster;
  - el scheduler respeta los límites por hoster, así que una carpeta de 2000 archivos no abre 2000 conexiones;
  - los archivos se reparten entre los egresses disponibles, así una carpeta grande de MEGA no se queda parada cuando una IP agota su cuota.
- **Seguridad:** los nombres de archivos y carpetas vienen del hoster y no son de fiar.
  - Cada parte de la ruta se sanea: se quitan `..`, `/` y `\`, los caracteres no válidos en Windows y los nombres reservados.
  - Después se comprueba que la ruta final queda dentro de la carpeta de destino, para que una carpeta maliciosa no pueda escribir fuera de ella.

## 10. Persistencia y configuración

- **SQLite** (`modernc.org/sqlite`, sin cgo) con migraciones embebidas (`pressly/goose`). Tablas:
  - `packages` (uno por carpeta o grupo de enlaces), `downloads` (con su `rel_path` dentro de la carpeta) y `segments`;
  - `accounts`;
  - `egresses` y `quota_marks`;
  - `history`.
- **Configuración:** `config.toml` en `os.UserConfigDir()/admindescargas/` (con `pelletier/go-toml/v2`). Contiene:
  - el puerto de escucha y la carpeta de descargas, que se resuelve con `adrg/xdg` en los tres sistemas;
  - los límites de concurrencia y velocidad;
  - las definiciones de egress y la estrategia de rotación;
  - los ajustes de cada hoster, como los dominios de TeraBox y los valores de `RetryAfter`.
- **Logs:** `log/slog` en JSON o texto.

## 11. Multiplataforma desde el día 1

- **El daemon y la CLI van en Go puro con `CGO_ENABLED=0`:**
  - `modernc` para SQLite;
  - `go-keyring`, que no usa cgo en ningún sistema;
  - `wireguard-go/netstack`, que funciona igual en los tres.
- **La app de escritorio sí necesita cgo en Linux y macOS** por el webview. Por eso no se compila de forma cruzada: cada sistema se compila en su propio runner de CI. En Linux hacen falta `libgtk-3-dev` y `libwebkit2gtk-4.1-dev`.
- **Nombres de archivo:** se sanean para Windows (caracteres `<>:"/\|?*`, nombres reservados como `CON` o `PRN`, y rutas largas).
- **Rutas:** se construyen con `filepath`, nunca con separadores fijos.
- **CI en GitHub Actions** con una matriz `ubuntu`, `windows` y `macos`:
  - build de la web;
  - `go vet`;
  - `golangci-lint`;
  - `go test -race`;
  - `goreleaser --snapshot`, que genera los zip y tar.gz del daemon para linux, windows y darwin en amd64 y arm64;
  - `wails build` de la app de escritorio en cada sistema, con el instalador NSIS en Windows, el `.dmg` en macOS y el AppImage en Linux.

## 12. Fases de implementación

0. **Andamiaje:**
   - `git init` y `PLAN.md`;
   - `go mod init admindescargas` (el module path se cambia cuando exista el repo remoto);
   - el layout de carpetas;
   - `cobra`, la config, `slog` y SQLite con migraciones;
   - el Taskfile o Makefile, el CI con la matriz y `goreleaser`.
1. **Motor:**
   - el downloader segmentado con reanudación y persistencia;
   - reanudar, reiniciar desde cero y reintentar (sección 9.1), incluida la reanudación automática al arrancar;
   - el scheduler;
   - el hoster genérico;
   - `netx.Direct`;
   - `internal/app`, el arranque del núcleo, diseñado desde ya para que lo reutilice la app de escritorio;
   - `adesc daemon`, `add`, `ls`, `resume`, `restart` y `retry`.
2. **API y Web UI mínima:**
   - añadir enlaces, ver la lista con progreso por SSE, y pausar, reanudar, reiniciar o reintentar;
   - la autenticación por token y contraseña;
   - el sistema de temas de la sección 7.1 (tokens, `data-estilo`, `data-tema` y acento, fuentes locales) y la separación entre datos y presentación desde el primer componente;
   - **los 10 diseños**: los 5 estilos en oscuro y claro, con los 4 acentos de cada uno, los atajos de teclado y Ajustes › Apariencia (selector de estilo con miniaturas, tema y acento). Un agente hace el sistema de temas, los datos compartidos y Grafito; otro hace Terminal, Medianoche, Monocromo y Enfoque sobre esa base;
   - todas las pantallas (Descargas, LinkGrabber, Cuentas, Red y Ajustes) en los 5 estilos.
3. **Escritorio base:**
   - `cmd/adesc-desktop` con Wails: una ventana con la misma Web UI, sobre `internal/app`;
   - el modo integrado y el modo cliente remoto;
   - el lock de la carpeta de datos;
   - el build de escritorio en la matriz de CI de los tres sistemas.
4. **Hosters, ola 1, carpetas y detección de enlaces:**
   - MediaFire, Google Drive (público), MEGA (público, con descifrado CTR) y TeraBox (con cookie);
   - la descarga de carpetas de los cuatro (sección 9.3): árbol con casillas en el LinkGrabber, estructura de carpetas, filtros y "buscar archivos nuevos";
   - el monitor del portapapeles en la app de escritorio (sección 9.2);
   - los fixtures y `selftest`.
5. **Cuentas:**
   - el SecretStore (keyring y vault);
   - el login de MEGA con 2FA, el OAuth de Google y la importación de cookies;
   - la comprobación de cuota;
   - la rotación de cuentas.
6. **Red y rotación:**
   - proxies con health check, Tor, WireGuard netstack, reconexión UPnP o por script, y VPN por comando;
   - el QuotaBook, el RotationManager y la verificación de IP pública;
   - la pantalla de Red en la UI.
7. **Hosters, ola 2, y post-proceso:**
   - Pixeldrain, Gofile, Dropbox, OneDrive, 1fichier y Yandex, con sus carpetas y listas cuando el hoster las tenga;
   - la copia del archivo en Drive;
   - la extracción de zip, 7z (`bodgit/sevenzip`) y rar (`nwaples/rardecode/v2`), con lista de contraseñas;
   - la verificación de checksums.
8. **Integración con el escritorio:**
   - en la app: bandeja, notificaciones, diálogos nativos, arrastrar y soltar, autoarranque, instancia única y esquema `adesc://`;
   - la extensión MV3 para Chrome y Firefox: menú contextual, captura de descargas al estilo IDM y exportación de cookies de los hosters a la cuenta;
   - `adesc service install` para el daemon;
   - los instaladores: NSIS, `.dmg`, AppImage y `.deb`.

## 13. Dependencias principales (las versiones se fijan al implementar)

| Uso | Paquetes |
|---|---|
| CLI y configuración | `spf13/cobra`, `pelletier/go-toml/v2`, `adrg/xdg` |
| Persistencia | `modernc.org/sqlite`, `pressly/goose/v3` |
| Red | `golang.org/x/net/proxy`, `golang.org/x/time/rate` |
| Parseo y criptografía | `PuerkitoBio/goquery`, `golang.org/x/crypto` (pbkdf2, argon2) |
| Google Drive | `golang.org/x/oauth2`, `google.golang.org/api/drive/v3` |
| MEGA | `t3rm1n4l/go-mega`, como referencia y para el login (se reutiliza si expone el `sid`; si no, se implementa un cliente ligero propio) |
| Rotación de IP | `golang.zx2c4.com/wireguard` (`device`, `conn`, `tun/netstack`), `cretz/bine`, `huin/goupnp` |
| Credenciales y servicio | `zalando/go-keyring`, `kardianos/service` |
| Extracción | `bodgit/sevenzip`, `nwaples/rardecode/v2` |
| Tests | `things-go/go-socks5`, para levantar proxies falsos |
| Frontend | Svelte 5, Vite, TypeScript; fuentes locales (Fontsource): Inter, JetBrains Mono, DM Sans, Geist, Geist Mono y Manrope |
| Escritorio | `wailsapp/wails` (v3; plan B: v2 con `fyne.io/systray` y `gen2brain/beeep`), `emersion/go-autostart`, `gofrs/flock` |
| Portapapeles | `golang.design/x/clipboard`; `wl-paste` (`wl-clipboard`) en Wayland sin XWayland |

## 14. Verificación

- **Tests unitarios** con `go test -race ./...`:
  - el downloader contra un `httptest.Server` que simula Range, cortes, enlaces caducados (403), cuota (509) y servidores sin Range;
  - que la reanudación produce un archivo idéntico, comparando el sha256;
  - que al matar el proceso a mitad, el progreso guardado nunca va por delante de los datos en disco;
  - que un cambio de `ETag` o de tamaño en el servidor provoca un reinicio desde cero;
  - el extractor de enlaces del portapapeles con textos reales: varios hosters mezclados, formatos antiguos y nuevos, y texto sin enlaces;
  - la reconstrucción de rutas de carpetas de MEGA y MediaFire a partir de fixtures;
  - el saneado de rutas con nombres maliciosos, como `../`, `CON` o `a:b`;
  - la criptografía de MEGA con vectores conocidos: desempaquetado de la clave, atributos y CTR desde un offset arbitrario;
  - el RotationManager con egresses falsos y un reloj falso;
  - el SecretStore (vault).
- **Tests de plugins offline:** se parsean los fixtures de `testdata/`.
- **Tests de plugins en vivo:** `go test -tags live ./internal/hosters/...` con enlaces públicos de prueba y cuentas tomadas de variables de entorno.
- **Prueba de rotación end-to-end en local:**
  - un hoster falso que devuelve 509 tras X MB por IP de origen;
  - dos proxies SOCKS5 locales;
  - se comprueba que la descarga cambia de egress y termina con el checksum correcto.
- **Pruebas manuales:**
  1. Arrancar `adesc daemon`.
  2. Abrir la Web UI y añadir un enlace de MediaFire, uno de MEGA (archivo y carpeta), uno de Drive (archivo grande con aviso de virus) y uno de TeraBox con cookie.
  3. Matar el daemon a mitad de una descarga, arrancarlo de nuevo y verificar que se reanuda sola. Después usar "Reiniciar" y comprobar que empieza desde cero.
  4. Descargar una carpeta de MEGA y otra de MediaFire con subcarpetas, desmarcar una subcarpeta en el LinkGrabber y comprobar que la estructura en disco coincide con la del hoster.
  5. Con Tor instalado, comprobar que NEWNYM cambia la IP.
  6. Importar un `.conf` real de WireGuard y comprobar que `adesc ip` muestra la IP del túnel.
  7. Probar la reconexión UPnP si el router la soporta.
- **Escritorio:**
  - `wails dev` para desarrollar con recarga en caliente;
  - un smoke test en el CI de Linux con `xvfb-run`: arrancar la app, comprobar que la API integrada responde y cerrarla;
  - pruebas manuales en los tres sistemas:
    1. Añadir un enlace, cerrar la ventana y comprobar que la descarga sigue desde la bandeja y que llega la notificación al terminar.
    2. Conectar la CLI y la extensión a la app abierta.
    3. Abrir una segunda instancia y comprobar que enfoca la primera.
    4. Usar el modo cliente remoto contra un `adesc daemon`.
    5. Copiar un párrafo con enlaces de MEGA y MediaFire mezclados con texto y comprobar que la app los detecta y los ofrece en el LinkGrabber.
- **Apariencia:**
  - un test que recorre los 10 pares estilo · tema y comprueba que cada token de texto contra su fondo cumple 4,5:1;
  - un test que falla si un componente usa un color literal en vez de un token;
  - prueba manual: cambiar de estilo y de tema con descargas activas, sin perder el estado ni ver parpadeo; con el tema `sistema`, cambiar el modo del sistema operativo y comprobar que la UI lo sigue;
  - comparar cada estilo con su diseño del lienzo de la sección 7.1.
- **CI:** tiene que pasar en verde en Linux, Windows y macOS.

## 15. Riesgos y notas

- **Términos de servicio:** usar proxies o VPN para saltar las cuotas va contra los ToS de MEGA, TeraBox y otros, y pueden banear cuentas. Por eso, al descargar con una cuenta, la rotación de IP queda **desactivada por defecto** para esa cuenta: la cuota de una cuenta no depende de la IP y rotar puede marcarla.
- **Cambios en los hosters:** los hosters cambian su HTML y su API sin avisar, sobre todo TeraBox y el form de Drive. Para mitigarlo:
  - los parsers van aislados en un archivo por plugin;
  - hay fixtures grabados;
  - `selftest` permite detectar roturas pronto.
- **Proveedores de VPN:** limitan las conexiones simultáneas (por ejemplo, Proton free solo permite una), y ese límite es configurable por proveedor.
- **Tor:** es lento y suele estar bloqueado por algunos hosters. Se ofrece como último recurso dentro del pool.
- **Escritorio:**
  - Wails v3 puede seguir sin versión estable al llegar a esa fase; para eso está el plan B con v2.
  - Las distribuciones de Linux antiguas traen WebKitGTK 4.0 en vez de 4.1, y necesitan otro build.
  - En macOS, sin una cuenta de Apple Developer la app no va firmada ni notarizada, y Gatekeeper la bloquea la primera vez hasta que el usuario la autoriza.
- **Portapapeles en Linux:** en Wayland sin XWayland, una app en segundo plano solo puede leer el portapapeles con `wl-paste`, y algunos escritorios, como GNOME, no ofrecen el protocolo que necesita. En esos casos el monitor puede funcionar solo con la ventana enfocada.
- **Carpetas de Drive sin credenciales:** el listado sin OAuth ni API key es limitado. Para carpetas grandes, la app pedirá conectar una cuenta de Google o una API key.

## 16. Pendientes

### 16.1 Migrar la app de escritorio de Wails v2 a Wails v3

**Estado:** pendiente, para que lo tome otro agente.

**Por qué ahora:** la app se hizo con Wails v2 (plan B de la sección 8) porque todas las versiones de v3 exigían Go ≥ 1.25. El proyecto ya usa Go 1.27 (`go 1.27.3` en los dos `go.mod`), así que ese bloqueo desapareció. v3 trae de serie lo que con v2 se cubrió con piezas aparte.

**Situación actual (punto de partida):**
- Módulo aparte `cmd/adesc-desktop` (`replace admindescargas => ../..`), con Wails v2.17.1 y build tags `desktop,production,webkit2_41`. Su `README.md` explica la arquitectura y qué cubre cada pieza.
- Bandeja con `fyne.io/systray` (`tray*.go`, con un bucle propio en el hilo principal de AppKit en macOS: `mainthread_darwin.go`, `tray_darwin.go`).
- Notificaciones con `runtime.SendNotification*` de Wails v2 (`notifications.go`).
- Instancia única con `options.SingleInstanceLock`; `adesc://` por argumentos o `mac.Options.OnUrlOpen`.
- Monitor del portapapeles en `internal/desktop/clipboard` (`golang.design/x/clipboard` y `wl-paste`).
- La lógica que no depende de Wails vive en `internal/desktop` y se prueba con el resto del proyecto (`go test ./...`). Los tests del módulo de escritorio corren con `-tags bindings` sin GTK, o con `-tags desktop,production,webkit2_41` con GTK.
- La interfaz habla con la API por HTTP/SSE; el único binding es `Connection()`. Orígenes aceptados por la API para el webview: `wails://wails`, `http://wails.localhost`, `http://localhost:34115` (`app.WithExtraOrigins`). Con v3 cambian; hay que actualizarlos y sus tests.

**Qué hacer:**
1. Comprobar la última versión estable de Wails v3 y su versión mínima de Go.
2. Portar `cmd/adesc-desktop` a la API de v3 (`application.New`, ventanas, servicios en lugar de bindings, eventos):
   - bandeja con la bandeja nativa de v3, quitando `fyne.io/systray` y el bucle de AppKit propio si ya no hace falta;
   - notificaciones con el servicio de notificaciones de v3, manteniendo los botones «Añadir» / «Ignorar»;
   - instancia única y `adesc://` con lo que trae v3;
   - diálogos nativos, arrastrar y soltar, autoarranque, ocultar a la bandeja al cerrar y `--hidden`;
   - si v3 ofrece eventos de portapapeles suficientes, valorar usarlos (sección 9.2); si no, se queda `internal/desktop/clipboard`.
3. Valorar si el módulo aparte sigue siendo necesario. Se creó para que el `go.mod` raíz no dependiera de Wails ni de cgo; si se integra en el raíz, `go build ./...` y `CGO_ENABLED=0` del daemon tienen que seguir funcionando (build tags).
4. Actualizar los orígenes del webview en la API, el build (`wails.json` o el `Taskfile` de v3, NSIS, `.dmg`, AppImage, `.deb`, registro de `adesc://`), el job `desktop` del CI (`WAILS_VERSION`, comandos de build y el smoke test con `xvfb-run`), `make deps-update` y la documentación (`cmd/adesc-desktop/README.md`, `CONTRIBUTING.md`, secciones 8 y 13 de este plan).
5. Añadir golangci-lint del módulo de escritorio al CI, que hoy solo lintea el módulo raíz.

**Criterios de aceptación:**
- `go build`, `go vet`, `go test -race` y `golangci-lint run` en verde en los dos módulos (o en el único, si se fusionan); el daemon sigue compilando con `CGO_ENABLED=0` para linux, windows y darwin.
- La app compila y arranca en Linux; el smoke test con `xvfb-run` pasa (la API integrada responde y la app se cierra limpia).
- Se mantienen todas las funciones nativas de la sección 8 y el monitor del portapapeles de la sección 9.2, y las pruebas manuales de escritorio de la sección 14.
- Windows y macOS: como mínimo, el job de CI compila en los tres sistemas; las pruebas manuales en esos sistemas quedan anotadas si no se pueden hacer.
