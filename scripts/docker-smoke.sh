#!/bin/sh
# docker-smoke.sh - smoke test for the baton Docker image.
#
# Runs *inside* the container (it assumes `baton` and the runtime packages
# from the Dockerfile are on PATH) and checks:
#   - the process is not root
#   - every bundled utility works, including an archive round-trip
#   - `baton init` / `validate` / `run` succeed against the bundled hello
#     template, in a throwaway directory that is removed on exit
#
# Usage (from the host, with the image already built):
#   docker compose run --rm --entrypoint sh baton /workspace/scripts/docker-smoke.sh
#
# Exits 0 when every check passes, non-zero on the first failure.

set -eu

log() { printf '==> %s\n' "$*"; }
fail() {
	printf 'FAIL: %s\n' "$*" >&2
	exit 1
}

# --- 1. non-root ---------------------------------------------------------
uid="$(id -u)"
[ "$uid" != "0" ] || fail "running as root (uid 0), expected a non-root uid"
log "running as uid=$uid gid=$(id -g) (non-root, ok)"

# --- 2. utilities present and working -------------------------------------
for bin in baton git bash jq curl grep rg bzip2 zip unzip tar gzip gunzip; do
	command -v "$bin" >/dev/null 2>&1 || fail "missing required binary: $bin"
done
log "all required binaries are on PATH"

workdir="$(mktemp -d)"
cleanup() { rm -rf "$workdir"; }
trap cleanup EXIT INT TERM

echo "hello baton" >"$workdir/payload.txt"

# jq
[ "$(printf '{"a":1}' | jq -r .a)" = "1" ] || fail "jq did not round-trip"

# grep / ripgrep
grep -q "hello" "$workdir/payload.txt" || fail "grep did not find expected text"
rg -q "hello" "$workdir/payload.txt" || fail "ripgrep did not find expected text"

# gzip
gzip -c "$workdir/payload.txt" >"$workdir/payload.txt.gz"
gunzip -c "$workdir/payload.txt.gz" | cmp -s - "$workdir/payload.txt" ||
	fail "gzip round-trip mismatch"

# bzip2
bzip2 -c "$workdir/payload.txt" >"$workdir/payload.txt.bz2"
bunzip2 -c "$workdir/payload.txt.bz2" | cmp -s - "$workdir/payload.txt" ||
	fail "bzip2 round-trip mismatch"

# tar (+gzip)
tar -C "$workdir" -czf "$workdir/payload.tar.gz" payload.txt
mkdir -p "$workdir/tar-out"
tar -C "$workdir/tar-out" -xzf "$workdir/payload.tar.gz"
cmp -s "$workdir/payload.txt" "$workdir/tar-out/payload.txt" ||
	fail "tar round-trip mismatch"

# zip / unzip
(cd "$workdir" && zip -q payload.zip payload.txt)
mkdir -p "$workdir/zip-out"
(cd "$workdir/zip-out" && unzip -q "$workdir/payload.zip")
cmp -s "$workdir/payload.txt" "$workdir/zip-out/payload.txt" ||
	fail "zip round-trip mismatch"

log "jq, grep, ripgrep, gzip, bzip2, tar and zip all round-tripped"

# git (used by baton's own file-step git history, not a scenario feature
# here - just confirm the binary actually runs)
git -C "$workdir" init -q
[ -d "$workdir/.git" ] || fail "git init did not create a repository"

# curl: confirm the binary runs (no network call in a smoke test)
curl --version >/dev/null 2>&1 || fail "curl --version failed"

log "git and curl are functional"

# --- 3. baton init / validate / run --------------------------------------
# cd into the throwaway dir first: baton's config search reads ./baton.yaml
# relative to the working directory, and /workspace (the bind-mounted repo
# checkout) has its own baton.yaml for the summarize template, which needs
# a provider key the hello template has no business requiring.
scenario_dir="$workdir/myworkflow"

baton init "$scenario_dir" ||
	fail "baton init failed"
[ -f "$scenario_dir/hello.yaml" ] ||
	fail "baton init did not write hello.yaml"

(cd "$scenario_dir" && baton validate hello.yaml) ||
	fail "baton validate rejected the generated hello scenario"

set +e
(cd "$scenario_dir" && baton run hello.yaml -i who=Docker)
run_status=$?
set -e
[ "$run_status" -eq 0 ] || fail "baton run exited $run_status, expected 0"

log "baton init/validate/run all succeeded"

# --- 4. failure exit codes -------------------------------------------------
# An input that fails its pattern must be rejected by validate/run with a
# non-zero, non-crashing exit code (exitcode.Config == 3). Note: POSIX
# `if cond; then ...; fi` reports exit status 0 when cond is false and
# there is no else, so the status has to be captured explicitly with -e
# suspended around the one command expected to fail.
set +e
(cd "$scenario_dir" && baton run hello.yaml -i who='not valid!') >/dev/null 2>&1
bad_status=$?
set -e

[ "$bad_status" -ne 0 ] ||
	fail "baton run accepted an input that violates its pattern"
[ "$bad_status" -eq 3 ] ||
	fail "expected exit code 3 (config) for an invalid input, got $bad_status"

log "baton reports the expected exit code for an invalid input"

log "docker smoke test passed"
