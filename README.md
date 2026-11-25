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

## Instalación

No hay binarios publicados todavía: se compila desde el código. Qué hace falta:

| Herramienta | Para qué | Obligatoria |
|---|---|---|
| Git | clonar el repositorio | sí |
| Go **1.27.3 o superior** | daemon, CLI y app de escritorio | sí |
| Node 18 o superior (recomendado 22 LTS) y npm | compilar la Web UI (Vite 6) | sí, para tener interfaz web |
| GNU Make | atajos `make …` (se puede compilar sin él, ver abajo) | no |
| Compilador de C (gcc o clang) | `make test-race` y la app de escritorio en Linux y macOS | solo para eso |
| GTK 3 y WebKitGTK 4.1 (Linux) | app de escritorio | solo para la app |
| `tor`, `wl-clipboard`, el cliente de tu VPN | Tor como salida, portapapeles en Wayland, VPN del sistema | no |

El daemon y la CLI no usan cgo (`CGO_ENABLED=0`). WireGuard funciona en espacio
de usuario, así que no hace falta `wireguard-tools` ni ser root. Los `.zip`,
`.7z` y `.rar` se extraen sin programas externos.

Las versiones de Go de los repositorios de muchas distribuciones son más
antiguas que 1.27: si `go version` muestra menos de 1.27.3, instala Go desde
<https://go.dev/dl/>.

### Linux

1. **Go** (cualquier distribución; cambia `amd64` por `arm64` si hace falta):

   ```sh
   curl -fLo /tmp/go.tgz https://go.dev/dl/go1.27.4.linux-amd64.tar.gz
   sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf /tmp/go.tgz
   echo 'export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin' >> ~/.profile && . ~/.profile
   go version
   ```

2. **Herramientas de compilación y Node.** El paquete de escritorio (GTK 3 +
   WebKitGTK 4.1) solo hace falta para `adesc-desktop`:

   | Distribución | Herramientas, Node y Make | Solo para la app de escritorio |
   |---|---|---|
   | Debian 12+, Ubuntu 22.04+, Mint | `sudo apt install git make gcc nodejs npm` | `sudo apt install pkg-config libgtk-3-dev libwebkit2gtk-4.1-dev` |
   | Fedora | `sudo dnf install git make gcc nodejs npm` | `sudo dnf install pkgconf-pkg-config gtk3-devel webkit2gtk4.1-devel` |
   | Arch, Manjaro | `sudo pacman -S --needed git make gcc nodejs npm` | `sudo pacman -S --needed pkgconf gtk3 webkit2gtk-4.1` |
   | openSUSE Tumbleweed / Leap 15.6+ | `sudo zypper install git make gcc nodejs npm` | `sudo zypper install pkg-config gtk3-devel webkit2gtk3-devel` |

   Ubuntu 22.04 trae Node 12, que no sirve: instala Node 22 con
   [nvm](https://github.com/nvm-sh/nvm) (`nvm install 22`) o desde
   <https://nodejs.org>. Comprueba con `node --version` que tienes 18 o superior.

3. **Opcional:** `tor` (paquete `tor` en todas) para usar Tor como salida;
   `wl-clipboard` para detectar enlaces copiados en Wayland sin XWayland. Para
   guardar las credenciales en el llavero hace falta un servicio Secret Service
   en marcha (GNOME Keyring o KWallet); sin él se usa el vault cifrado.

### macOS (Intel y Apple Silicon)

```sh
xcode-select --install        # compilador de C, make y git
# Homebrew: https://brew.sh
brew install go node
go version && node --version
```

Opcional: `brew install tor`. La app de escritorio usa el WebKit del sistema y
no necesita nada más. Las credenciales van al Llavero de macOS.

### Windows 10 y 11

En PowerShell:

```powershell
winget install --id Git.Git
winget install --id GoLang.Go
winget install --id OpenJS.NodeJS.LTS
winget install --id ezwinports.make         # opcional, para usar make
```

Cierra y vuelve a abrir la terminal para que se actualice el `PATH`.

- Los objetivos de `make` usan sintaxis de shell POSIX: ejecútalos desde **Git
  Bash** (viene con Git), no desde PowerShell ni `cmd`.
- La app de escritorio usa WebView2, que ya viene con Windows 11. En Windows 10,
  si falta: `winget install --id Microsoft.EdgeWebView2Runtime`. No necesita
  compilador de C.
- `make test-race` sí necesita cgo y un compilador de C para Windows (por
  ejemplo, gcc de [MSYS2](https://www.msys2.org/)). Los demás tests no.
- Las credenciales van al Administrador de credenciales de Windows.

## Compilar

```sh
git clone https://github.com/Abahafart/admin-downloads.git
cd admin-downloads
make all        # = make web + make build: Web UI embebida y bin/adesc (daemon + CLI)
make desktop    # app de escritorio en cmd/adesc-desktop/build/bin/ (compila también la Web UI)
make cross      # bin/adesc-<so>-<arq> para linux, windows y darwin en amd64 y arm64
```

`make build` sin `make web` antes compila el daemon con una página provisional
en lugar de la interfaz («La interfaz web no está compilada…»). `make
web-placeholder` vuelve a dejar esa página en `web/dist`.

**Sin make** (por ejemplo, en PowerShell):

```powershell
cd web; npm ci; npm run build; cd ..
$env:CGO_ENABLED = "0"; go build -trimpath -o bin\adesc.exe .\cmd\adesc
# App de escritorio en Windows (sin ventana de consola):
go -C cmd/adesc-desktop build -tags desktop,production -ldflags "-s -w -H=windowsgui" -o build/bin/adesc-desktop.exe .
```

En Linux y macOS, sin make: `cd web && npm ci && npm run build && cd ..`,
luego `CGO_ENABLED=0 go build -trimpath -o bin/adesc ./cmd/adesc`; la app,
`go -C cmd/adesc-desktop build -tags desktop,production,gtk3 -o build/bin/adesc-desktop .`
(el tag `gtk3` solo afecta a Linux). Los paquetes `.deb`, AppImage y `.dmg` se
generan como se explica en [cmd/adesc-desktop/README.md](cmd/adesc-desktop/README.md).

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
make lint        # golangci-lint v2.15 (go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0)
make lint-desktop   # lint de la app de escritorio
cd web && npm run dev   # Web UI con recarga en caliente (proxy de /api al daemon)
```

Las pruebas de extremo a extremo (`internal/e2e`) arrancan el núcleo completo
contra servidores HTTP y proxies SOCKS5 locales: descarga genérica con sha256,
reanudación tras reiniciar, rotación por cuota 509, carpetas con subcarpetas
deseleccionadas, extracción de un zip, SSE, autenticación y secretos. No usan
la red externa; las pruebas contra hosters reales llevan la etiqueta `live`.
