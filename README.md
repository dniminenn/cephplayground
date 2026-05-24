# cephplayground

`cephplayground` runs a disposable Ceph cluster for application development.
It exposes the three primary Ceph data services on localhost:

- RGW (S3) for AWS SDK code
- CephFS for `ceph-fuse` or `mount.ceph` clients
- RBD for `rbd-nbd` or `rbd map` clients

It is designed for cases where MinIO is useful but not close enough to Ceph,
and where a real cluster is more than you want to operate. State lives under
`/tmp/cephplayground/<name>`; on systems where `/tmp` is tmpfs, nothing
persists across reboots.

Module path: `github.com/dniminenn/cephplayground`

The tool intentionally avoids `cephadm`. It runs one Ceph container with
direct Ceph daemons inside it:

- 1 monitor
- 1 manager
- 1 OSD
- 1 MDS (when CephFS is enabled)
- 1 RGW (when RGW is enabled)
- one host-managed loop device backed by a file under the state directory
- pool replica size 1

## Requirements

- Linux
- Docker or Podman
- `losetup`
- root privileges for `launch` and `destroy`
- a tmpfs-backed state directory if you want to avoid SSD writes for OSD data
- `ceph-common` on the host for CephFS and RBD client tooling

The default image is `quay.io/ceph/ceph:v19`. Override it with `--image` to
test a specific Ceph release.

## Quick Start

Build the binary:

```sh
make
```

Install it somewhere on `PATH`:

```sh
sudo install -m 0755 bin/cephplayground /usr/local/bin/cephplayground
```

Check the host first:

```sh
cephplayground doctor
```

Start the playground (all three services by default):

```sh
sudo cephplayground launch --osd-size 16GiB
```

Pick a subset with `--services`:

```sh
sudo cephplayground launch --services rgw
sudo cephplayground launch --services cephfs,rbd
```

`launch` prints the endpoint and the full set of client variables. Export
them with:

```sh
eval "$(cephplayground env)"
```

Destroy the playground:

```sh
sudo cephplayground destroy
```

## Commands

```text
launch          Create and start the playground
destroy         Stop and remove the playground state
reset           Destroy then launch again
status          Show state and probe the RGW endpoint
env             Print client environment variables
shell           Open a shell in the playground container
logs            Follow container logs
doctor          Check host prerequisites
```

## Defaults

```text
name:          rgw
state dir:     /tmp/cephplayground/rgw
container:     cephplay-rgw
image:         quay.io/ceph/ceph:v19
services:      rgw,cephfs,rbd
OSD image:     16 GiB sparse file
OSD memory:    1 GiB target
RGW port:      7480
S3 region:     us-east-1
S3 access key: play
S3 secret key: playsecret
CephFS name:   playfs
CephFS client: cephplay-fs
RBD pool:      rbd
RBD image:     play (1 GiB, layering only)
RBD client:    cephplay-rbd
```

## Network Model

With `--services rgw` only, the container uses Docker port forwarding and
exposes `127.0.0.1:<rgw-port>:7480`.

When CephFS or RBD is included, the container switches to `--network host`
so that mon, OSD, and MDS are reachable directly on host loopback. RGW still
binds to `127.0.0.1:<rgw-port>` in this mode.

## Client Variables

`launch` and `env` print a layered block of shell exports. The S3 block is
unchanged from earlier versions of this tool, so existing AWS-SDK code keeps
working:

