# AdminDescargas

Gestor de descargas multi-hoster en Go, al estilo de JDownloader / IDM:
MEGA, MediaFire, Google Drive, TeraBox, Pixeldrain, Gofile, Dropbox, OneDrive,
1fichier, Yandex Disk y enlaces HTTP genéricos; cuentas, carpetas completas con
su estructura, reanudación tras cortes o reinicios, detección de enlaces
copiados, extracción de comprimidos y rotación de IP (proxies HTTP/SOCKS5, Tor,
WireGuard en espacio de usuario, reconexión del router y VPN del sistema).

El diseño completo y las fases están en [PLAN.md](PLAN.md). Las convenciones de
código y quién es dueño de cada paquete, en [CONTRIBUTING.md](CONTRIBUTING.md).

Hay tres formas de usarlo, todas sobre el mismo núcleo (`internal/app`):

- **`adesc daemon`**: API REST + SSE y la Web UI embebida (servidores, NAS, uso remoto);
- **`adesc <comando>`**: la CLI, cliente de esa API;
- **`adesc-desktop`**: la app de escritorio (Wails), ver [cmd/adesc-desktop/README.md](cmd/adesc-desktop/README.md).

## Requisitos

- Go 1.27 (el daemon y la CLI se compilan con `CGO_ENABLED=0`).
- Node 18 o superior y npm para la Web UI (Vite 6; con Node 22 también funciona).
- Solo para la app de escritorio: cgo, Wails v2 y WebKitGTK 4.1 en Linux.

## Compilar

```sh
make web        # compila la Web UI en web/dist (opcional: sin ella se sirve una página provisional)
make build      # genera bin/adesc (daemon + CLI, sin cgo)
make cross      # binarios para linux/windows/darwin en amd64 y arm64
make web-placeholder   # vuelve a dejar la página provisional en web/dist
```

## Arrancar el daemon

```sh
./bin/adesc daemon                     # usa <config del usuario>/admindescargas/config.toml
./bin/adesc --config ./config.toml daemon
```

