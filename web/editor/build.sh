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
# style-mod puts CodeMirror's styles into constructable stylesheets only in
# shadow roots and writes <style> tags on the document, which the CSP
# (style-src 'self') blocks. Adopted sheets work on the document too, and the
# editor must not live in a shadow root (see editor.js), so drop the check.
sm=node_modules/style-mod/src/style-mod.js
grep -q 'if (!root.head && root.adoptedStyleSheets && win.CSSStyleSheet) {' "$sm" || { echo "style-mod changed: review the patch in build.sh" >&2; exit 1; }
sed -i 's/if (!root.head && root.adoptedStyleSheets && win.CSSStyleSheet) {/if (root.adoptedStyleSheets \&\& win.CSSStyleSheet) {/' "$sm"
grep -q 'if (root.adoptedStyleSheets && win.CSSStyleSheet) {' "$sm"
./node_modules/.bin/esbuild editor.js --bundle --minify --format=esm --target=es2020 --legal-comments=none --outfile="$work/editor.bundle.js"
cp "$work/editor.bundle.js" "$out"
echo "wrote $out ($(wc -c < "$out") bytes)"
