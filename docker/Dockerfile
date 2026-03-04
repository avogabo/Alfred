# syntax=docker/dockerfile:1

FROM golang:1.24 AS build
WORKDIR /src
COPY . .

# Build metadata (set by CI via build args)
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
RUN go build -ldflags "-X github.com/avogabo/AlfredEDR/internal/version.Version=${VERSION} -X github.com/avogabo/AlfredEDR/internal/version.Commit=${COMMIT} -X github.com/avogabo/AlfredEDR/internal/version.Date=${BUILD_DATE}" -o /out/alfrededr ./cmd/alfrededr

# Health tool (included in image)
# Pin version for reproducible builds (avoid cache bust / upstream breakage)
RUN go install github.com/javi11/nzb-repair@v0.0.3

FROM ubuntu:jammy AS runtime
RUN apt-get update && apt-get install -y ca-certificates fuse3 curl jq rclone libglib2.0-0 par2 openjdk-17-jre-headless xz-utils mediainfo libmediainfo0v5 libmms0 locales && rm -rf /var/lib/apt/lists/*

# ngpost's bundled Qt expects OpenSSL 1.1 (libcrypto.so.1.1/libssl.so.1.1).
# Ubuntu jammy ships OpenSSL 3, so we temporarily add focal to install libssl1.1.
RUN set -e; \
  echo 'deb http://archive.ubuntu.com/ubuntu focal main' > /etc/apt/sources.list.d/focal.list; \
  apt-get update; \
  apt-get install -y --no-install-recommends libssl1.1; \
  rm -f /etc/apt/sources.list.d/focal.list; \
  rm -rf /var/lib/apt/lists/*

# ---- par2cmdline-turbo (faster PAR2 create/verify/repair) ----
FROM ubuntu:jammy AS par2build
RUN apt-get update && apt-get install -y --no-install-recommends \
    git build-essential autoconf automake libtool pkg-config ca-certificates \
    && rm -rf /var/lib/apt/lists/*
ARG PAR2_TURBO_REF=v1.4.0
RUN git clone --depth 1 --branch "${PAR2_TURBO_REF}" https://github.com/animetosho/par2cmdline-turbo.git /src/par2cmdline-turbo
WORKDIR /src/par2cmdline-turbo
RUN test -f configure || ./automake.sh
RUN ./configure --prefix=/usr/local
RUN make -j"$(nproc)"
RUN make install DESTDIR=/out

FROM runtime

RUN locale-gen en_US.UTF-8 || true
ENV LANG=en_US.UTF-8
ENV LC_ALL=en_US.UTF-8

COPY --from=build /out/alfrededr /usr/local/bin/alfrededr
COPY --from=build /go/bin/nzb-repair /usr/local/bin/nzb-repair
# Install turbo par2 tools (par2create/par2/...) overriding base package when present
COPY --from=par2build /out/usr/local/bin/ /usr/local/bin/

# ---- ngpost (bundled AppImage contents) ----
# We bundle the extracted squashfs-root to avoid system Qt deps.
ARG NGPOST_VERSION=v4.16
ARG NGPOST_ASSET=ngPost_v4.16_cmd-x86_64.AppImage
RUN mkdir -p /opt/ngpost
RUN curl -L -o /tmp/ngpost.AppImage "https://github.com/mbruel/ngPost/releases/download/${NGPOST_VERSION}/${NGPOST_ASSET}" \
    && chmod +x /tmp/ngpost.AppImage \
    && /tmp/ngpost.AppImage --appimage-extract >/dev/null \
    && mv squashfs-root/* /opt/ngpost/ \
    && rm -f /tmp/ngpost.AppImage

# Wrapper so runner can call /usr/local/bin/ngpost
RUN printf '%s\n' \
  '#!/usr/bin/env bash' \
  'set -euo pipefail' \
  'export LD_LIBRARY_PATH="/opt/ngpost/lib:${LD_LIBRARY_PATH:-}"' \
  'exec /opt/ngpost/AppRun "$@"' \
  > /usr/local/bin/ngpost \
  && chmod +x /usr/local/bin/ngpost

# ---- FileBot (portable) ----
ARG FILEBOT_VERSION=5.1.6
RUN mkdir -p /opt/filebot \
  && curl -L -o /tmp/filebot.tar.xz "https://get.filebot.net/filebot/FileBot_${FILEBOT_VERSION}/FileBot_${FILEBOT_VERSION}-portable.tar.xz" \
  && tar -xJf /tmp/filebot.tar.xz -C /opt/filebot \
  && rm -f /tmp/filebot.tar.xz \
  && chmod +x /opt/filebot/filebot.sh \
  && ln -sf /opt/filebot/filebot.sh /usr/local/bin/filebot

# ---- nyuu (Nyuu-Obfuscation fork) ----
# Source: https://github.com/Antidote2151/Nyuu-Obfuscation
# Nyuu is node-based and OPTIONAL for local builds/tests.
ARG INSTALL_NYUU=1
ARG NYUU_REPO=github:Antidote2151/Nyuu-Obfuscation
RUN if [ "$INSTALL_NYUU" = "1" ]; then \
    set -e; \
    for i in 1 2 3; do \
      echo "apt attempt $i"; \
      apt-get update && \
      apt-get install -y --no-install-recommends --fix-missing nodejs npm git python3 make g++ && \
      break; \
      sleep 5; \
    done; \
    rm -rf /var/lib/apt/lists/*; \
    npm install -g --production --unsafe-perm "$NYUU_REPO"; \
  else \
    echo "Skipping nyuu install (INSTALL_NYUU=0)"; \
  fi

COPY docker/entrypoint.sh /usr/local/bin/entrypoint-alfred.sh
RUN chmod +x /usr/local/bin/entrypoint-alfred.sh

# runtime dirs
RUN mkdir -p /config /host /cache /backups
EXPOSE 1516

ENTRYPOINT ["/usr/local/bin/entrypoint-alfred.sh"]
