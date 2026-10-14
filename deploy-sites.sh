# deploy-sites.sh — sourced by deploy.sh and first-install.sh: turns
# --site NAME or --host USER@HOST into the ssh target.  Not run on its own.
#
# The sites file maps each site to the host its UPS hangs off.  It is local
# and not in git: copy sites.example to sites and fill in your hosts, so the
# addresses stay out of the repo.  UPS_MQTT_SITES names another file.
#
# There is no default host, so a deploy always names the host it changes.

# resolve_target SITE HOST: prints the ssh target, or explains on stderr
# and returns 2.  HOST, when given, wins over the site's entry.
resolve_target() {
  local site=$1 host=$2 dir sites target names
  if [[ -n "$host" ]]; then
    printf '%s\n' "$host"
    return 0
  fi
  dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
  sites="${UPS_MQTT_SITES:-$dir/sites}"
  if [[ -z "$site" ]]; then
    names=""
    [[ -f "$sites" ]] && names=$(awk '{ sub(/\r$/, "") } $1 !~ /^#/ && $2 !~ /^#/ && NF >= 2 {printf "%s ", $1}' "$sites")
    echo "ERROR: name the host: --site NAME${names:+ (sites: ${names% })} or --host USER@HOST" >&2
    return 2
  fi
  if [[ ! -f "$sites" ]]; then
    echo "ERROR: no $sites: copy sites.example to sites and fill in your hosts (or pass --host)" >&2
    return 2
  fi
  target=$(awk -v s="$site" '{ sub(/\r$/, "") } $1 !~ /^#/ && $2 !~ /^#/ && $1 == s && NF >= 2 {print $2; exit}' "$sites")
  if [[ -z "$target" ]]; then
    echo "ERROR: site '$site' is not in $sites; add it or pass --host" >&2
    return 2
  fi
  printf '%s\n' "$target"
}
