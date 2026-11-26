#!/usr/bin/env bash
# install-agent.sh — Install muster-agent as a systemd service on Linux.
# Requires root. Tested on Ubuntu 20.04+, Debian 11+.
#
# Usage:
#   sudo ./install-agent.sh --token mst_agent_... --name prod-1 --project /opt/app
#   sudo ./install-agent.sh --binary /path/to/bin --token mst_agent_...
#   sudo ./install-agent.sh --config /path/to/json  # copy an existing (enrolled) config
set -euo pipefail

die() { printf 'Error: %s\n' "$1" >&2; exit 1; }
info() { printf '==> %s\n' "$1"; }

BINARY=""
CONFIG=""
SERVICE_FILE=""
JOIN_TOKEN="${MUSTER_AGENT_JOIN_TOKEN:-}"
RELAY="wss://relay.getmuster.dev"
NAME=""
PROJECT=""
MODE="muster"
AGENT_CONFIG="/var/lib/muster/agent/config.json"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --binary)  BINARY="$2";       shift 2 ;;
    --config)  CONFIG="$2";       shift 2 ;;
    --service) SERVICE_FILE="$2"; shift 2 ;;
    --token)   JOIN_TOKEN="$2";   shift 2 ;;
    --relay)   RELAY="$2";        shift 2 ;;
    --name)    NAME="$2";         shift 2 ;;
    --project) PROJECT="$2";      shift 2 ;;
    --mode)    MODE="$2";         shift 2 ;;
    -h|--help)
      printf 'Usage: %s [--token mst_agent_...] [--relay URL] [--name NAME] [--project DIR] [--mode muster|push]\n' "$0"
      printf '          [--binary PATH] [--config PATH] [--service PATH]\n'
      printf '\n  --token    One-time join token; enrolls the agent (env MUSTER_AGENT_JOIN_TOKEN)\n'
      printf '  --relay    Relay URL (default: wss://relay.getmuster.dev)\n'
      printf '  --name     Agent name (default: join token machine name or hostname)\n'
      printf '  --project  Project directory on this machine\n'
      printf '  --binary   Path to muster-agent binary (default: ./muster-agent)\n'
      printf '  --config   Existing enrolled config to install (instead of --token)\n'
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
install -d -m 0750 -o muster -g muster /var/lib/muster
install -d -m 0700 -o muster -g muster /var/lib/muster/agent
install -d -m 0755 /etc/muster

# --- install binary ---------------------------------------------------------
info "Installing binary to /usr/local/bin/muster-agent..."
install -m 0755 "$BINARY" /usr/local/bin/muster-agent

# --- enroll / install config -------------------------------------------------
if [[ -n "$JOIN_TOKEN" ]]; then
  info "Enrolling with relay ${RELAY}..."
  join_args=(join --config "$AGENT_CONFIG" --relay "$RELAY" --token "$JOIN_TOKEN" --mode "$MODE")
  [[ -n "$NAME" ]] && join_args+=(--name "$NAME")
  [[ -n "$PROJECT" ]] && join_args+=(--project "$PROJECT")
  # Run as the service user so the node credential is owned by it (0600).
  runuser -u muster -- /usr/local/bin/muster-agent "${join_args[@]}" \
    || die "Enrollment failed (is the join token valid and unused?)"
elif [[ -n "$CONFIG" ]]; then
  [[ -f "$CONFIG" ]] || die "Config file not found: $CONFIG"
  info "Installing config to ${AGENT_CONFIG}..."
  install -m 0600 -o muster -g muster "$CONFIG" "$AGENT_CONFIG"
elif [[ ! -f "$AGENT_CONFIG" ]]; then
  die "No join token given and no config at ${AGENT_CONFIG}. Pass --token mst_agent_..."
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
info "  muster-agent status --config ${AGENT_CONFIG}"
