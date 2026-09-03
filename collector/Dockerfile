# CordBrief Collector Service Dockerfile
# Production-shaped Discord Desktop + Xpra + Vencord Collector

FROM debian:bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive
ENV LANG=en_US.UTF-8
ENV LC_ALL=en_US.UTF-8

# 1. Base packages & certificates
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    gnupg \
    procps \
    git \
    && rm -rf /var/lib/apt/lists/*

# 2. Xpra official repository
RUN mkdir -p /usr/share/keyrings /etc/apt/sources.list.d \
    && curl -fsSL https://xpra.org/xpra.asc -o /usr/share/keyrings/xpra.asc \
    && curl -fsSL https://raw.githubusercontent.com/Xpra-org/xpra/master/packaging/repos/bookworm/xpra.sources -o /etc/apt/sources.list.d/xpra.sources

# 3. Node.js official repository
RUN mkdir -p /etc/apt/keyrings \
    && curl -fsSL https://deb.nodesource.com/gpgkey/nodesource-repo.gpg.key | gpg --dearmor -o /etc/apt/keyrings/nodesource.gpg \
    && echo "deb [signed-by=/etc/apt/keyrings/nodesource.gpg] https://deb.nodesource.com/node_22.x nodistro main" > /etc/apt/sources.list.d/nodesource.list

# 4. Install X11/Xpra runtime dependencies, Openbox window manager, and Node.js
RUN apt-get update && apt-get install -y --no-install-recommends \
    xvfb \
    xpra \
    xpra-x11 \
    xpra-html5 \
    openbox \
    xdotool \
    zenity \
    python3-pil \
    dbus \
    dbus-x11 \
    gnome-keyring \
    libsecret-1-0 \
    libsecret-tools \
    libasound2 \
    libatk-bridge2.0-0 \
    libatk1.0-0 \
    libcairo2 \
    libcups2 \
    libdbus-1-3 \
    libdrm2 \
    libgbm1 \
    libglib2.0-0 \
    libgtk-3-0 \
    libnspr4 \
    libnss3 \
    libpango-1.0-0 \
    libx11-6 \
    libx11-xcb1 \
    libxcb1 \
    libxcomposite1 \
    libxdamage1 \
    libxext6 \
    libxfixes3 \
    libxrandr2 \
    libxshmfence1 \
    libxss1 \
    libxtst6 \
    xdg-utils \
    nodejs \
    && corepack enable \
    && corepack prepare pnpm@latest --activate \
    && rm -rf /var/lib/apt/lists/*

# 5. Download and install official Discord Desktop Linux .deb package
RUN curl -fsSL "https://discord.com/api/download?platform=linux&format=deb" -o /tmp/discord.deb \
    && apt-get update \
    && apt-get install -y --no-install-recommends /tmp/discord.deb \
    && rm -f /tmp/discord.deb \
    && rm -rf /var/lib/apt/lists/*

# 6. Create unprivileged runtime user 'cordbrief'
RUN useradd -u 1000 -m -s /bin/bash cordbrief \
    && mkdir -p /home/cordbrief/.config/discord \
    && mkdir -p /home/cordbrief/.local/share/keyrings \
    && mkdir -p /home/cordbrief/.xpra \
    && mkdir -p /home/cordbrief/.config/openbox \
    && mkdir -p /var/cordbrief/exchange \
    && chown -R cordbrief:cordbrief /home/cordbrief /var/cordbrief

# 7. Clone Vencord, integrate CordBriefCollector userplugin, and pre-build
USER cordbrief
WORKDIR /home/cordbrief

RUN git clone --depth 1 https://github.com/Vendicated/Vencord.git /home/cordbrief/vencord \
    && cd /home/cordbrief/vencord \
    && pnpm install

COPY --chown=cordbrief:cordbrief plugin/ /home/cordbrief/vencord/src/userplugins/cordbriefCollector/

RUN cd /home/cordbrief/vencord \
    && pnpm build

USER root
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

USER cordbrief
WORKDIR /home/cordbrief

EXPOSE 14500

VOLUME ["/home/cordbrief/.config/discord", "/home/cordbrief/.local/share/keyrings", "/var/cordbrief/exchange"]

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
