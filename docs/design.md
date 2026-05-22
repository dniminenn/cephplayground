# Design

## Goal

Provide a local RGW endpoint that behaves like Ceph RGW without asking app
developers to operate a real Ceph cluster or risk host disks.

## Non-goals

- Durable storage
- HA behavior
- Replication testing
- Production Ceph topology
- Kubernetes object bucket integration

## Architecture

The host binary manages:

- state directory creation
- marker-based cleanup guards
- OSD image creation
- host loop device attachment
- Docker/Podman container lifecycle
- port forwarding

The container bootstrap manages:

- monitor keyrings and monmap
- one monitor
- one manager
- one OSD
- one RGW
- one RGW S3 user

## Runtime Choice

Docker/Podman is the default backend because it is more portable for developers
than `systemd-nspawn` and avoids distro-specific rootfs tools such as
`pacstrap` or `debootstrap`.

The tool does not run `cephadm`. It uses a Ceph container image only as a process
environment for direct Ceph daemons.

## OSD Provisioning

The host creates one sparse image under the state directory and attaches it to a
loop device. Only that loop device is passed into the container.

Inside the container, `ceph-volume raw` prepares and activates the OSD against
the loop device. The bootstrap script then starts `ceph-osd` directly.

This keeps the host cleanup model clear: `destroy` stops the container, removes
it, detaches the known loop device, and removes the marked state directory.

## Cleanup Contract

Cleanup is allowed only when the target state directory contains:

```text
.cephplayground
```

with content:

```text
cephplayground
```

The primary destructive verb is `destroy`. `down` is accepted as an alias for
users who expect Docker Compose-style naming, but it is not the documented
interface.

The primary start verb is `launch`. `up` is accepted as an alias for the same
reason.
