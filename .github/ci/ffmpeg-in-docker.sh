#!/bin/sh
# ffmpeg for the integration tests, run from our own analysis-media image
# (debian:12-slim with Debian's ffmpeg, as in production), for when the
# runner can't install it: on 11 Nov its Ubuntu mirrors were unreachable
# while Docker, and Debian's mirrors inside it, worked.
#
# Paths are the same inside: /tmp (where the tests' temporary files are),
# the runner's temp directory and the workspace are mounted where they
# are. It runs as the caller, so what it writes is the caller's.
set -e
img=${SB_FFMPEG_IMAGE:-sb-analysis-media:ci}
mounts="-v /tmp:/tmp"
for d in "${RUNNER_TEMP:-}" "${GITHUB_WORKSPACE:-}" "$PWD"; do
	[ -n "$d" ] && [ "$d" != /tmp ] && mounts="$mounts -v $d:$d"
done
# shellcheck disable=SC2086 # $mounts is a list of options
exec docker run --rm -i --user "$(id -u):$(id -g)" $mounts -w "$PWD" --entrypoint ffmpeg "$img" "$@"
