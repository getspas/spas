#!/usr/bin/env bash
set -euo pipefail

: "${RPM_SIGNING_KEY:?Set the armored RPM signing key}"
: "${RPM_SIGNING_FINGERPRINT:?Set the RPM signing fingerprint}"
: "${SPAS_PACKAGE_MAINTAINER:?Set the package maintainer name and email}"

umask 077
mkdir -p .distribution
printf '%s\n' "$RPM_SIGNING_KEY" > .distribution/rpm-signing.key
export GNUPGHOME
GNUPGHOME=$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/spas-gnupg.XXXXXX")
trap 'rm -rf "$GNUPGHOME"' EXIT
gpg --batch --import .distribution/rpm-signing.key
gpg --batch --armor --export "$RPM_SIGNING_FINGERPRINT" > .distribution/spas-rpm-signing.asc
test -s .distribution/spas-rpm-signing.asc