```sh
# RGW (S3); drop-in for AWS SDKs
export AWS_ACCESS_KEY_ID='play'
export AWS_SECRET_ACCESS_KEY='playsecret'
export AWS_REGION='us-east-1'
export AWS_DEFAULT_REGION='us-east-1'
export AWS_ENDPOINT_URL='http://127.0.0.1:7480'
export CEPHPLAY_ENDPOINT='http://127.0.0.1:7480'
export CEPHPLAY_REGION='us-east-1'

# Ceph cluster; point ceph-common at these
export CEPH_CONF='/tmp/cephplayground/rgw/ceph.conf'
export CEPHPLAY_FSID='...'
export CEPHPLAY_MON_HOST='127.0.0.1'
export CEPHPLAY_ADMIN_KEYRING='/tmp/cephplayground/rgw/ceph.client.admin.keyring'

# CephFS; mount with ceph-fuse or mount.ceph
export CEPHPLAY_CEPHFS_NAME='playfs'
export CEPHPLAY_CEPHFS_CLIENT='cephplay-fs'
export CEPHPLAY_CEPHFS_KEYRING='/tmp/cephplayground/rgw/ceph.client.cephplay-fs.keyring'

# RBD; map with rbd-nbd or rbd map
export CEPHPLAY_RBD_POOL='rbd'
export CEPHPLAY_RBD_IMAGE='play'
export CEPHPLAY_RBD_CLIENT='cephplay-rbd'
export CEPHPLAY_RBD_KEYRING='/tmp/cephplayground/rgw/ceph.client.cephplay-rbd.keyring'
```

## Client Examples

S3 via MinIO Go client:

```go
import (
    "github.com/minio/minio-go/v7"
    "github.com/minio/minio-go/v7/pkg/credentials"
)

client, err := minio.New("127.0.0.1:7480", &minio.Options{
    Creds:  credentials.NewStaticV4("play", "playsecret", ""),
    Secure: false,
    Region: "us-east-1",
})
```

CephFS via `ceph-fuse`:

```sh
sudo ceph-fuse \
  --id "$CEPHPLAY_CEPHFS_CLIENT" \
  --conf "$CEPH_CONF" \
  -k "$CEPHPLAY_CEPHFS_KEYRING" \
  -r / /mnt/playfs
```

RBD via `rbd-nbd`:

```sh
sudo rbd-nbd map \
  --id "$CEPHPLAY_RBD_CLIENT" \
  --conf "$CEPH_CONF" \
  -k "$CEPHPLAY_RBD_KEYRING" \
  "$CEPHPLAY_RBD_POOL/$CEPHPLAY_RBD_IMAGE"
```

## Safety Model

`cephplayground destroy` refuses to remove a state directory unless it
contains a `.cephplayground` marker whose content matches the tool name.

The tool does not scan host disks. It creates exactly one sparse OSD image
under the state directory, attaches it as a loop device, and passes only
that loop device into the container. The default path does not require
`--privileged`.

The generated credentials are development credentials. They are written to
the state directory so client tools can read them without running as root.

## SSD Avoidance

The runtime state defaults to `/tmp/cephplayground/<name>`. On systems where
`/tmp` is tmpfs, OSD data and all generated cluster state stay in RAM.

Docker or Podman image layers may still live wherever the container runtime
stores images. That is separate from playground data.

## Why Not nspawn?

`systemd-nspawn` is a good tool for a local Linux appliance, but not the
right default for a public developer tool. Docker and Podman are more common,
work across more developer machines, and let this project avoid host-distro
rootfs creation entirely.

`cephplayground` still avoids running `cephadm` inside a container. The
container starts Ceph's daemons directly so there is no nested container
runtime.

## Non-goals

- Durable storage
- HA behavior
- Replication testing
- Production Ceph topology
- Kubernetes object bucket integration

## Architecture Notes

The host binary owns the state directory, the marker-based cleanup guard,
the sparse OSD image, the loop device, and the container lifecycle. The
container entrypoint owns Ceph configuration, keyrings, the monitor map,
and the per-service daemons and pools.

`launch --services` resolves a comma-separated subset of `rgw,cephfs,rbd`.
The entrypoint branches on the resolved list; daemons and pools that are
not requested are never created. Files written to the state directory
(`ceph.conf`, admin keyring, per-service keyrings) sit at predictable paths
so clients can consume them without parsing launch output.

Inside the container, `ceph-volume raw` prepares and activates the OSD
against the loop device passed in from the host, then the bootstrap script
starts `ceph-osd` directly. `destroy` stops the container, detaches the
known loop device, and removes the marked state directory.

`launch`/`up` and `destroy`/`down` are aliased for users who expect
Docker Compose-style naming; `launch` and `destroy` are the documented
verbs.
