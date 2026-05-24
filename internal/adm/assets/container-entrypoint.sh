#!/usr/bin/env bash
set -euo pipefail

CEPHPLAY_DIR=${CEPHPLAY_DIR:-/cephplay}
CONFIG=${CEPHPLAY_DIR}/config.env
READY=${CEPHPLAY_DIR}/ready

log() {
  printf '[cephplayground] %s\n' "$*"
}

die() {
  log "fatal: $*"
  exit 1
}

need() {
  command -v "$1" >/dev/null 2>&1 || die "missing required command: $1"
}

has_service() {
  local target=$1
  local svc
  IFS=',' read -ra _svcs <<<"${SERVICES:-rgw}"
  for svc in "${_svcs[@]}"; do
    [[ "$svc" == "$target" ]] && return 0
  done
  return 1
}

wait_for_ceph() {
  local i
  for i in $(seq 1 120); do
    if ceph -s >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  die "ceph CLI did not become ready"
}

wait_for_osd() {
  local i
  for i in $(seq 1 120); do
    if ceph osd stat 2>/dev/null | grep -q '1 osds: 1 up'; then
      return 0
    fi
    sleep 1
  done
  die "OSD did not become up"
}

wait_for_rgw_admin() {
  local i
  for i in $(seq 1 120); do
    if radosgw-admin user list >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  die "RGW admin commands did not become ready"
}

wait_for_rgw_http() {
  local i
  for i in $(seq 1 120); do
    if (exec 3<>/dev/tcp/127.0.0.1/"$RGW_BIND_PORT") 2>/dev/null; then
      exec 3<&- 3>&-
      return 0
    fi
    if ! kill -0 "$RGW_PID" 2>/dev/null; then
      die "radosgw exited before binding port $RGW_BIND_PORT (check container logs above)"
    fi
    sleep 1
  done
  die "radosgw did not start listening on port $RGW_BIND_PORT"
}

wait_for_mds_active() {
  local fs=$1
  local i
  for i in $(seq 1 120); do
    if ceph fs status "$fs" 2>/dev/null | grep -q 'active'; then
      return 0
    fi
    sleep 1
  done
  die "MDS for $fs did not become active"
}

shutdown() {
  set +e
  rm -f "$READY"
  for pid in "${PIDS[@]:-}"; do
    kill "$pid" >/dev/null 2>&1 || true
  done
  wait >/dev/null 2>&1 || true
}

start_daemon() {
  "$@" &
  PIDS+=("$!")
}

need ceph
need ceph-authtool
need ceph-mon
need ceph-mgr
need ceph-osd
need ceph-volume
need monmaptool
need radosgw
need radosgw-admin

test -f "$CONFIG" || die "missing $CONFIG"
# shellcheck source=/dev/null
. "$CONFIG"

: "${SERVICES:=rgw}"
: "${RGW_BIND_ADDR:=0.0.0.0}"
: "${RGW_BIND_PORT:=7480}"
: "${HOST_NETWORK:=0}"
: "${CEPH_CONF_FILE:=ceph.conf}"
: "${ADMIN_KEYRING_FILE:=ceph.client.admin.keyring}"
: "${CEPHFS_NAME:=playfs}"
: "${CEPHFS_CLIENT:=cephplay-fs}"
: "${CEPHFS_KEYRING_FILE:=ceph.client.cephplay-fs.keyring}"
: "${RBD_POOL:=rbd}"
: "${RBD_IMAGE:=play}"
: "${RBD_CLIENT:=cephplay-rbd}"
: "${RBD_KEYRING_FILE:=ceph.client.cephplay-rbd.keyring}"

has_service cephfs && CEPHFS_ENABLED=1 || CEPHFS_ENABLED=0
has_service rbd && RBD_ENABLED=1 || RBD_ENABLED=0
has_service rgw && RGW_ENABLED=1 || RGW_ENABLED=0

log "services: ${SERVICES}"

test -b "$OSD_DEVICE" || die "$OSD_DEVICE is not a block device"
id ceph >/dev/null 2>&1 || die "container image must have a ceph user"

if [[ "$CEPHFS_ENABLED" == "1" ]]; then need ceph-mds; fi
if [[ "$RBD_ENABLED" == "1" ]]; then need rbd; fi

PIDS=()
trap shutdown EXIT TERM INT

install -d -m 0755 /etc/ceph
install -d -o ceph -g ceph -m 0755 \
  /run/ceph \
  /var/log/ceph \
  /var/lib/ceph/bootstrap-osd \
  /var/lib/ceph/mon/ceph-play \
  /var/lib/ceph/mgr/ceph-play \
  /var/lib/ceph/radosgw/ceph-rgw.play
