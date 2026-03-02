#!/usr/bin/env bash
# install-agent.sh — Install muster-agent as a systemd service on Linux.
# Requires root. Tested on Ubuntu 20.04+, Debian 11+.
#
# Usage:
#   sudo ./install-agent.sh                         # install from local build
#   sudo ./install-agent.sh --binary /path/to/bin   # use a specific binary
#   sudo ./install-agent.sh --config /path/to/json  # copy config into place
set -euo pipefail

die() { printf 'Error: %s\n' "$1" >&2; exit 1; }
info() { printf '==> %s\n' "$1"; }

BINARY=""
CONFIG=""
SERVICE_FILE=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --binary)  BINARY="$2";       shift 2 ;;
    --config)  CONFIG="$2";       shift 2 ;;
    --service) SERVICE_FILE="$2"; shift 2 ;;
    -h|--help)
      printf 'Usage: %s [--binary PATH] [--config PATH] [--service PATH]\n' "$0"
      printf '\n  --binary   Path to muster-agent binary (default: ./muster-agent)\n'
      printf '  --config   Path to agent.json to install (copies to /etc/muster/agent.json)\n'
      printf '  --service  Path to systemd unit file (default: deploy/muster-agent.service)\n'
      exit 0 ;;
    *) die "Unknown flag: $1" ;;
  esac
done

# --- require root -----------------------------------------------------------
[[ "$(id -u)" -eq 0 ]] || die "This script must be run as root (use sudo)."

# --- resolve defaults -------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

if [[ -z "$BINARY" ]]; then
  # Look for binary next to this script, then in project root
  if [[ -f "${SCRIPT_DIR}/muster-agent" ]]; then
    BINARY="${SCRIPT_DIR}/muster-agent"
  elif [[ -f "${SCRIPT_DIR}/../muster-agent" ]]; then
    BINARY="${SCRIPT_DIR}/../muster-agent"
  else
    die "No muster-agent binary found. Build it first (make agent) or pass --binary."
  fi
fi

if [[ -z "$SERVICE_FILE" ]]; then
  SERVICE_FILE="${SCRIPT_DIR}/muster-agent.service"
fi

[[ -f "$BINARY" ]]       || die "Binary not found: $BINARY"
[[ -f "$SERVICE_FILE" ]] || die "Service file not found: $SERVICE_FILE"

# --- create user/group ------------------------------------------------------
info "Creating muster user and group..."
if ! getent group muster >/dev/null 2>&1; then
  groupadd --system muster
fi
if ! getent passwd muster >/dev/null 2>&1; then
  useradd --system --gid muster --home-dir /var/lib/muster --shell /usr/sbin/nologin muster
fi

# --- create directories -----------------------------------------------------
info "Creating directories..."
install -d -m 0755 -o muster -g muster /var/lib/muster
install -d -m 0755 /etc/muster

# --- install binary ---------------------------------------------------------
info "Installing binary to /usr/local/bin/muster-agent..."
install -m 0755 "$BINARY" /usr/local/bin/muster-agent

# --- install config (if provided) -------------------------------------------
if [[ -n "$CONFIG" ]]; then
  [[ -f "$CONFIG" ]] || die "Config file not found: $CONFIG"
  info "Installing config to /etc/muster/agent.json..."
  install -m 0640 -o root -g muster "$CONFIG" /etc/muster/agent.json
elif [[ ! -f /etc/muster/agent.json ]]; then
  info "[note] No config installed. Create /etc/muster/agent.json before starting."
  info "       See deploy/agent.json.example for reference."
fi

# --- install systemd service ------------------------------------------------
info "Installing systemd service..."
install -m 0644 "$SERVICE_FILE" /etc/systemd/system/muster-agent.service
systemctl daemon-reload

# --- enable and start -------------------------------------------------------
info "Enabling and starting muster-agent..."
systemctl enable muster-agent
systemctl start muster-agent

# --- status -----------------------------------------------------------------
printf '\n'
info "Installation complete."
printf '\n'
systemctl status muster-agent --no-pager || true
printf '\n'
info "Useful commands:"
info "  journalctl -u muster-agent -f      # follow logs"
info "  systemctl restart muster-agent      # restart"
info "  systemctl stop muster-agent         # stop"
info "  cat /etc/muster/agent.json          # view config"
