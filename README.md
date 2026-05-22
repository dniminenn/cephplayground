# cephplayground

`cephplayground` runs a disposable Ceph RGW playground for application
development. It is designed for the case where MinIO is useful but not close
enough to Ceph RGW behavior.

Module path: `github.com/dniminenn/cephplayground`

The tool intentionally avoids `cephadm`. It runs one Ceph container with direct
Ceph daemons inside it:

- 1 monitor
- 1 manager
- 1 OSD
- 1 RGW
- pool replica size 1
- one host-managed loop device backed by a file under the state directory

The default state directory is `/tmp/cephplayground/rgw`.

## Requirements

- Linux
- Docker or Podman
- `losetup`
- root privileges for `launch` and `destroy`
- a tmpfs-backed state directory if you want to avoid SSD writes for OSD data

The default image is `quay.io/ceph/ceph:v19`. Override it with `--image` if you
want to test a specific Ceph release.

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

Start the playground:

```sh
sudo cephplayground launch --osd-size 16GiB --rgw-port 7480
```

`launch` prints the endpoint and shell exports your app can use.

Export client variables:

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
env             Print AWS-compatible environment variables
shell           Open a shell in the playground container
logs            Follow container logs
doctor          Check host prerequisites
```

## Defaults

```text
name: rgw
state dir: /tmp/cephplayground/rgw
container: cephplay-rgw
image: quay.io/ceph/ceph:v19
OSD image: 16 GiB sparse file
OSD memory target: 1 GiB
RGW port: 7480
S3 access key: play
S3 secret key: playsecret
S3 region: us-east-1
```

Default launch output includes:

```sh
export AWS_ACCESS_KEY_ID='play'
export AWS_SECRET_ACCESS_KEY='playsecret'
export AWS_REGION='us-east-1'
export AWS_DEFAULT_REGION='us-east-1'
export AWS_ENDPOINT_URL='http://127.0.0.1:7480'
export CEPHPLAY_ENDPOINT='http://127.0.0.1:7480'
export CEPHPLAY_REGION='us-east-1'
```

For the MinIO Go client:

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

## Safety Model

`cephplayground destroy` refuses to remove a state directory unless it contains
a `.cephplayground` marker whose content matches the tool name.

The tool does not scan host disks. It creates exactly one sparse OSD image under
the state directory, attaches it as a loop device, and passes only that loop
device into the container. The default path does not require `--privileged`.

The generated S3 credentials are development credentials. They are written to
the state directory so `cephplayground env` can print them for your app.

## SSD Avoidance

The runtime state defaults to `/tmp/cephplayground/rgw`. On systems where `/tmp`
is tmpfs, OSD data and generated Ceph playground state stay in RAM.

Docker or Podman image layers may still live wherever your container runtime
stores images. That is separate from playground object data.

## Why Not nspawn?

`systemd-nspawn` is a good tool for a local Linux appliance, but it is not the
right default for a public developer tool. Docker and Podman are more common,
work across more developer machines, and let this project avoid host-distro
rootfs creation entirely.

`cephplayground` still avoids running `cephadm` inside a container. The container
starts Ceph's daemons directly so there is no nested container runtime.
