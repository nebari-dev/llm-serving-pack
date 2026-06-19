#!/usr/bin/env bash
# check-links.sh - Internal link checker for the docs Hugo site.
# Builds the site, then verifies:
#   1. Every internal href/src in public/**/*.html resolves to an existing file
#      under docs/site/public/.
#   2. Every .edit-link href points to a source file that exists under
#      docs/site/content/.
# Exits 0 (LINKS_OK) on success; exits 1 and lists offenders on failure.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SITE_DIR="$REPO_ROOT/docs/site"
PUBLIC_DIR="$SITE_DIR/public"
CONTENT_DIR="$SITE_DIR/content"

# ---------------------------------------------------------------------------
# 1. Build the site
# ---------------------------------------------------------------------------
echo "Building site..."
(cd "$SITE_DIR" && hugo --minify --quiet)
echo "Build complete."

# ---------------------------------------------------------------------------
# Derive subpath prefix from baseURL (e.g. /nebari-llm-serving-pack)
# baseURL = "https://host/subpath/" -> subpath prefix = "/subpath"
# ---------------------------------------------------------------------------
BASE_URL=$(grep -m1 'baseURL' "$SITE_DIR/hugo.toml" | sed 's/.*= *"\(.*\)"/\1/')
# Strip scheme+host: https://nebari-dev.github.io/nebari-llm-serving-pack/
SUBPATH_PREFIX=$(echo "$BASE_URL" | sed 's|^https\?://[^/]*||' | sed 's|/$||')
# e.g. SUBPATH_PREFIX = "/nebari-llm-serving-pack"

# ---------------------------------------------------------------------------
# Helper: resolve a URL path to a filesystem path under public/
# Returns the path string; caller checks existence.
# ---------------------------------------------------------------------------
resolve_path() {
    local href="$1"

    # Strip the subpath prefix if present
    if [ -n "$SUBPATH_PREFIX" ]; then
        href="${href#$SUBPATH_PREFIX}"
    fi

    # Empty after stripping means it was just the subpath root -> /
    [ -z "$href" ] && href="/"

    # Trailing slash or bare dir -> index.html
    if [[ "$href" == */ ]]; then
        echo "$PUBLIC_DIR${href}index.html"
    else
        echo "$PUBLIC_DIR$href"
    fi
}

# ---------------------------------------------------------------------------
# 2. Walk every HTML file and check internal links
# ---------------------------------------------------------------------------
BROKEN_LINKS=()

while IFS= read -r -d '' html_file; do
    # Extract href=... and src=... values (minified HTML, no quotes around values)
    # Match both quoted (href="...") and unquoted (href=...) forms.
    while IFS= read -r url; do
        # Skip empty
        [ -z "$url" ] && continue

        # Skip external URLs
        case "$url" in
            http://*|https://*) continue ;;
        esac

        # Skip anchors only (#...) and mailto:
        case "$url" in
            \#*|mailto:*) continue ;;
        esac

        # Only process absolute paths (starting with /) or paths starting with subpath
        case "$url" in
            /*) ;;
            *) continue ;;
        esac

        target=$(resolve_path "$url")

        if [ ! -e "$target" ]; then
            BROKEN_LINKS+=("BROKEN: $html_file -> $url (resolved: $target)")
        fi
    done < <(
        grep -oE 'href=[^[:space:]>]+|src=[^[:space:]>]+' "$html_file" \
            | sed 's/^href=//; s/^src=//' \
            | sed 's/^"//; s/"$//' \
            | sed "s/^'//; s/'$//"
    )
done < <(find "$PUBLIC_DIR" -name "*.html" -print0)

# ---------------------------------------------------------------------------
# 3. Check .edit-link hrefs -> content/ source files
# ---------------------------------------------------------------------------
EDIT_BASE=$(grep -m1 'editBase' "$SITE_DIR/hugo.toml" | sed 's/.*= *"\(.*\)"/\1/')
# e.g. https://github.com/nebari-dev/nebari-llm-serving-pack/edit/main/docs/site/content

BROKEN_EDIT=()

while IFS= read -r -d '' html_file; do
    # Extract href values from <a class=edit-link href=...> (minified HTML)
    while IFS= read -r edit_href; do
        [ -z "$edit_href" ] && continue

        # Strip editBase prefix to get the relative path in content/
        rel="${edit_href#$EDIT_BASE/}"

        # If nothing was stripped, something is wrong - skip
        [ "$rel" = "$edit_href" ] && continue

        src_file="$CONTENT_DIR/$rel"
        if [ ! -f "$src_file" ]; then
            BROKEN_EDIT+=("BROKEN edit-link: $html_file -> $edit_href (expected: $src_file)")
        fi
    done < <(
        grep -oE 'class=edit-link href=[^[:space:]>]+|class="edit-link"[[:space:]]+href="[^"]*"' "$html_file" \
            | grep -oE 'href=[^[:space:]>]+' \
            | sed 's/^href=//; s/^"//; s/"$//'
    )
done < <(find "$PUBLIC_DIR" -name "*.html" -print0)

# ---------------------------------------------------------------------------
# 4. Report results
# ---------------------------------------------------------------------------
FAILED=0

if [ ${#BROKEN_LINKS[@]} -gt 0 ]; then
    echo ""
    echo "Internal link failures (${#BROKEN_LINKS[@]}):"
    for msg in "${BROKEN_LINKS[@]}"; do
        echo "  $msg"
    done
    FAILED=1
fi

if [ ${#BROKEN_EDIT[@]} -gt 0 ]; then
    echo ""
    echo "Edit-link failures (${#BROKEN_EDIT[@]}):"
    for msg in "${BROKEN_EDIT[@]}"; do
        echo "  $msg"
    done
    FAILED=1
fi

if [ $FAILED -eq 1 ]; then
    exit 1
fi

echo "LINKS_OK"
