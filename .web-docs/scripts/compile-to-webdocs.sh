#!/usr/bin/env bash
# Compiles the packer-sdc rendered docs (.docs/) into an integrations-compliant
# folder (.web-docs/). Adapted from the standard Packer plugin web-docs
# pipeline used by the reference plugins (e.g. packer-plugin-amazon,
# packer-plugin-ansible).
#
# Usage: compile-to-webdocs.sh <plugin-dir> <docs-dir> <web-docs-dir> <org>

set -o pipefail

componentTypeFromFolderName() {
    case "$1" in
        builders)       echo "builder" ;;
        provisioners)   echo "provisioner" ;;
        post-processors) echo "post-processor" ;;
        datasources)    echo "data-source" ;;
        *)              echo "" ;;
    esac
}

# $1: content to adjust links, $2: the organization of the integration
rewriteLinks() {
    local result="$1"
    local organization="$2"
    local urlSegment="([^/]+)"
    local urlAnchor="(#[^/]+)"

    # Component index page links -> integration root page links.
    local find="\(\/packer\/plugins\/$urlSegment\/$urlSegment$urlAnchor?\)"
    local replace="\(\/packer\/integrations\/$organization\/\2\3\)"
    result="$(echo "$result" | sed -E "s/$find/$replace/g")"

    # Component links -> integration component page links.
    local find="\(\/packer\/plugins\/$urlSegment\/$urlSegment\/$urlSegment$urlAnchor?\)"
    local replace="\(\/packer\/integrations\/$organization\/\2\/latest\/components\/\1\/\3\4\)"
    result="$(echo "$result" | sed -E "s/$find/$replace/g")"

    # Component URL segment renames (Packer plugin -> Integrations naming).
    result="$(echo "$result" \
        | sed "s/\/datasources\//\/data-source\//g" \
        | sed "s/\/builders\//\/builder\//g" \
        | sed "s/\/post-processors\//\/post-processor\//g" \
        | sed "s/\/provisioners\//\/provisioner\//g" \
    )"

    echo "$result"
}

# $1: docs dir, $2: web docs dir, $3: component file, $4: org
processComponentFile() {
    local docsDir="$1"
    local webDocsDir="$2"
    local componentFile="$3"
    local org="$4"

    local escapedDocsDir="$(echo "$docsDir" | sed 's/\//\\\//g' | sed 's/\./\\\./g')"
    local componentTypeAndSlug="$(echo "$componentFile" | sed "s/$escapedDocsDir\///g" | sed 's/\.mdx//g')"

    local componentSlug="$(echo "$componentTypeAndSlug" | cut -d'/' -f 2)"
    local componentType="$(componentTypeFromFolderName "$(echo "$componentTypeAndSlug" | cut -d'/' -f 1)")"
    if [[ "$componentType" = "" ]]; then
        echo "Failed to process '$componentFile', unexpected folder name."
        echo "Documentation for components must be stored in one of:"
        echo "builders, provisioners, post-processors, datasources"
        exit 1
    fi

    local webDocsFolder="$webDocsDir/components/$componentType/$componentSlug"
    mkdir -p "$webDocsFolder"
    local webDocsFile="$webDocsFolder/README.md"
    local webDocsFileTmp="$webDocsFolder/README.md.tmp"

    cp "$componentFile" "$webDocsFile"

    # Remove the frontmatter header (everything through the closing ---).
    local lastMetadataLine="$(grep -n -m 2 '^---' "$componentFile" | tail -n1 | cut -d':' -f1)"
    tail -n +"$(($lastMetadataLine+2))" "$webDocsFile" > "$webDocsFileTmp"
    mv "$webDocsFileTmp" "$webDocsFile"

    # Remove the top H1, as it is added automatically on the web.
    tail -n +3 "$webDocsFile" > "$webDocsFileTmp"
    mv "$webDocsFileTmp" "$webDocsFile"

    rewriteLinks "$(cat "$webDocsFile")" "$org" > "$webDocsFileTmp"
    mv "$webDocsFileTmp" "$webDocsFile"
}

# $1: plugin dir, $2: docs dir, $3: web docs dir, $4: org
compileWebDocs() {
    local pluginDir="$1"
    local docsDir="$pluginDir/$2"
    local webDocsDir="$pluginDir/$3"

    echo "Compiling MDX docs in '$2' to Markdown in '$3'..."
    mkdir -p "$webDocsDir"

    cp "$docsDir/README.md" "$webDocsDir/README.md"

    for file in $(find "$docsDir" | grep "$docsDir/.*/.*\.mdx" | grep --invert-match "index.mdx"); do
        processComponentFile "$docsDir" "$webDocsDir" "$file" "$4"
    done
}

compileWebDocs "$1" "$2" "$3" "$4"
