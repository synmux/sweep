#!/usr/bin/env bash
# Build sweep, including setup: checks the Go toolchain, downloads and
# verifies modules, optionally runs the full check suite, then compiles.
#
#   ./build.sh                 build ./sweep
#   ./build.sh --check         run gofmt, go vet, golangci-lint and tests first
#   ./build.sh --install       install into $(go env GOBIN) or $(go env GOPATH)/bin
#   ./build.sh --output PATH   write the binary to PATH
#
# The version reported by `sweep --version` comes from `git describe`.
set -euo pipefail

script_directory="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$script_directory"

run_checks=false
install_binary=false
output_path="./sweep"

usage() {
	sed -n '2,9p' "$0" | sed 's/^# \{0,1\}//'
}

while (($# > 0)); do
	case "$1" in
	--check) run_checks=true ;;
	--install) install_binary=true ;;
	--output)
		[[ $# -ge 2 ]] || {
			echo "error: --output needs a path" >&2
			exit 2
		}
		output_path="$2"
		shift
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		echo "error: unknown option: $1" >&2
		usage >&2
		exit 2
		;;
	esac
	shift
done

if [[ -t 1 ]]; then
	bold=$'\e[1m' dim=$'\e[2m' green=$'\e[32m' red=$'\e[31m' reset=$'\e[0m'
else
	bold='' dim='' green='' red='' reset=''
fi

step() { printf '%s==>%s %s%s%s\n' "$green" "$reset" "$bold" "$1" "$reset"; }
fail() {
	printf '%serror:%s %s\n' "$red" "$reset" "$1" >&2
	exit 1
}

# version_at_least A B: true when version A >= version B (dotted numbers).
version_at_least() {
	[[ "$(printf '%s\n%s\n' "$2" "$1" | sort -V | head -n 1)" == "$2" ]]
}

step "Checking the Go toolchain"
command -v go >/dev/null 2>&1 ||
	fail "Go is not installed. Install Go $(awk '/^go /{print $2}' go.mod) or later (for example: brew install go, or mise use go@latest)."
required_version="$(awk '/^go /{print $2}' go.mod)"
installed_version="$(go env GOVERSION)"
installed_version="${installed_version#go}"
if version_at_least "$installed_version" "$required_version"; then
	printf '    %sgo %s (go.mod needs %s)%s\n' "$dim" "$installed_version" "$required_version" "$reset"
elif [[ "$(go env GOTOOLCHAIN)" == local ]]; then
	fail "go.mod needs Go $required_version but Go $installed_version is installed and GOTOOLCHAIN=local stops an automatic download. Upgrade Go or unset GOTOOLCHAIN."
else
	printf '    %sgo %s is older than %s; Go will download the required toolchain%s\n' "$dim" "$installed_version" "$required_version" "$reset"
fi

step "Downloading and verifying modules"
go mod download
go mod verify

if $run_checks; then
	step "Checking formatting"
	unformatted="$(gofmt -l .)"
	[[ -z "$unformatted" ]] || fail "gofmt would change these files:"$'\n'"$unformatted"

	step "Running go vet"
	go vet ./...

	if command -v golangci-lint >/dev/null 2>&1; then
		step "Running golangci-lint"
		golangci-lint run ./...
	else
		printf '    %sgolangci-lint not found, skipping (https://golangci-lint.run/welcome/install/)%s\n' "$dim" "$reset"
	fi

	step "Running tests"
	# -short skips the test that calls the live Jev API.
	go test -race -short ./...
fi

version="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
build_flags=(-trimpath -ldflags "-s -w -X main.version=$version")

if $install_binary; then
	step "Installing sweep $version"
	CGO_ENABLED=0 go install "${build_flags[@]}" .
	install_directory="$(go env GOBIN)"
	[[ -n "$install_directory" ]] || install_directory="$(go env GOPATH)/bin"
	built_binary="$install_directory/sweep"
else
	step "Building sweep $version"
	CGO_ENABLED=0 go build "${build_flags[@]}" -o "$output_path" .
	built_binary="$output_path"
fi

"$built_binary" --version >/dev/null || fail "the built binary at $built_binary does not run"
printf '%s✓%s %s\n' "$green" "$reset" "$built_binary"
