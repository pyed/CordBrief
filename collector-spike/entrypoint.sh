#!/usr/bin/env bash
set -eo pipefail

echo "=== CordBrief Collector Spike Entrypoint ==="

# 1. Prepare private runtime directory with 0700 permissions for UID 1000
export XDG_RUNTIME_DIR="/tmp/runtime-cordbrief"
mkdir -p -m 0700 "$XDG_RUNTIME_DIR"

# 2. Globally disable Electron sandbox so updater relaunches succeed in Docker
export ELECTRON_DISABLE_SANDBOX=1

# Ensure Vencord is built if dist is missing
if [ ! -f /home/cordbrief/vencord/dist/patcher.js ]; then
    echo "[Entrypoint] Building Vencord..."
    (cd /home/cordbrief/vencord && pnpm build)
fi

# 3. Official Discord configuration and endpoint
DISCORD_CONFIG_DIR="/home/cordbrief/.config/discord"
mkdir -p "$DISCORD_CONFIG_DIR"
touch "$DISCORD_CONFIG_DIR/domainMigrated"

# Ensure symlink points to latest installed app version if updated
LATEST_APP=$(ls -d "$DISCORD_CONFIG_DIR"/app-* 2>/dev/null | sort -V | tail -n 1 || true)
if [ -n "$LATEST_APP" ] && [ -x "$LATEST_APP/Discord" ]; then
    echo "[Entrypoint] Pointing Discord symlink to latest version: $LATEST_APP"
    ln -sf "$LATEST_APP/Discord" "$DISCORD_CONFIG_DIR/Discord"
fi

SETTINGS_FILE="$DISCORD_CONFIG_DIR/settings.json"
if [ ! -f "$SETTINGS_FILE" ]; then
    echo "[Entrypoint] Initializing official Discord settings.json..."
    cat << 'EOF' > "$SETTINGS_FILE"
{
  "IS_MAXIMIZED": true,
  "IS_MINIMIZED": false,
  "WEBAPP_ENDPOINT": "https://discord.com"
}
EOF
fi

export DISCORD_WEBAPP_ENDPOINT="https://discord.com"

DISPLAY_NUM="${DISPLAY:-:100}"
PORT="${XPRA_PORT:-14500}"
JOURNAL_DIR="/var/cordbrief/journal"
JOURNAL_FILE="${CORDBRIEF_JOURNAL_PATH:-/var/cordbrief/journal/discord_events.ndjson}"

# Ensure journal directory exists
mkdir -p "$JOURNAL_DIR"
touch "$JOURNAL_FILE"

# 4. Clean up stale X11 / Xpra socket and lock files
NUM="${DISPLAY_NUM#:}"
rm -f "/tmp/.X${NUM}-lock" "/tmp/.X11-unix/X${NUM}" "/home/cordbrief/.xpra/${DISPLAY_NUM}"* "$XDG_RUNTIME_DIR/xpra"* 2>/dev/null || true

# 5. Start D-Bus session bus with private runtime dir
if [ -z "$DBUS_SESSION_BUS_ADDRESS" ]; then
    eval $(dbus-launch --sh-syntax)
    echo "[Entrypoint] D-Bus session bus started at: $DBUS_SESSION_BUS_ADDRESS"
fi

# 6. Initialize and unlock GNOME Keyring Daemon for Secret Service API (libsecret)
KEYRING_DIR="/home/cordbrief/.local/share/keyrings"
mkdir -p "$KEYRING_DIR"
if [ ! -f "$KEYRING_DIR/login.keyring" ] && [ ! -f "$KEYRING_DIR/default" ]; then
    echo "[Entrypoint] Initializing default keyring..."
    printf 'cordbrief\n' | gnome-keyring-daemon --unlock --components=secrets 2>/dev/null || true
fi
eval $(printf 'cordbrief\n' | gnome-keyring-daemon --start --components=secrets 2>/dev/null) || true
echo "[Entrypoint] GNOME Keyring Secret Service active (GNOME_KEYRING_CONTROL=$GNOME_KEYRING_CONTROL)"

# 7. Configure Openbox window manager with auto-maximize rules
mkdir -p /home/cordbrief/.config/openbox
cat << 'EOF' > /home/cordbrief/.config/openbox/rc.xml
<?xml version="1.0" encoding="UTF-8"?>
<openbox_config xmlns="http://openbox.org/3.4/rc">
  <applications>
    <application class="*">
      <decor>yes</decor>
    </application>
    <application class="discord" type="normal">
      <maximized>true</maximized>
    </application>
  </applications>
</openbox_config>
EOF

echo "[Entrypoint] Starting Xpra desktop on port $PORT hosting Openbox & Discord ($DISPLAY_NUM)..."
echo "[Entrypoint] Web endpoint available at: http://127.0.0.1:$PORT/"

# 8. Launch Xpra in desktop mode, forwarding all necessary environment variables to children
exec xpra start-desktop \
    --bind-tcp="0.0.0.0:${PORT}" \
    --html=on \
    --daemon=no \
    --mdns=no \
    --webcam=no \
    --notifications=no \
    --bell=no \
    --pulseaudio=no \
    --env="DBUS_SESSION_BUS_ADDRESS=${DBUS_SESSION_BUS_ADDRESS}" \
    --env="GNOME_KEYRING_CONTROL=${GNOME_KEYRING_CONTROL}" \
    --env="XDG_RUNTIME_DIR=${XDG_RUNTIME_DIR}" \
    --env="DISCORD_WEBAPP_ENDPOINT=https://discord.com" \
    --env="ELECTRON_DISABLE_SANDBOX=1" \
    --xvfb="Xvfb -screen 0 1280x800x24 +extension GLX +extension RANDR +extension RENDER +extension Composite -nolisten tcp -noreset" \
    --start-child="openbox" \
    --start-child="discord --no-sandbox --remote-debugging-port=9222 --enable-logging" \
    "${DISPLAY_NUM}"
