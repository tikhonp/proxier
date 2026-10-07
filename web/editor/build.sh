#!/bin/sh
# Rebuilds internal/platform/ui/static/js/editor.bundle.js from editor.js.
# Run it through `make editor`: it expects node:22-alpine (npm, no host Node).
# node_modules lives in a temporary directory, never in the repository.
set -eu
src=$(cd "$(dirname "$0")" && pwd)
out="$src/../../internal/platform/ui/static/js/editor.bundle.js"
work=$(mktemp -d)
cp "$src/package.json" "$src/package-lock.json" "$src/editor.js" "$work/"
cd "$work"
npm ci --no-audit --no-fund --silent
./node_modules/.bin/esbuild editor.js --bundle --minify --format=esm --target=es2020 --legal-comments=none --outfile="$work/editor.bundle.js"
cp "$work/editor.bundle.js" "$out"
echo "wrote $out ($(wc -c < "$out") bytes)"
