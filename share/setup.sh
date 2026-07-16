#!/usr/bin/env bash
# setup.sh -- one-shot setup for a colleague's machine.
#
# Installs the gateway/gatewayd binaries (if they sit next to this
# script, i.e. you unpacked a share bundle) into ~/.local/bin, then
# installs a default board profile and an empty auto-readonly policy
# with the permissions gatewayd requires. Existing files are never
# overwritten. Safe to re-run.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
config="${XDG_CONFIG_HOME:-$HOME/.config}/ai-debug-gateway"
bin="$HOME/.local/bin"

# 1. Binaries (only when bundled next to this script).
for b in gateway gatewayd; do
  if [ -x "$here/$b" ]; then
    mkdir -p "$bin"
    if [ -e "$bin/$b" ]; then
      echo "skip: $bin/$b already exists"
    else
      cp "$here/$b" "$bin/$b"
      echo "installed: $bin/$b"
    fi
  fi
done

# 2. Config directories, owner-only as gatewayd expects.
mkdir -p "$config/profiles" "$config/policies"
chmod 700 "$config" "$config/profiles" "$config/policies"

# 3. Default board profile. Point it at the machine's USB-serial
#    adapter automatically when there is exactly one.
profile="$config/profiles/default.json"
if [ -e "$profile" ]; then
  echo "skip: $profile already exists"
else
  cp "$here/templates/default-profile.json" "$profile"
  chmod 600 "$profile"
  mapfile -t ports < <(ls /dev/serial/by-id/ 2>/dev/null || true)
  if [ "${#ports[@]}" -eq 1 ]; then
    sed -i "s|REPLACE_WITH_YOUR_SERIAL_BY_ID_PATH|/dev/serial/by-id/${ports[0]}|" "$profile"
    echo "installed: $profile (port: /dev/serial/by-id/${ports[0]})"
  else
    echo "installed: $profile"
    echo "  !! found ${#ports[@]} serial adapters; edit the Key field yourself:"
    printf '     /dev/serial/by-id/%s\n' "${ports[@]:-<none-detected>}"
  fi
fi

# 4. Empty auto-readonly policy (built-in read-only allowlist only).
policy="$config/policies/default.json"
if [ -e "$policy" ]; then
  echo "skip: $policy already exists"
else
  cp "$here/templates/default-policy.json" "$policy"
  chmod 600 "$policy"
  echo "installed: $policy"
fi

# 5. Sanity hints.
if ! id -nG | tr ' ' '\n' | grep -qx dialout; then
  echo "!! you are not in the 'dialout' group; run: sudo usermod -aG dialout $USER (then re-login)"
fi
case ":$PATH:" in
  *":$bin:"*) ;;
  *) echo "!! $bin is not on your PATH" ;;
esac

echo
echo "Done. Start with:"
echo "  gatewayd --auto-readonly     # terminal 1 (owns the serial port)"
echo "  gateway start                # terminal 2"
echo "  gateway attach"
