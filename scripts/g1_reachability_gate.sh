#!/usr/bin/env bash
# G1 reachability gate (ares-repair-plan-zh.md §8): every internal package
# must be reachable from a production entrypoint (cmd/ares, sdk, services,
# api) unless whitelisted here. Prevents "built but never wired" packages
# from re-entering the tree unnoticed.
set -euo pipefail

# Packages allowed to be unreachable (experimental / SDK-only / offline tools).
WHITELIST=(
  "internal/ares_integration"     # pure test package
  "internal/knowledge/provider/postgres" # examples-only (D6 verified)
  "internal/knowledge/retriever"        # 0 imports (D6 verified)
  "internal/knowledge/service"          # examples-only (D6 verified)
  "internal/knowledge/workflow"         # examples-only (D6 verified)
  "internal/fabric"                    # parent package (doc.go only); sub-packages are reachable
)

cd "$(dirname "$0")/.."

prod_deps=$(go list -deps ./cmd/ares/... ./sdk/... ./services/... ./api/... 2>/dev/null | sort -u)
all_pkgs=$(go list ./internal/... 2>/dev/null | sort -u)

unreachable=()
for pkg in $all_pkgs; do
  if ! grep -qx "$pkg" <<< "$prod_deps"; then
    skip=false
    for w in "${WHITELIST[@]}"; do
      if [[ "$pkg" == "github.com/Timwood0x10/ares$w" || "$pkg" == "github.com/Timwood0x10/ares/$w" ]]; then skip=true; break; fi
    done
    if [[ "$skip" == false ]]; then
      unreachable+=("$pkg")
    fi
  fi
done

if [ ${#unreachable[@]} -eq 0 ]; then
  echo "G1 reachability gate: PASSED (all internal packages reachable or whitelisted)"
  exit 0
fi

echo "G1 reachability gate: FAILED — unreachable internal packages (add to WHITELIST or wire them):"
for p in "${unreachable[@]}"; do
  echo "  $p"
done
exit 1