if [[ "$CEPHFS_ENABLED" == "1" ]]; then
  install -d -o ceph -g ceph -m 0755 /var/lib/ceph/mds/ceph-play
fi

cat >/etc/ceph/ceph.conf <<EOF_CONF
[global]
fsid = ${FSID}
mon_initial_members = play
mon_host = 127.0.0.1
public_network = 127.0.0.0/8
cluster_network = 127.0.0.0/8
auth_cluster_required = cephx
auth_service_required = cephx
auth_client_required = cephx
osd_pool_default_size = 1
osd_pool_default_min_size = 1
osd_pool_default_pg_num = 1
osd_pool_default_pgp_num = 1
osd_crush_chooseleaf_type = 0
osd_memory_target = ${OSD_MEMORY_TARGET}
mon_warn_on_pool_no_redundancy = false
mon_allow_pool_delete = true
mon_data_avail_warn = 5
mon_max_pg_per_osd = 1000
mgr_standby_modules = false
log_to_file = false
log_to_stderr = true

[client.rgw.play]
rgw_frontends = beast endpoint=${RGW_BIND_ADDR}:${RGW_BIND_PORT}
rgw_enable_usage_log = true
EOF_CONF

ceph-authtool --create-keyring /tmp/ceph.mon.keyring --gen-key -n mon. --cap mon 'allow *'
ceph-authtool --create-keyring /etc/ceph/ceph.client.admin.keyring \
  --gen-key -n client.admin \
  --cap mon 'allow *' \
  --cap osd 'allow *' \
  --cap mgr 'allow *' \
  --cap mds 'allow *'
ceph-authtool --create-keyring /var/lib/ceph/bootstrap-osd/ceph.keyring \
  --gen-key -n client.bootstrap-osd \
  --cap mon 'profile bootstrap-osd' \
  --cap mgr 'allow r'
ceph-authtool /tmp/ceph.mon.keyring --import-keyring /etc/ceph/ceph.client.admin.keyring
ceph-authtool /tmp/ceph.mon.keyring --import-keyring /var/lib/ceph/bootstrap-osd/ceph.keyring
chown ceph:ceph /tmp/ceph.mon.keyring /etc/ceph/ceph.client.admin.keyring /var/lib/ceph/bootstrap-osd/ceph.keyring

monmaptool --create --add play 127.0.0.1 --fsid "$FSID" /tmp/monmap
chown ceph:ceph /tmp/monmap
ceph-mon --mkfs -i play --monmap /tmp/monmap --keyring /tmp/ceph.mon.keyring --setuser ceph --setgroup ceph

log "starting monitor"
start_daemon ceph-mon -f --cluster ceph -i play --setuser ceph --setgroup ceph
wait_for_ceph

ceph auth get-or-create mgr.play \
  mon 'allow profile mgr' \
  osd 'allow *' \
  mds 'allow *' >/var/lib/ceph/mgr/ceph-play/keyring
chown -R ceph:ceph /var/lib/ceph/mgr/ceph-play

log "starting manager"
start_daemon ceph-mgr -f --cluster ceph -i play --setuser ceph --setgroup ceph

ceph config set global osd_pool_default_size 1 || true
ceph config set global osd_pool_default_min_size 1 || true
ceph config set global osd_pool_default_pg_num 1 || true
ceph config set global osd_pool_default_pgp_num 1 || true
ceph config set global mon_warn_on_pool_no_redundancy false || true
ceph config set global mon_max_pg_per_osd 1000 || true
ceph config set global osd_memory_target "$OSD_MEMORY_TARGET" || true

OSD_ID=${OSD_ID:-0}

log "preparing OSD on ${OSD_DEVICE}"
if ! ceph-volume raw prepare \
    --bluestore \
    --data "$OSD_DEVICE" \
    --osd-id "$OSD_ID"; then
  log "raw prepare with explicit OSD id failed; retrying fresh-cluster prepare"
  ceph-volume raw prepare \
    --bluestore \
    --data "$OSD_DEVICE"
fi
ceph-volume raw activate --device "$OSD_DEVICE" --no-systemd >/dev/null 2>&1 || true

log "starting OSD"
start_daemon ceph-osd -f --cluster ceph -i "$OSD_ID" --setuser ceph --setgroup ceph
wait_for_osd

