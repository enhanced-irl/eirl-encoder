#!/bin/bash
# Slims the Jetson's base system group by group (BASE.md §6). Runs ON the board, with sudo:
#
#   sudo base/slim.sh --check <group>           what is left to apply, and how each item was before
#   sudo base/slim.sh --apply <group> [--force] applies the group (aborts while streaming)
#   sudo base/slim.sh --undo  <group>           leaves it as it was before the first --apply
#
# A unit group is base/units-<group>.txt: one unit per line. «linger:<user>» enables linger
# («linger:@login» = the user who runs sudo, or EIRL_LOGIN_USER); «file:<path>» installs
# base/files/<name> at <path> (keeping a copy if it existed); «mask:<unit>» only masks it (no disable,
# no stop: for units linked from outside /lib, such as those in /opt/nvidia, which a disable would leave
# with no way to enable them again). Applying twice changes nothing: the previous state is saved the
# FIRST time in /var/lib/eirl-base/<group>.prev, and that is what --undo restores.
set -u

HERE=$(cd "$(dirname "$0")" && pwd)
STATE=/var/lib/eirl-base

die() { echo "slim: $*" >&2; exit 1; }

[ "$(id -u)" = 0 ] || die "needs sudo"
[ $# -ge 2 ] || die "usage: $0 --check|--apply|--undo <group> [--force]"
action=$1 group=$2 force=${3:-}
list=$HERE/units-$group.txt
[ -f "$list" ] || die "no such group: $list"
prev=$STATE/$group.prev
mkdir -p "$STATE"

# The user whose linger «linger:@login» means: the one who ran sudo. It is expanded before anything
# else, so the .prev files keep the real user name (as they always did).
LOGIN_USER=${EIRL_LOGIN_USER:-${SUDO_USER:-}}
if grep -qE '^[[:space:]]*linger:@login([[:space:]#]|$)' "$list" && [ -z "$LOGIN_USER" ]; then
    die "$list uses linger:@login: run it with sudo from that user, or set EIRL_LOGIN_USER"
fi

items() {
    sed -e 's/#.*//' -e 's/[[:space:]]*$//' -e 's/^[[:space:]]*//' -e "s/^linger:@login$/linger:$LOGIN_USER/" "$list" |
        grep -v '^$'
}

# The user the engine runs as (User= of eirl-engine.service): its control socket is for that user.
engine_user() {
    local u=${EIRL_ENGINE_USER:-$(systemctl show -p User --value eirl-engine 2>/dev/null)}
    echo "${u:-root}"
}

# Never change anything with the output on (BASE.md §6, a board rule): it is checked here, in the same
# command that applies, not a minute earlier.
# If the engine is running and does not answer, it counts as «streaming» (fails closed). The touch
# screen can turn the output on without going through anyone.
emitting() {
    grep -q '^OUTPUT=on' /run/eirl-engine/state 2>/dev/null && return 0
    systemctl is-active -q eirl-engine || return 1
    local mode
    mode=$(sudo -u "$(engine_user)" env PYTHONIOENCODING=utf-8 eirl-enginectl state 2>/dev/null |
        python3 -c 'import json,sys; print(json.load(sys.stdin)["output"]["mode"])' 2>/dev/null)
    [ "$mode" != off ]
}

exists() { systemctl cat "$1" >/dev/null 2>&1; }
enabled_of() { systemctl is-enabled "$1" 2>/dev/null | tail -1; }
active_of() { systemctl is-active "$1" 2>/dev/null; }
linger_of() { [ -e "/var/lib/systemd/linger/$1" ] && echo yes || echo no; }

recorded() { [ -f "$prev" ] && grep -q "^$1 " "$prev"; }
record() { recorded "$1" || echo "$1 $2 $3" >> "$prev"; }

# For «file:» items the .prev file stores «existia» (it existed) or «nuevo» (it is new): boards that are
# already slimmed have those words in their .prev files, so they must not change.
check() {
    printf '%-32s %-10s %-9s %s\n' unit now active before
    items | while read -r it; do
        if [ "${it%%:*}" = linger ]; then
            u=${it#linger:}
            now=$(linger_of "$u")
            before=$(grep "^$it " "$prev" 2>/dev/null | cut -d' ' -f2)
            printf '%-32s %-10s %-9s %s\n' "$it" "linger=$now" - "${before:+linger=$before}"
            continue
        fi
        if [ "${it%%:*}" = mask ]; then
            u=${it#mask:}
            printf '%-32s %-10s %-9s %s\n' "$u" "$(enabled_of "$u")" "$(active_of "$u")" "$(grep "^$it " "$prev" 2>/dev/null | cut -d' ' -f2-)"
            continue
        fi
        if [ "${it%%:*}" = file ]; then
            f=${it#file:}
            if cmp -s "$HERE/files/$(basename "$f")" "$f"; then now=installed; else now=pending; fi
            printf '%-32s %-10s %-9s %s\n' "$(basename "$f")" "$now" - "$(grep "^$it " "$prev" 2>/dev/null | cut -d' ' -f2)"
            continue
        fi
        if ! exists "$it"; then
            printf '%-32s %s\n' "$it" "(not on this board)"
            continue
        fi
        before=$(grep "^$it " "$prev" 2>/dev/null | cut -d' ' -f2-)
        printf '%-32s %-10s %-9s %s\n' "$it" "$(enabled_of "$it")" "$(active_of "$it")" "$before"
    done
}

apply() {
    if emitting && [ "$force" != --force ]; then
        die "streaming: nothing applied (--force only with the operator's OK)"
    fi
    items | while read -r it; do
        if [ "${it%%:*}" = linger ]; then
            u=${it#linger:}
            record "$it" "$(linger_of "$u")" -
            [ "$(linger_of "$u")" = yes ] || loginctl enable-linger "$u"
            echo "linger $u: yes"
            continue
        fi
        if [ "${it%%:*}" = mask ]; then
            u=${it#mask:}
            record "$it" "$(enabled_of "$u")" "$(active_of "$u")"
            systemctl mask "$u" >/dev/null 2>&1 || die "$u: could not mask it"
            echo "$u: masked"
            continue
        fi
        if [ "${it%%:*}" = file ]; then
            f=${it#file:}
            src=$HERE/files/$(basename "$f")
            [ -f "$src" ] || die "missing $src"
            if [ -e "$f" ]; then
                recorded "$it" || cp -p "$f" "$f.eirl-base-bak"
                record "$it" existia -
            else
                record "$it" nuevo -
            fi
            mkdir -p "$(dirname "$f")"
            install -m 0644 "$src" "$f"
            echo "$f: installed"
            continue
        fi
        if ! exists "$it"; then
            echo "$it: does not exist, skipped"
            continue
        fi
        en=$(enabled_of "$it")
        record "$it" "$en" "$(active_of "$it")"
        if [ "$en" = masked ]; then
            echo "$it: already masked"
            continue
        fi
        # disable --now does not stop SysV units (it hands them to update-rc.d): always stop.
        systemctl disable "$it" >/dev/null 2>&1
        systemctl stop "$it" >/dev/null 2>&1
        # mask fails if the unit file is in /etc/systemd/system: it stays disabled.
        if systemctl mask "$it" >/dev/null 2>&1; then
            echo "$it: out ($en → masked)"
        else
            echo "$it: out ($en → $(enabled_of "$it"); cannot be masked: unit file in /etc)"
        fi
    done
    systemctl reset-failed >/dev/null 2>&1
}

undo() {
    [ -f "$prev" ] || die "group $group is not applied (no $prev)"
    while read -r it en act; do
        if [ "${it%%:*}" = linger ]; then
            u=${it#linger:}
            [ "$en" = no ] && loginctl disable-linger "$u"
            echo "linger $u: $en"
            continue
        fi
        if [ "${it%%:*}" = mask ]; then
            systemctl unmask "${it#mask:}" >/dev/null 2>&1
            echo "${it#mask:}: unmasked (before: $en / $act)"
            continue
        fi
        if [ "${it%%:*}" = file ]; then
            f=${it#file:}
            if [ "$en" = existia ] && [ -f "$f.eirl-base-bak" ]; then mv "$f.eirl-base-bak" "$f"; else rm -f "$f"; fi
            echo "$f: restored ($en)"
            continue
        fi
        systemctl unmask "$it" >/dev/null 2>&1
        case $en in enabled|enabled-runtime) systemctl enable "$it" >/dev/null 2>&1 ;; esac
        [ "$act" = active ] && systemctl start "$it" >/dev/null 2>&1
        echo "$it: $(enabled_of "$it") / $(active_of "$it") (before: $en / $act)"
    done < "$prev"
    mv "$prev" "$prev.undone-$(date +%Y%m%d-%H%M%S)"
}

case $action in
    --check) check ;;
    --apply) apply ;;
    --undo) undo ;;
    *) die "unknown action: $action" ;;
esac
