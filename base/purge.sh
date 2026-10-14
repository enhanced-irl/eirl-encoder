#!/bin/bash
# B3: purges packages by groups (BASE.md §5). Runs ON the board, with sudo:
#
#   sudo base/purge.sh --plan  <group>   simulates and saves the plan; removes nothing (it does apply the
#                                        safety net below, to simulate with keep.txt already manual)
#   sudo base/purge.sh --apply <group>   purges EXACTLY the last plan (aborts while streaming or if the
#                                        simulation no longer matches the plan)
#   sudo base/purge.sh --undo  <group>   reinstalls what was purged (needs network and NVIDIA's repo)
#
# The group is base/purge-<group>.txt: packages or shell patterns (cuda-*), one per line.
# Safety net, before the first purge:
#   - a copy of the package selection, of the manual/hold marks and of /etc in /var/lib/eirl-base/backup;
#   - every nvidia-l4t-* on hold, except those the group names by hand. 🔴 An apt upgrade (04 Oct 2026)
#     rewrote the QSPI from the postinst of nvidia-l4t-bootloader: none is ever touched through a
#     pattern;
#   - base/keep.txt marked as manual, so that autoremove does not take what the encoder uses.
# A plan aborts if it purges anything in keep.txt or an nvidia-l4t-* not named, or if it INSTALLS anything.
set -u

HERE=$(cd "$(dirname "$0")" && pwd)
STATE=/var/lib/eirl-base
export LC_ALL=C DEBIAN_FRONTEND=noninteractive

die() { echo "purge: $*" >&2; exit 1; }