La primera ejecución crea `config.toml` en la carpeta de configuración del
usuario (`~/.config/admindescargas/` en Linux, `~/Library/Application Support/admindescargas/`
en macOS, `%AppData%\admindescargas\` en Windows) con un token de API aleatorio.
El daemon escucha en `http://127.0.0.1:7979`; la Web UI está en esa misma
dirección. Solo puede haber un núcleo por carpeta de datos (si la app de
escritorio ya está abierta, la CLI habla con ella).

Para instalarlo como servicio (systemd, launchd o el SCM de Windows):
`adesc service install` y `adesc service start`.

### Credenciales de las cuentas

Las contraseñas, cookies, claves de API y tokens OAuth nunca van a SQLite ni a
los logs: se guardan en el llavero del sistema o, si no hay (servidores sin
sesión gráfica), en un vault cifrado (`secrets.vault`, AES-256-GCM con clave
Argon2id). El vault arranca **bloqueado**: desbloquéalo desde la Web UI o con
`adesc secrets unlock` (la primera vez eliges la contraseña maestra), o pon
`vault_password_file` en `[secrets]` para servicios que arrancan solos.

### Acceso remoto

Fuera de `127.0.0.1` hace falta contraseña para la Web UI:
`adesc password` guarda su hash Argon2id en `server.password_hash`. La CLI, la
extensión del navegador y la app de escritorio usan siempre el token
(`Authorization: Bearer <server.token>`).

## Ejemplo de `config.toml`

Todas las claves son opcionales: lo que falta toma el valor por defecto.

```toml
data_dir = "/var/lib/admindescargas"     # SQLite, vault, lock
download_dir = "/srv/descargas"

[server]
listen = "127.0.0.1:7979"
token = "…generado en el primer arranque…"
# password_hash = "$argon2id$…"          # adesc password (obligatorio fuera de loopback)
# allowed_origins = ["https://descargas.midominio"]   # orígenes extra (CORS)

[downloads]
max_active = 3          # descargas simultáneas
max_per_hoster = 2
max_per_egress = 4      # >= max_active para no limitar con una sola salida
conns_per_file = 8
speed_limit = 0         # bytes/s, 0 = sin límite
max_retries = 5
resume_on_start = true
exists_policy = "skip"  # skip | overwrite | rename

[secrets]
backend = "auto"        # auto | keyring | vault | memory
# vault_password_file = "/etc/admindescargas/vault.pass"

[rotation]
enabled = true
strategy = "round-robin"   # round-robin | least-used | random | fixed
global_rotation = false    # permitir router / VPN del sistema (pausa todas las descargas)
min_global_interval = "5m"
default_retry_after = "1h"

# --- Salidas de red (egress) ---
[egress]
direct = true              # la conexión propia del equipo
health_interval = "5m"

[egress.proxies]
list = ["socks5://usuario:clave@203.0.113.10:1080", "http://198.51.100.20:3128"]
files = ["/etc/admindescargas/proxies.txt"]   # una entrada por línea
dedupe_by_ip = true

[egress.tor]
enabled = false
socks_addr = "127.0.0.1:9050"
control_addr = "127.0.0.1:9051"
circuits = 4

[[egress.wireguard]]
name = "proton"
confs = ["/etc/wireguard/proton-*.conf"]   # túneles en espacio de usuario, sin root
max_simultaneous = 1

[egress.router]
enabled = false
method = "upnp"            # upnp | script
# script = ["/usr/local/bin/reconectar-router"]   # argv, sin shell

[[egress.system_vpn]]
name = "mullvad"
connect = ["mullvad", "connect"]
rotate = ["mullvad", "reconnect"]
disconnect = ["mullvad", "disconnect"]

# --- Hosters ---
[hosters.gdrive.options]
client_id = "…apps.googleusercontent.com"   # tu cliente OAuth de tipo «Aplicación de escritorio»
client_secret = "…"
copy_on_quota = "true"

[hosters.terabox]
retry_after = "30m"

[postprocess]
verify_checksums = true
extract = true
passwords = ["contraseña-habitual"]
delete_archives = false
```

Los comandos del router y de la VPN solo se leen de este archivo: la API nunca
permite cambiarlos. Los proxies y los `.conf` de WireGuard también se pueden
importar desde la Web UI o con `adesc egress import`.

## Uso de la CLI

```sh
adesc add https://mega.nz/file/...          # al LinkGrabber (también lee texto de stdin)
adesc add --start https://mega.nz/folder/...   # directo a la cola
adesc grabber ls                            # enlaces pendientes de confirmar
adesc grabber confirm --all
adesc ls                                    # descargas
adesc pause 3 4; adesc resume 3; adesc retry 5; adesc restart 5; adesc rm 6
adesc accounts add mega --user yo@ejemplo.com      # pide la contraseña por stdin
adesc accounts import-cookies terabox cookies.txt
adesc accounts check
adesc accounts oauth-complete 'http://127.0.0.1:7979/api/v1/oauth/google/callback?state=…'
adesc secrets status; adesc secrets unlock; adesc secrets lock; adesc secrets passwd
adesc egress ls; adesc egress import proxies.txt; adesc egress test; adesc egress rotate
adesc selftest mega mediafire               # comprueba los plugins contra enlaces públicos
adesc --json ls                             # salida JSON para scripts
adesc --help                                # todos los comandos
```

Con `--url` y `--token` la CLI habla con un daemon de otra máquina.

## Desarrollo

```sh
make test        # go test ./...
make test-race   # con el detector de carreras (necesita cgo)
make lint        # golangci-lint v2
cd web && npm run dev   # Web UI con recarga en caliente (proxy de /api al daemon)
```

Las pruebas de extremo a extremo (`internal/e2e`) arrancan el núcleo completo
contra servidores HTTP y proxies SOCKS5 locales: descarga genérica con sha256,
reanudación tras reiniciar, rotación por cuota 509, carpetas con subcarpetas
deseleccionadas, extracción de un zip, SSE, autenticación y secretos. No usan
la red externa; las pruebas contra hosters reales llevan la etiqueta `live`.
