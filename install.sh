#!/bin/sh

set -eu
umask 022

repository="Mosquid/tapas"
requested_version="${TAPAS_VERSION:-latest}"
if [ -n "${TAPAS_INSTALL_DIR:-}" ]; then
    install_dir=$TAPAS_INSTALL_DIR
elif [ -n "${HOME:-}" ]; then
    install_dir="$HOME/.local/bin"
else
    install_dir=
fi
if [ -n "${TAPAS_SKILL_DIR:-}" ]; then
    skill_dir=$TAPAS_SKILL_DIR
elif [ -n "${HOME:-}" ]; then
    skill_dir="$HOME/.claude/skills/agent-secrets"
else
    skill_dir=
fi
install_skill=1

usage() {
    printf '%s\n' \
        'Install Tapas and its Claude Code skill.' \
        '' \
        'Usage: install.sh [options]' \
        '' \
        'Options:' \
        '  --version VERSION   Release tag to install (default: latest)' \
        '  --install-dir DIR   Binary directory (default: ~/.local/bin)' \
        '  --skill-dir DIR     Claude skill directory' \
        '  --no-skill          Install only the binary' \
        '  -h, --help          Show this help' \
        '' \
        'The same settings can be supplied through TAPAS_VERSION,' \
        'TAPAS_INSTALL_DIR, and TAPAS_SKILL_DIR.'
}

while [ "$#" -gt 0 ]; do
    case "$1" in
        --version)
            [ "$#" -ge 2 ] || { printf '%s\n' 'error: --version requires a value' >&2; exit 2; }
            requested_version=$2
            shift 2
            ;;
        --install-dir)
            [ "$#" -ge 2 ] || { printf '%s\n' 'error: --install-dir requires a value' >&2; exit 2; }
            install_dir=$2
            shift 2
            ;;
        --skill-dir)
            [ "$#" -ge 2 ] || { printf '%s\n' 'error: --skill-dir requires a value' >&2; exit 2; }
            skill_dir=$2
            shift 2
            ;;
        --no-skill)
            install_skill=0
            shift
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            printf 'error: unknown option: %s\n' "$1" >&2
            usage >&2
            exit 2
            ;;
    esac
done

if [ -z "$install_dir" ] || { [ "$install_skill" -eq 1 ] && [ -z "$skill_dir" ]; }; then
    printf '%s\n' 'error: HOME is unset; provide --install-dir and --skill-dir' >&2
    exit 1
fi

for command_name in curl tar mktemp awk; do
    command -v "$command_name" >/dev/null 2>&1 || {
        printf 'error: required command is not installed: %s\n' "$command_name" >&2
        exit 1
    }
done

case "$(uname -s)" in
    Darwin) os=darwin ;;
    Linux) os=linux ;;
    *)
        printf 'error: unsupported operating system: %s\n' "$(uname -s)" >&2
        exit 1
        ;;
esac

case "$(uname -m)" in
    arm64|aarch64) arch=arm64 ;;
    x86_64|amd64) arch=amd64 ;;
    *)
        printf 'error: unsupported architecture: %s\n' "$(uname -m)" >&2
        exit 1
        ;;
esac

asset="tapas-${os}-${arch}.tar.gz"
if [ -n "${TAPAS_ASSET_BASE_URL:-}" ]; then
    asset_base_url=${TAPAS_ASSET_BASE_URL%/}
elif [ "$requested_version" = latest ]; then
    asset_base_url="https://github.com/${repository}/releases/latest/download"
else
    case "$requested_version" in
        v*) release_tag=$requested_version ;;
        *) release_tag="v${requested_version}" ;;
    esac
    asset_base_url="https://github.com/${repository}/releases/download/${release_tag}"
fi

temporary_dir=$(mktemp -d "${TMPDIR:-/tmp}/tapas-install.XXXXXX")
cleanup() {
    [ -z "${binary_stage:-}" ] || rm -f "$binary_stage"
    [ -z "${skill_stage:-}" ] || rm -f "$skill_stage"
    rm -rf "$temporary_dir"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

archive="$temporary_dir/$asset"
checksums="$temporary_dir/checksums.txt"
printf 'Downloading %s\n' "$asset"
curl -fsSL "$asset_base_url/$asset" -o "$archive"
curl -fsSL "$asset_base_url/checksums.txt" -o "$checksums"

expected=$(awk -v name="$asset" '$2 == name || $2 == "*" name { print $1; exit }' "$checksums")
[ -n "$expected" ] || {
    printf 'error: %s is absent from checksums.txt\n' "$asset" >&2
    exit 1
}
if command -v sha256sum >/dev/null 2>&1; then
    actual=$(sha256sum "$archive" | awk '{ print $1 }')
elif command -v shasum >/dev/null 2>&1; then
    actual=$(shasum -a 256 "$archive" | awk '{ print $1 }')
else
    printf '%s\n' 'error: sha256sum or shasum is required to verify the download' >&2
    exit 1
fi
[ "$actual" = "$expected" ] || {
    printf '%s\n' 'error: downloaded archive checksum does not match' >&2
    exit 1
}

tar -xzf "$archive" -C "$temporary_dir"
archive_root="$temporary_dir/tapas-${os}-${arch}"
[ -x "$archive_root/tapas" ] || {
    printf '%s\n' 'error: release archive does not contain the tapas executable' >&2
    exit 1
}
if [ "$install_skill" -eq 1 ]; then
    skill_source="$archive_root/skills/agent-secrets/SKILL.md"
    [ -f "$skill_source" ] || {
        printf '%s\n' 'error: release archive does not contain the Claude skill' >&2
        exit 1
    }
fi

mkdir -p "$install_dir"
binary_stage="$install_dir/.tapas.new.$$"
cp "$archive_root/tapas" "$binary_stage"
chmod 0755 "$binary_stage"
mv -f "$binary_stage" "$install_dir/tapas"
binary_stage=

if [ "$install_skill" -eq 1 ]; then
    mkdir -p "$skill_dir"
    skill_stage="$skill_dir/.SKILL.md.new.$$"
    cp "$skill_source" "$skill_stage"
    chmod 0644 "$skill_stage"
    mv -f "$skill_stage" "$skill_dir/SKILL.md"
    skill_stage=
fi

printf 'Installed tapas to %s\n' "$install_dir/tapas"
if [ "$install_skill" -eq 1 ]; then
    printf 'Installed the Claude Code skill to %s\n' "$skill_dir/SKILL.md"
fi

case ":${PATH:-}:" in
    *:"$install_dir":*) ;;
    *) printf 'Note: add %s to PATH before running tapas.\n' "$install_dir" ;;
esac

if ! command -v sops >/dev/null 2>&1; then
    printf '%s\n' 'Note: SOPS is a required runtime dependency; install it before running tapas init.'
fi
printf '%s\n' 'Start a new Claude Code session if the agent-secrets skill is not detected.'
