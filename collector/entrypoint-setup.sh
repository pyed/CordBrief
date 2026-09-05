#!/usr/bin/env bash
set -eo pipefail

echo "=== CordBrief Setup & Authentication Tool Entrypoint ==="

# 1. Private runtime directory with 0700 permissions for UID 1000
export XDG_RUNTIME_DIR="/tmp/runtime-cordbrief"
mkdir -p -m 0700 "$XDG_RUNTIME_DIR"

# 2. Globally disable Electron sandbox so container execution succeeds
export ELECTRON_DISABLE_SANDBOX=1
export CORDBRIEF_ROLE="setup"

# 3. Exchange, private data, and runtime directory preparation
EXCHANGE_DIR="${CORDBRIEF_EXCHANGE_DIR:-/var/cordbrief/exchange}"
mkdir -p "$EXCHANGE_DIR/events"
COLLECTOR_DATA_DIR="${CORDBRIEF_COLLECTOR_DATA_DIR:-/var/lib/cordbrief}"
mkdir -p -m 0700 "$COLLECTOR_DATA_DIR"
RUNTIME_DIR="${CORDBRIEF_RUNTIME_DIR:-/var/cordbrief/runtime}"
mkdir -p "$RUNTIME_DIR"

# 4. Kernel flock(2) single ownership lease
LOCK_FILE="$RUNTIME_DIR/runtime.lock"
exec 9>"$LOCK_FILE"
if ! flock -n 9; then
    echo "[Setup] ERROR: Runtime lock is held by another process/container ($LOCK_FILE)."
    echo "[Setup] Please stop cordbrief-collector before running setup:"
    echo "  docker compose stop cordbrief-collector"
    exit 1
fi
echo "[Setup] Acquired kernel flock on $LOCK_FILE (FD 9 held by PID $$)."
cat << EOF > "$RUNTIME_DIR/runtime-owner.json"
{
  "holder": "setup",
  "pid": $$,
  "hostname": "$HOSTNAME",
  "acquired_at": "$(date -u +"%Y-%m-%dT%H:%M:%SZ")",
  "instance_nonce": "$$"
}
EOF

# 5. Official Discord configuration and endpoint
DISCORD_CONFIG_DIR="/home/cordbrief/.config/discord"
mkdir -p "$DISCORD_CONFIG_DIR"
touch "$DISCORD_CONFIG_DIR/domainMigrated"
rm -f "$DISCORD_CONFIG_DIR"/Singleton* "$DISCORD_CONFIG_DIR"/DevToolsActivePort 2>/dev/null || true

# Ensure symlink points to latest installed app version if updated
LATEST_APP=$(ls -d "$DISCORD_CONFIG_DIR"/app-* 2>/dev/null | sort -V | tail -n 1 || true)
if [ -n "$LATEST_APP" ] && [ -x "$LATEST_APP/Discord" ]; then
    echo "[Setup] Pointing Discord symlink to latest version: $LATEST_APP"
    ln -sf "$LATEST_APP/Discord" "$DISCORD_CONFIG_DIR/Discord"
fi

SETTINGS_FILE="$DISCORD_CONFIG_DIR/settings.json"
if [ ! -f "$SETTINGS_FILE" ]; then
    echo "[Setup] Initializing official Discord settings.json..."
    cat << 'EOF' > "$SETTINGS_FILE"
{
  "IS_MAXIMIZED": true,
  "IS_MINIMIZED": false,
  "WEBAPP_ENDPOINT": "https://discord.com"
}
EOF
fi

export DISCORD_WEBAPP_ENDPOINT="https://discord.com"

# Ensure Vencord is built if dist missing
if [ ! -f /home/cordbrief/vencord/dist/patcher.js ]; then
    echo "[Setup] Building Vencord from source..."
    (cd /home/cordbrief/vencord && pnpm build)
fi

# Stage runtime release if Discord app is already installed
if [ -n "$LATEST_APP" ] && [ -d "$LATEST_APP" ]; then
    echo "[Setup] Staging runtime distribution release into volume ($RUNTIME_DIR)..."
    node /home/cordbrief/stage-runtime.mjs || true
fi

DISPLAY_NUM="${DISPLAY:-:100}"
PORT="${XPRA_PORT:-14500}"

# 6. Clean up stale X11 / Xpra socket and lock files
NUM="${DISPLAY_NUM#:}"
rm -f "/tmp/.X${NUM}-lock" "/tmp/.X11-unix/X${NUM}" "/home/cordbrief/.xpra/${DISPLAY_NUM}"* "$XDG_RUNTIME_DIR/xpra"* 2>/dev/null || true

# 7. Start D-Bus session bus with private runtime dir
if [ -z "$DBUS_SESSION_BUS_ADDRESS" ]; then
    eval $(dbus-launch --sh-syntax)
    echo "[Setup] D-Bus session bus started at: $DBUS_SESSION_BUS_ADDRESS"
fi

# 8. Initialize and unlock GNOME Keyring Daemon for Secret Service API (libsecret)
KEYRING_DIR="/home/cordbrief/.local/share/keyrings"
mkdir -p "$KEYRING_DIR"
printf 'cordbrief\n' | gnome-keyring-daemon --unlock --components=secrets 2>/dev/null || true
eval $(printf 'cordbrief\n' | gnome-keyring-daemon --start --components=secrets 2>/dev/null) || true
echo "[Setup] GNOME Keyring Secret Service active (GNOME_KEYRING_CONTROL=$GNOME_KEYRING_CONTROL)"

# 9. Configure Openbox window manager with auto-maximize rules
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
      <iconic>no</iconic>
      <focus>yes</focus>
    </application>
    <application class="discord" title="*Updater*">
      <maximized>false</maximized>
      <iconic>no</iconic>
      <focus>yes</focus>
    </application>
  </applications>
</openbox_config>
EOF

echo "[Setup] Starting Xpra desktop on port $PORT hosting Openbox & Discord ($DISPLAY_NUM)..."

# 10. Launch Xpra in desktop mode, forwarding all necessary environment variables to children
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
    --env="CORDBRIEF_ROLE=setup" \
    --env="CORDBRIEF_EXCHANGE_DIR=${EXCHANGE_DIR}" \
    --env="CORDBRIEF_COLLECTOR_DATA_DIR=${COLLECTOR_DATA_DIR}" \
    --env="CORDBRIEF_RUNTIME_DIR=${RUNTIME_DIR}" \
    --xvfb="Xvfb -screen 0 1280x800x24 +extension GLX +extension RANDR +extension RENDER +extension Composite -nolisten tcp -noreset" \
    --start-child="openbox" \
    --start-child="node /home/cordbrief/supervisor.mjs" \
    "${DISPLAY_NUM}"
