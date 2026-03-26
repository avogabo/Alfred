# AlfredEDR

AlfredEDR convierte **NZBs → una biblioteca FUSE** con MKVs “virtuales” que se **descargan on‑demand**, e incluye una **UI web** para **Importar / Subir **.
Pensado para que **Plex apunte a `library-auto`**.

## Quickstart (Docker Compose - recomendado)

> Este es el despliegue canónico para todos los entornos (Unraid, Portainer, Docker host, etc.).

```yaml
services:
  alfrededr:
    image: ghcr.io/avogabo/alfred:latest
    container_name: alfrededr
    restart: unless-stopped
    ports:
      - "1516:1516"
    cap_add:
      - SYS_ADMIN
    devices:
      - /dev/fuse:/dev/fuse
    security_opt:
      - apparmor:unconfined
    volumes:
      - /tu/ruta/a/alfrededr/config:/config
      - /tu/ruta/a/alfrededr/cache:/cache
      - /tu/ruta/a/alfrededr/backups:/backups
      - /tu/ruta/a/tu-biblioteca-host:/host:rshared
```

UI: `http://<HOST>:1516/webui/`

### Validación rápida tras levantar

```bash
docker inspect alfrededr --format '{{range .Mounts}}{{println .Destination "->" .Propagation}}{{end}}'
```

Debe mostrar `/host -> rshared`.

## Volúmenes / Paths

- `/config`: `config.json` + SQLite
- `/host:rshared`: inbox + mounts FUSE (donde se monta `inbox` y `mount`; en `inbox` van las watch `media` y `nzb`, y en `mount` quedan los montajes para Plex/Kodi/Jellyfin/Infuse).
- `/cache`: staging + cache + backups locales de Health
- `/backups`: backups

## Funciones (UI)

- **Biblioteca**: navegar por la libreria
- **Subida**: upload (nyuu obfuscation) → NZB (+ PAR2 local opcional)
- **Importar**: importar NZBs → aparecen MKVs virtuales
- **Ajustes**: config + restart
- **Logs**: logs de jobs

## Ajustes recomendados (rutas vigiladas + flujo)

En **Ajustes (Settings)** tienes dos carpetas vigiladas:

- **Watch media** (`watch.media.dir`): recomienda poner aquí **tus descargas** (donde caen los releases antes de subir/importar).
  - Ejemplo: `/host/inbox/media`
- **Watch NZB** (`watch.nzb.dir`): recomienda montar aquí tu **origen de NZBs** (por ejemplo OneDrive con los NZBs del grupo EDR).
  - Ejemplo: `/host/inbox/nzb`

> Idea: *media* = “lo que vas a procesar/subir”; *nzb* = “la cola/entrada de NZBs”.

## Library (reglas tipo Filebot)

La vista `library` se construye con plantillas configurables (estilo Filebot). Falta por completar la UI para editar todas las reglas, pero el backend ya soporta:

- `library.movie_dir_template`, `library.movie_file_template`
- `library.series_dir_template`, `library.season_folder_template`, `library.series_file_template`

Puedes personalizarlas en `config.json` (o vía UI cuando esté completa) para adaptar tu estructura de biblioteca.

## Primer arranque (first install)

Si `/config/config.json` no existe, AlfredEDR crea un **config.json mínimo** (sin secretos) para que el contenedor pueda arrancar y luego termines la configuración desde la UI.