[ "$(id -u)" = 0 ] || die "needs sudo"
[ $# -ge 2 ] || die "usage: $0 --plan|--apply|--undo <group>"
action=$1 group=$2
list=$HERE/purge-$group.txt
[ -f "$list" ] || die "no such group: $list"
plan=$STATE/$group.plan
mkdir -p "$STATE"

clean() { sed -e 's/#.*//' -e 's/[[:space:]]*$//' -e 's/^[[:space:]]*//' "$1" | grep -v '^$'; }

# The user the engine runs as (User= of eirl-engine.service): its control socket is for that user.
engine_user() {
    local u=${EIRL_ENGINE_USER:-$(systemctl show -p User --value eirl-engine 2>/dev/null)}
    echo "${u:-root}"
}

# Same as slim.sh: fails closed if the engine is running and does not answer.
emitting() {
    grep -q '^OUTPUT=on' /run/eirl-engine/state 2>/dev/null && return 0
    systemctl is-active -q eirl-engine || return 1
    local mode
    mode=$(sudo -u "$(engine_user)" env PYTHONIOENCODING=utf-8 eirl-enginectl state 2>/dev/null |
        python3 -c 'import json,sys; print(json.load(sys.stdin)["output"]["mode"])' 2>/dev/null)
    [ "$mode" != off ]
}

# Installed = dpkg's 2nd letter in i/U/F/H/W/t (any desired state: «hi» is one on hold), or «rc».
installed() { dpkg-query -W -f='${db:Status-Abbrev} ${Package}\n' 2>/dev/null | awk '$1 ~ /^.[iUFHWt]/ || $1 == "rc" {print $2}'; }

# Installed packages that match the group's patterns.
expand() {
    local all pat
    all=$(installed)
    clean "$list" | while read -r pat; do
        for p in $all; do
            # shellcheck disable=SC2053
            [[ $p == $pat ]] && echo "$p"
        done
    done | sort -u
}

# The nvidia-l4t-* that the group names literally (no wildcards): the only ones it may purge.
named_l4t() { clean "$list" | grep '^nvidia-l4t-' | grep -v '[*?[]' || true; }

simulate() {
    # --auto-remove: includes what is left orphaned, so that the plan is the complete list.
    apt-get -s --auto-remove purge "$@" 2>&1
}

# Packages with a library loaded RIGHT NOW in a process (engine, app, network…). A plan that removes
# one aborts, unless it is in base/inuse-ok.txt with its reason: that way it does not depend on keep.txt
# being complete (gstreamer1.0-x, e.g., looked like X11 and brings the engine's textoverlay).
inuse() {
    cat /proc/[0-9]*/maps 2>/dev/null | awk '$6 ~ /\.so/ {print $6}' | sort -u |
        xargs -r dpkg -S 2>/dev/null | grep -v '^diversion' | cut -d: -f1 | tr ',' '\n' | sed 's/ //g' | sort -u
}

check_plan() {
    local sim=$1 bad=0 p used
    used=$(inuse)
    if grep -q '^Inst ' "$sim"; then
        echo "🔴 the simulation INSTALLS or upgrades packages:"; grep '^Inst ' "$sim" | head
        bad=1
    fi
    for p in $(awk '/^(Purg|Remv) /{print $2}' "$sim"); do
        if clean "$HERE/keep.txt" | awk '{print $1}' | grep -qx "$p"; then
            echo "🔴 it would remove $p (it is in keep.txt)"; bad=1
        fi
        if echo "$used" | grep -qx "$p" && ! clean "$HERE/inuse-ok.txt" 2>/dev/null | awk '{print $1}' | grep -qx "$p"; then
            echo "🔴 it would remove $p, which has libraries loaded in a running process"; bad=1
        fi
        if [[ $p == nvidia-l4t-* ]] && ! named_l4t | grep -qx "$p"; then
            echo "🔴 it would remove $p (an nvidia-l4t-* not named in the group)"; bad=1
        fi
    done
    return $bad
}

backup() {
    local b=$STATE/backup
    [ -d "$b" ] && return
    mkdir -p "$b"
    dpkg --get-selections > "$b/selections.txt"
    apt-mark showmanual > "$b/manual.txt"
    apt-mark showhold > "$b/hold.txt"
    dpkg-query -W -f='${Package}=${Version}\n' > "$b/versions.txt"
    tar czf "$b/etc.tgz" -C / etc 2>/dev/null
    echo "backup in $b"
}

guard() {
    # hold on ALL nvidia-l4t-* except those this group names (released only while the operation
    # lasts: rehold holds them again); manual for keep.txt (only the installed ones).
    local hold keep named
    hold=$(installed | grep '^nvidia-l4t-' | grep -vxF -f <(named_l4t; echo '#') || true)
    # shellcheck disable=SC2086
    [ -n "$hold" ] && apt-mark hold $hold >/dev/null
    named=$(named_l4t | grep -xF -f <(installed) || true)
    # shellcheck disable=SC2086
    [ -n "$named" ] && apt-mark unhold $named >/dev/null
    keep=$(clean "$HERE/keep.txt" | awk '{print $1}' | grep -xF -f <(installed) || true)
    # shellcheck disable=SC2086
    [ -n "$keep" ] && apt-mark manual $keep >/dev/null
}

rehold() {
    local named
    named=$(named_l4t | grep -xF -f <(installed) || true)
    # shellcheck disable=SC2086
    [ -n "$named" ] && apt-mark hold $named >/dev/null
    return 0
}

size_mb() {
    # shellcheck disable=SC2046
    dpkg-query -W -f='${Installed-Size}\n' "$@" 2>/dev/null | awk '{s+=$1} END {printf "%.0f", s/1024}'
}

do_plan() {
    local pkgs sim
    # Without this, what the desktop brought in as a dependency (network-manager, e.g.) shows up as
    # orphaned in the simulation: the simulation must run with keep.txt already marked as manual.
    backup
    guard
    trap rehold EXIT
    pkgs=$(expand)
    [ -n "$pkgs" ] || die "nothing installed from group $group"
    sim=$(mktemp)
    # shellcheck disable=SC2086
    simulate $pkgs > "$sim"
    if grep -q '^E: ' "$sim"; then cat "$sim"; rm -f "$sim"; die "apt cannot resolve the plan"; fi
    echo "group $group: $(echo "$pkgs" | wc -l) packages requested"
    if ! check_plan "$sim"; then rm -f "$sim"; die "plan rejected: not saved"; fi
    awk '/^(Purg|Remv) /{print $2}' "$sim" | sort -u > "$plan"
    # shellcheck disable=SC2046
    echo "plan: $(wc -l < "$plan") packages to purge, $(size_mb $(cat "$plan")) MB → $plan"
    rm -f "$sim"
}

do_apply() {
    [ -s "$plan" ] || die "no plan: run --plan $group first"
    if emitting; then die "streaming: nothing purged"; fi
    backup
    guard
    trap rehold EXIT
    local sim
    sim=$(mktemp)
    # 🪤 It simulates the SAME as --plan (the group's packages), not the plan's list: simulating the list
    # with its orphans already in it makes apt find second-level orphans, and it never matches.
    # shellcheck disable=SC2046
    simulate $(expand) > "$sim"
    check_plan "$sim" || { rm -f "$sim"; die "the simulation is no longer safe"; }
    if ! diff -q <(awk '/^(Purg|Remv) /{print $2}' "$sim" | sort -u) "$plan" >/dev/null; then
        rm -f "$sim"; die "the simulation no longer matches the plan: run --plan $group again"
    fi
    rm -f "$sim"
    dpkg-query -W -f='${Package} ${Version}\n' $(cat "$plan") > "$STATE/$group.purged" 2>/dev/null
    # shellcheck disable=SC2046
    nice -n 19 ionice -c3 apt-get -y purge $(cat "$plan") || die "apt-get purge failed (see dpkg --audit)"
    apt-get clean
    dpkg --audit
    echo "purged: $(wc -l < "$STATE/$group.purged") packages (list in $STATE/$group.purged)"
}

do_undo() {
    local f=$STATE/$group.purged
    [ -s "$f" ] || die "group $group is not purged (no $f)"
    # shellcheck disable=SC2046
    apt-get -y install --no-install-recommends $(awk '{print $1}' "$f") || die "the reinstallation failed"
    mv "$f" "$f.undone-$(date +%Y%m%d-%H%M%S)"
}

case $action in
    --plan) do_plan ;;
    --apply) do_apply ;;
    --undo) do_undo ;;
    *) die "unknown action: $action" ;;
esac
