#!/bin/zsh
set -euo pipefail

umask 077
backup_dir="${HERMES_BACKUP_DIR:-$HOME/Backups/Hermes}"
retention_days="${HERMES_BACKUP_RETENTION_DAYS:-14}"
hermes_bin="${HERMES_BIN:-$HOME/.local/bin/hermes}"
timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
archive="$backup_dir/hermes-backup-$timestamp.zip"
checksum="$archive.sha256"

mkdir -p "$backup_dir"
chmod 700 "$backup_dir"

"$hermes_bin" backup --output "$archive"
chmod 600 "$archive"
/usr/bin/unzip -tq "$archive" >/dev/null
/usr/bin/shasum -a 256 "$archive" > "$checksum"
chmod 600 "$checksum"

/usr/bin/find "$backup_dir" -type f \( -name 'hermes-backup-*.zip' -o -name 'hermes-backup-*.zip.sha256' \) -mtime "+$retention_days" -delete

printf '{"status":"ok","archive":"%s","checksum":"%s","retention_days":%s}\n' "$archive" "$checksum" "$retention_days"