if [[ "$RGW_ENABLED" == "1" ]]; then
  ceph auth get-or-create client.rgw.play \
    mon 'allow rw' \
    osd 'allow rwx' >/var/lib/ceph/radosgw/ceph-rgw.play/keyring
  chown -R ceph:ceph /var/lib/ceph/radosgw/ceph-rgw.play

  # Ceph Squid (v19) no longer auto-creates the default realm/zonegroup/zone;
  # radosgw fails with "failed to load zonegroup" if we don't bootstrap them first.
  if ! radosgw-admin zonegroup get --rgw-zonegroup=default >/dev/null 2>&1; then
    log "bootstrapping RGW realm/zonegroup/zone"
    radosgw-admin realm create --rgw-realm=default --default >/dev/null
    radosgw-admin zonegroup create --rgw-zonegroup=default --endpoints="http://127.0.0.1:${RGW_PORT}" --master --default >/dev/null
    radosgw-admin zone create --rgw-zonegroup=default --rgw-zone=default --endpoints="http://127.0.0.1:${RGW_PORT}" --master --default >/dev/null
    radosgw-admin period update --commit >/dev/null
  fi

  log "starting RGW"
  start_daemon radosgw -f --cluster ceph --name client.rgw.play --setuser ceph --setgroup ceph
  RGW_PID=${PIDS[-1]}
  wait_for_rgw_admin
  wait_for_rgw_http

  if ! radosgw-admin user info --uid "$S3_UID" >/dev/null 2>&1; then
    radosgw-admin user create \
      --uid "$S3_UID" \
      --display-name "$S3_UID" \
      --access-key "$AWS_ACCESS_KEY_ID" \
      --secret-key "$AWS_SECRET_ACCESS_KEY" >/dev/null
  fi
fi

if [[ "$CEPHFS_ENABLED" == "1" ]]; then
  log "creating CephFS ${CEPHFS_NAME}"
  ceph osd pool create cephfs_metadata 1 1 >/dev/null 2>&1 || true
  ceph osd pool create cephfs_data 1 1 >/dev/null 2>&1 || true
  if ! ceph fs ls 2>/dev/null | grep -q "name: ${CEPHFS_NAME}"; then
    ceph fs new "$CEPHFS_NAME" cephfs_metadata cephfs_data --force >/dev/null
  fi

  ceph auth get-or-create mds.play \
    mon 'allow profile mds' \
    osd 'allow rwx' \
    mds 'allow' >/var/lib/ceph/mds/ceph-play/keyring
  chown -R ceph:ceph /var/lib/ceph/mds/ceph-play

  log "starting MDS"
  start_daemon ceph-mds -f --cluster ceph -i play --setuser ceph --setgroup ceph
  wait_for_mds_active "$CEPHFS_NAME"

  # Scoped client for fs mounts
  ceph fs authorize "$CEPHFS_NAME" "client.${CEPHFS_CLIENT}" / rw >"${CEPHPLAY_DIR}/${CEPHFS_KEYRING_FILE}"
  chmod 0644 "${CEPHPLAY_DIR}/${CEPHFS_KEYRING_FILE}"
fi

if [[ "$RBD_ENABLED" == "1" ]]; then
  log "creating RBD pool ${RBD_POOL}"
  ceph osd pool create "$RBD_POOL" 1 1 >/dev/null 2>&1 || true
  ceph osd pool application enable "$RBD_POOL" rbd >/dev/null 2>&1 || true
  rbd pool init "$RBD_POOL" >/dev/null 2>&1 || true

  if ! rbd info "${RBD_POOL}/${RBD_IMAGE}" >/dev/null 2>&1; then
    log "creating sample RBD image ${RBD_POOL}/${RBD_IMAGE} (1GiB)"
    # layering only; krbd is happiest with feature set restricted, rbd-nbd handles anything
    rbd create --size 1024 --image-feature layering "${RBD_POOL}/${RBD_IMAGE}"
  fi

  ceph auth get-or-create "client.${RBD_CLIENT}" \
    mon 'profile rbd' \
    osd "profile rbd pool=${RBD_POOL}" >"${CEPHPLAY_DIR}/${RBD_KEYRING_FILE}"
  chmod 0644 "${CEPHPLAY_DIR}/${RBD_KEYRING_FILE}"
fi

# Export ceph.conf and admin keyring for host clients
cp /etc/ceph/ceph.conf "${CEPHPLAY_DIR}/${CEPH_CONF_FILE}"
cp /etc/ceph/ceph.client.admin.keyring "${CEPHPLAY_DIR}/${ADMIN_KEYRING_FILE}"
chmod 0644 "${CEPHPLAY_DIR}/${CEPH_CONF_FILE}" "${CEPHPLAY_DIR}/${ADMIN_KEYRING_FILE}"

touch "$READY"
if [[ "$RGW_ENABLED" == "1" ]]; then
  log "ready: http://127.0.0.1:${RGW_PORT}"
else
  log "ready"
fi

while true; do
  for pid in "${PIDS[@]}"; do
    if ! kill -0 "$pid" >/dev/null 2>&1; then
      log "daemon pid ${pid} exited"
      exit 1
    fi
  done
  sleep 2
done
