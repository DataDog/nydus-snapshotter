## Requirements

### Registry

We need an https registry which supports the OCI referrer API.
It will be `my-registry` below.

### Nydus

See [config file](./nydus.toml). No modification to [nydusd config](./nydusd-config.json)
Modify the addresses of nydus and containerd as needed.

```toml
# The snapshotter's GRPC server socket, containerd will connect to plugin on this socket
address = "/nydus.sock"

[remote.auth]
# Containerd socket path
image_service_address = "/containerd.sock"
```

### Containerd

I used containerd 2.1 configured to leverage the transfer service with nydus. See [config file](./containerd.toml).
Modify the address of the nydus socker according to your setup.

 ```toml
[proxy_plugins.nydus]
# Path to nydus socket
address = "/nydus.sock"
```

### Prepare manifests

Both images are based on nginx:latest. They share 1 additional layer.
Image 2 has several layers that add a few directories/files on top.

```bash
docker buildx build -t my-registry/reproducer:1 -f ./Dockerfile.1 --push .
nydusify convert --source my-registry/reproducer:1 --target my-registry/reproducer:1-nydus --with-referrer
docker buildx build -t my-registry/reproducer:2 -f ./Dockerfile.2 --push .
```

In [container-1](./container-1.yaml) and [container-2](./container-2.yaml), modify the images to match your registry.

### Env variables

Export a bunch of env variable for easier debugging

```bash
CONTAINERD_ADDRESS=/containerd.sock
CONTAINERD_NAMESPACE=k8s.io
CONTAINERD_SNAPSHOTTER=nydus
RUNTIME_ENDPOINT=unix:///containerd.sock
IMAGE_SERVICE_ENDPOINT=unix:///nydus.sock
CONTAINER_RUNTIME_ENDPOINT=unix:///containerd.sock
```

## Reproducer

1. Start nydus-snapshotter and containerd
2. Create first pod with nydus referrer: `crictl run -t 60s container-1.yaml sandbox-1.yaml`
   1. it should succeed
   2. the logs should show `test1`:

```
$ sudo cat logs/container.1.log
2025-05-26T15:42:50.042292535+02:00 stdout F bin
2025-05-26T15:42:50.042329493+02:00 stdout F boot
2025-05-26T15:42:50.042332256+02:00 stdout F dev
2025-05-26T15:42:50.042334045+02:00 stdout F docker-entrypoint.d
2025-05-26T15:42:50.042336618+02:00 stdout F docker-entrypoint.sh
2025-05-26T15:42:50.042338296+02:00 stdout F etc
2025-05-26T15:42:50.042340048+02:00 stdout F home
2025-05-26T15:42:50.042341476+02:00 stdout F lib
2025-05-26T15:42:50.04234294+02:00 stdout F lib64
2025-05-26T15:42:50.042344649+02:00 stdout F media
2025-05-26T15:42:50.042346325+02:00 stdout F mnt
2025-05-26T15:42:50.04234776+02:00 stdout F opt
2025-05-26T15:42:50.04234923+02:00 stdout F proc
2025-05-26T15:42:50.042350712+02:00 stdout F root
2025-05-26T15:42:50.042352213+02:00 stdout F run
2025-05-26T15:42:50.042353777+02:00 stdout F sbin
2025-05-26T15:42:50.042355282+02:00 stdout F srv
2025-05-26T15:42:50.042356708+02:00 stdout F sys
2025-05-26T15:42:50.042358136+02:00 stdout F test1
2025-05-26T15:42:50.04235965+02:00 stdout F tmp
2025-05-26T15:42:50.042361128+02:00 stdout F usr
2025-05-26T15:42:50.042362657+02:00 stdout F var
```

3. Create second pod without nydus referrer: `crictl run -t 60s container-2.yaml sandbox-2.yaml`
   1. it may or may not succeed
   2. I've seen cases where it fails with

```
FATA[0010] running container: starting the container "f59fdb83bf27440e68aab8ee6a5927b8c112776e80df6ce3114fa7d41ee52425": rpc error: code = Unknown desc = failed to create containerd task: failed to create shim task: OCI runtime create failed: runc create failed: unable to start container process: exec: "/bin/bash": stat /bin/bash: no such file or directory
```

   3. If it does succeed, the logs should show that we are missing some content (test2, test3, test4 and file1 in this case)

```
$ sudo cat logs/container.2.log
2025-05-26T15:43:14.504964402+02:00 stdout F bin
2025-05-26T15:43:14.505001675+02:00 stdout F boot
2025-05-26T15:43:14.505006437+02:00 stdout F dev
2025-05-26T15:43:14.505009623+02:00 stdout F docker-entrypoint.d
2025-05-26T15:43:14.505013768+02:00 stdout F docker-entrypoint.sh
2025-05-26T15:43:14.505016585+02:00 stdout F etc
2025-05-26T15:43:14.505019409+02:00 stdout F home
2025-05-26T15:43:14.505022137+02:00 stdout F lib
2025-05-26T15:43:14.505025107+02:00 stdout F lib64
2025-05-26T15:43:14.505027881+02:00 stdout F media
2025-05-26T15:43:14.505030629+02:00 stdout F mnt
2025-05-26T15:43:14.505033404+02:00 stdout F opt
2025-05-26T15:43:14.505040219+02:00 stdout F proc
2025-05-26T15:43:14.505042648+02:00 stdout F root
2025-05-26T15:43:14.505045072+02:00 stdout F run
2025-05-26T15:43:14.505047542+02:00 stdout F sbin
2025-05-26T15:43:14.505049972+02:00 stdout F srv
2025-05-26T15:43:14.505052549+02:00 stdout F sys
2025-05-26T15:43:14.505057621+02:00 stdout F test1
2025-05-26T15:43:14.505060244+02:00 stdout F test5
2025-05-26T15:43:14.505062735+02:00 stdout F tmp
2025-05-26T15:43:14.50506539+02:00 stdout F usr
2025-05-26T15:43:14.505067981+02:00 stdout F var
```
