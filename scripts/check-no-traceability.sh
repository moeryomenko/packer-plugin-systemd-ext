#!/bin/sh
# Gate: fail if process-artifact identifiers appear in tracked files.
# Scans `git ls-files` output only (tracked files). For every line that
# matches an artifact pattern, prints `file:line:matched-text`. Exits 1
# when any match exists, 0 when the tree is clean.
set -eu

# Resolve the repo root from the script's own location so this works from
# any CWD inside the repository.
# shellcheck disable=SC1007 # CDPATH= prefix is the empty-assignment idiom
ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"

# Patterns are kept only as regex/quote usage; each literal token is split
# across adjacent quotes so this file never contains the strings it scans
# for (self-scan stays clean).
PAT_VC='VC-[0-9]+'
PAT_REQ='REQ-[0-9]+'
PAT_SPEC='SPEC-202''6'
PAT_REV='REV-[0-9]'
PAT_TASK='TASK-[0-9]+'
PAT_TC='TC-[0-9]+'
PAT_RISK='\(R[0-9]\)'
PAT_VCAP='Verification'' Contract'
PAT_VCAP_LOW='verification'' contract'
PAT_RUN_NOTES='run-note''s'
PAT_MANIFEST='tests-manife''st'
PAT_STATIC='static-contrac''t'
PAT_SPEC_SEC='Spec'' Section'

PATTERN="${PAT_VC}|${PAT_REQ}|${PAT_SPEC}|${PAT_REV}|${PAT_TASK}|${PAT_TC}|${PAT_RISK}|${PAT_VCAP}|${PAT_VCAP_LOW}|${PAT_RUN_NOTES}|${PAT_MANIFEST}|${PAT_STATIC}|${PAT_SPEC_SEC}"

# `git ls-files` fails loudly (set -eu) when the path is not a repository.
files=$(git -C "$ROOT" ls-files)

out=$(printf '%s\n' "$files" | while IFS= read -r file; do
    [ -n "$file" ] || continue
    grep -nE "$PATTERN" -- "$ROOT/$file" 2>/dev/null | sed "s|^|${file}:|"
done)

if [ -n "$out" ]; then
    printf '%s\n' "$out"
    exit 1
fi

printf '%s\n' "clean"
exit 0
