# Installation, upgrade and rollback

There is not yet a GitHub repository URL or a published binary release. Do not invent a release URL or download a binary from an unofficial mirror. Once the project repository/tag is published, the release archive should contain the CGO-free `mc-router`, SHA-256 checksum file, TOML example and license notices for `linux/amd64` and `linux/arm64`.

## Verify a future binary release

Use the exact archive URL published by the project owner, then verify it before extraction:

```sh
VERSION=v0.1.0
ARCHIVE="mc-router_${VERSION#v}_linux_amd64.tar.gz"
# Download ARCHIVE and the matching *_checksums.txt from the same GitHub Release.
sha256sum --check --ignore-missing mc-router_checksums.txt
mkdir -m 700 unpacked
 tar -xzf "$ARCHIVE" -C unpacked
./unpacked/mc-router --version
./unpacked/mc-router --help
./unpacked/mc-router --check-config --config ./unpacked/configs/config.example.toml
```

The version/owner/asset naming above is a GoReleaser packaging contract in this source tree, not evidence that a release exists. Verify the actual release page and filenames when one is published. Do not run a checksum file obtained from a different source than the archive as proof.

## Install a binary under systemd

Run commands as an administrator on a test host first. These commands assume a verified archive and systemd/Linux; they are not executed by `make`.

```sh
sudo useradd --system --home-dir /var/lib/mc-router --create-home \
  --shell /usr/sbin/nologin mc-router
sudo install -d -o root -g root -m 0755 /usr/local/bin
sudo install -m 0755 unpacked/mc-router /usr/local/bin/mc-router
sudo install -d -o root -g mc-router -m 0750 /etc/mc-router
sudo install -o root -g mc-router -m 0640 \
  unpacked/configs/config.example.toml /etc/mc-router/config.toml
sudoedit /etc/mc-router/config.toml
sudo -u mc-router /usr/local/bin/mc-router \
  --check-config --config /etc/mc-router/config.toml
sudo install -m 0644 deploy/systemd/mc-router-go.service \
  /etc/systemd/system/mc-router.service
sudo systemd-analyze verify /etc/systemd/system/mc-router.service
sudo systemctl daemon-reload
sudo systemctl start mc-router.service
sudo systemctl status mc-router.service --no-pager
sudo journalctl -u mc-router.service -n 50 --no-pager
```

The supplied unit expects the `mc-router` system account and config path above, starts a non-root process, sets `NoNewPrivileges`, protects system paths, applies task/memory/FD limits, and restarts only on process failure. It does **not** automatically enable on boot, open firewall ports, create Minecraft users, or move a backend. Confirm the config listener does not conflict before starting it.

Only after validating a separate listener and game login should an operator enable the unit on boot. In production, replacing a service already bound to `25565` requires a planned stop/start and verified rollback; this project does not alter that port by itself.

### Upgrade and rollback

Keep the previous verified binary and config owner-controlled. Stage the new binary at a versioned path, run `--version`, `--check-config` and systemd verification, then install it atomically to the path used by the unit. Restart deliberately and inspect `systemctl status`/`journalctl`. To rollback, restore the previous binary and config, restart the service and verify the listener/backend. Do not remove the Python router backup or rewrite live firewall rules as part of ordinary application upgrade.

## Docker Compose: standard mode only

The router container is unprivileged. The game backends must join the same user-defined Docker network. Create that network and ensure your backend services use DNS names matching `deploy/docker/config.example.toml` (or edit the routes before start):

```sh
docker network create minecraft
cp deploy/docker/config.example.toml config.docker.toml
# Edit config.docker.toml backend service names and route hostnames.
```

A minimal service invocation, after building an image locally:

```sh
docker run --rm --name mc-router \
  --network minecraft --read-only --cap-drop ALL \
  --security-opt no-new-privileges:true \
  -p 25565:25565/tcp \
  -v "$PWD/config.docker.toml:/etc/mc-router/config.toml:ro" \
  mc-router:local
```

The Dockerfile uses a multi-stage Go build and a non-root distroless runtime. The image has no Python, shell, Docker socket, network administration capability, host networking or transparent mode. Building locally requires Docker/BuildKit and the pinned Go base image. This command assumes the backend services already exist on `minecraft`; it does not start or authenticate them.

Do not map `127.0.0.1` from the sample container config: loopback in the router container points to the router itself. Do not expose backend ports to the Internet. Existing Docker `FORWARD` firewall policies can restrict egress; transparent mode is not a supported container profile.

## Source install / development build

Requires Go 1.27.3 or a newer compatible toolchain and the source tree:

```sh
go test ./...
go vet ./...
go build -trimpath -o ./mc-router ./cmd/mc-router
./mc-router --check-config --config configs/config.example.toml
```

No root, Docker, Python, system firewall changes or kernel capabilities are required for standard mode.
