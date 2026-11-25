# Extensión del navegador (Manifest V3)

Extensión para Chrome/Chromium (Edge, Brave…) y Firefox que conecta el navegador con
AdminDescargas (el daemon `adesc daemon` o la app de escritorio):

- **Menú contextual** «Descargar con AdminDescargas» sobre enlaces, texto
  seleccionado, la página, imágenes, vídeos y audio. Los enlaces se envían al
  LinkGrabber (o directamente a la cola, si lo activas en las opciones).
- **Captura de descargas al estilo IDM** (desactivada por defecto): cancela en el
  navegador las descargas que cumplen tus reglas y se las pasa al daemon.
- **Exportar cookies** de un hoster soportado a una cuenta de AdminDescargas, solo
  cuando pulsas el botón.
- **Icono con contador**: el número de descargas activas (`!` en rojo si el daemon
  no responde o el token es incorrecto).
- **Ventana emergente** con el estado, los contadores y el botón «Enviar página actual».

Está escrita en JavaScript plano (módulos ES), sin dependencias ni paso de build.

## Instalación

### Chrome, Chromium, Edge, Brave

1. Abre `chrome://extensions` (en Edge, `edge://extensions`).
2. Activa el **Modo de desarrollador** (arriba a la derecha).
3. Pulsa **Cargar descomprimida** y elige la carpeta `extension/` del repositorio.
4. Abre las opciones de la extensión (se abren solas desde la ventana emergente,
   botón «Opciones»).

### Firefox (128 o superior)

1. Abre `about:debugging#/runtime/this-firefox`.
2. Pulsa **Cargar complemento temporal…** y elige `extension/manifest.json`.
3. El complemento temporal desaparece al cerrar Firefox. Para instalarlo de forma
   permanente hay que firmarlo en addons.mozilla.org (también como «no listado») con
   `web-ext sign`; Firefox Developer Edition/Nightly permiten desactivar la firma
   (`xpinstall.signatures.required = false`).
4. En Firefox MV3 los permisos de host son opcionales: al pulsar **Guardar** en las
   opciones se pide el permiso para la dirección del daemon. También puedes darlo en
   `about:addons` › AdminDescargas › Permisos.

## Configuración

En la página de opciones:

- **URL del daemon**: por defecto `http://127.0.0.1:7979` (el `server.listen` por
  defecto). Si usas un daemon en otra máquina (`https://nas.lan:8443`), al guardar el
  navegador te pedirá permiso para esa dirección.
- **Token de la API**: el valor de `[server].token` en `config.toml`
  (`~/.config/admindescargas/config.toml` en Linux; se genera en el primer arranque).
  Se guarda en `storage.local` de la extensión, solo en este navegador, y nunca se
  escribe en la consola.
- **Probar conexión**: llama a `GET /api/v1/health` y a `GET /api/v1/stats`
  (este último comprueba el token).
- **Origen de la extensión**: la página lo muestra (`chrome-extension://<id>` o
  `moz-extension://<uuid>`). Si el daemon rechaza las peticiones con 403, añádelo a
  `server.allowed_origins` en `config.toml`.

## Captura de descargas

Cuando está activada, cada descarga nueva del navegador se compara con las reglas:

| Regla | Comportamiento |
|---|---|
| Extensiones | Lista separada por comas o espacios (`zip, rar, 7z, mkv…`). Vacía = cualquiera. `001` cubre también `002`, `003`… |
| Tamaño mínimo (MB) | Solo se aplica si el navegador conoce el tamaño. |
| Solo estos dominios | Vacío = todos. `example.com` incluye sus subdominios; `/regex/` es una expresión regular sobre el host. |
| Excluir estos dominios | Igual que la anterior, con prioridad sobre ella. |
| Ventanas privadas | Por defecto no se capturan. |

Nunca se capturan las URLs `blob:`/`data:`, las descargas servidas por el propio
daemon ni las que la extensión vuelve a lanzar. Si el envío falla, la descarga se
relanza en el navegador.

Limitaciones: el daemon descarga la URL sin las cookies ni la sesión del navegador,
así que las descargas que dependen de una sesión iniciada, de un formulario POST o de
un `Referer` pueden fallar en el daemon. Para esos hosters, exporta antes las cookies
a una cuenta (siguiente apartado) o excluye su dominio. En Chrome la decisión se toma
en `downloads.onDeterminingFilename` (ya se conoce el nombre real del archivo); en
Firefox, en `downloads.onCreated` (solo la URL y el tipo MIME).

