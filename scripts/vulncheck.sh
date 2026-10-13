#!/bin/sh
# Runs govulncheck (version passed as $1) and decides what a finding means for a developer's machine.
#
#   no findings                                      exit 0
#   ONLY Go standard library findings                exit 0 with a warning: they are fixed by installing a newer
#                                                    Go patch release, which a developer may not be able to do today.
#                                                    CI always installs the newest patch and enforces this check.
#   anything in a module we depend on, or a failure  exit non-zero: upgrade the module.
#
# govulncheck's summary line is "Your code is affected by N vulnerabilities from the Go standard library." when the
# standard library is the only source, and "... from 2 modules and the Go standard library." when a module is involved.
set -u

version="${1:?usage: vulncheck.sh <govulncheck version>}"

out=$(go run "golang.org/x/vuln/cmd/govulncheck@${version}" ./... 2>&1)
code=$?
printf '%s\n' "$out"

if [ "$code" -eq 0 ]; then
	exit 0
fi

if printf '%s\n' "$out" | grep -qE 'affected by [0-9]+ vulnerabilit(y|ies) from the Go standard library\.' \
	&& ! printf '%s\n' "$out" | grep -qE 'from [0-9]+ modules?'; then
	printf '\n'
	printf 'WARNING: govulncheck found only Go standard library vulnerabilities (%s).\n' "$(go version)"
	printf 'They are fixed in a newer Go patch release: install it when you can.\n'
	printf 'This is only a warning on your machine. CI installs the newest Go patch and ENFORCES this check.\n'
	exit 0
fi

exit "$code"
