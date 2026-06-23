#!/usr/bin/env bash
set -euo pipefail

PROJECT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LOG_DIR="${1:-$PROJECT_DIR/logs}"
PATTERN='ERR|ERROR|error|panic|Panic|Traceback|Exception|fatal|Fatal'

if [[ ! -d "$LOG_DIR" ]]; then
  echo "Log directory does not exist: $LOG_DIR"
  exit 1
fi

log_files=()
while IFS= read -r file; do
  log_files+=("$file")
done < <(
  find "$LOG_DIR" -type f \( -name '*.log' -o -name '*.out' \) -print0 |
    xargs -0 ls -t 2>/dev/null |
    head -20
)

if [[ ${#log_files[@]} -eq 0 ]]; then
  echo "No log files found under $LOG_DIR"
  exit 0
fi

echo "Scanning latest ${#log_files[@]} log files under $LOG_DIR"
echo

matches=0
for file in "${log_files[@]}"; do
  if rg -n --color=never "$PATTERN" "$file" >/tmp/dlapi-error-matches.$$ 2>/dev/null; then
    matches=1
    echo "==> $file"
    tail -40 /tmp/dlapi-error-matches.$$
    echo
  fi
done

rm -f /tmp/dlapi-error-matches.$$

if [[ "$matches" -eq 0 ]]; then
  echo "No recent errors found."
fi