## Exportar cookies a una cuenta

Hosters disponibles: MEGA, TeraBox (y sus dominios espejo), MediaFire, Google Drive,
Pixeldrain, Gofile, 1fichier, Dropbox, OneDrive y Yandex Disk.

1. Inicia sesión en el hoster en este navegador.
2. En las opciones, elige el hoster, pon un nombre a la cuenta y pulsa **Exportar
   cookies**.
3. El navegador pide en ese momento el permiso `cookies` y el acceso a los dominios
   de ese hoster. Sin ese permiso no se lee nada.
4. Las cookies se envían a `POST /api/v1/accounts/import-cookies` y, si la casilla
   está marcada (lo está por defecto), el permiso se retira al terminar.

Las cookies se envían en formato **Netscape `cookies.txt`** (el mismo que acepta
«Importar cookies» de la app y `accounts.Secret.Cookies`): una línea por cookie con
`dominio, subdominios, ruta, secure, caducidad (epoch, 0 = de sesión), nombre, valor`
separados por tabuladores; las cookies HttpOnly llevan el prefijo `#HttpOnly_`.

Las cookies de Google, Microsoft y Yandex dan acceso a toda la cuenta del proveedor,
no solo al almacenamiento: exporta solo a un daemon de confianza y, si es remoto,
usa HTTPS.

## Privacidad

- El token y la configuración se guardan solo en `storage.local` del navegador.
- La extensión solo habla con la URL del daemon que configures.
- Las cookies se leen únicamente al pulsar «Exportar cookies», del hoster elegido y
  con un permiso pedido en ese momento.
- La captura de descargas está desactivada por defecto y no actúa en ventanas
  privadas salvo que lo cambies.

## Permisos

| Permiso | Para qué |
|---|---|
| `contextMenus` | El menú «Descargar con AdminDescargas». |
| `storage` | Guardar URL, token y reglas. |
| `alarms` | Actualizar el contador del icono cada minuto. |
| `notifications` | Avisar de envíos correctos o fallidos. |
| `downloads` | Captura de descargas (cancelar en el navegador y relanzar si falla). |
| `activeTab` | Leer la URL de la pestaña al pulsar «Enviar página actual». |
| `http://127.0.0.1/*`, `http://localhost/*` | Hablar con el daemon local. |
| Opcional: `cookies` + dominios del hoster | Solo durante «Exportar cookies». |
| Opcional: otros orígenes (`<all_urls>`) | Solo se pide el origen concreto de un daemon remoto o de un hoster. |

## API usada

Todas las peticiones llevan `Authorization: Bearer <token>`:

| Método y ruta | Cuerpo / uso |
|---|---|
| `POST /api/v1/links` | `{"urls": [...], "source": "extension"}` para enlaces, medios, la página y las descargas capturadas; `{"text": "...", "source": "extension"}` para texto seleccionado. Con «Añadir directamente a la cola», también `"auto_confirm": true`. |
| `GET /api/v1/stats` | `{"active", "queued", "waiting", "failed", "completed", "grabber_size", "speed", "paused"}` para el icono y la ventana emergente. |
| `GET /api/v1/health` | Botón «Probar conexión». |
| `POST /api/v1/accounts/import-cookies` | `{"hoster": "<plugin>", "label": "...", "cookies": "<cookies.txt>"}`; `hoster` es el nombre del plugin (`mega`, `terabox`, `mediafire`, `gdrive`, `pixeldrain`, `gofile`, `onefichier`, `dropbox`, `onedrive`, `yandex`). |

Las peticiones salen de páginas de la extensión con permiso de host, así que no
dependen de CORS, pero el navegador envía la cabecera `Origin` de la extensión: el
daemon debe aceptarla (ver «Origen de la extensión» arriba).

## Desarrollo

- `lib/core.js`: lógica pura (reglas de captura, URLs, `cookies.txt`, tabla de
  hosters), compartida con los tests de Node.
- `lib/api.js`: cliente de la API, ajustes y contador del icono.
- `background.js`: menú contextual, captura y alarma.
- `options.html/js`, `popup.html/js`, `style.css`.
- `tools/make_icons.py`: regenera los iconos (`python3 tools/make_icons.py icons`).

Tests (Node 18 o superior, sin dependencias) desde la raíz del repositorio:

```sh
node --test extension/test
```

Comprueban la lógica de `lib/core.js`, que el manifiesto es válido para Chrome y
Firefox, que existen los archivos referenciados y que ningún script registra el token
ni las cookies.
