# om3

[![Main Go Docker Push (Latest)](https://github.com/opensvc/om3/actions/workflows/main-go-docker-push-latest.yml/badge.svg?branch=main)](https://github.com/opensvc/om3/actions/workflows/main-go-docker-push-latest.yml)
[![Release Go Docker Push (Versioned)](https://github.com/opensvc/om3/actions/workflows/release-go-docker-push-version.yml/badge.svg)](https://github.com/opensvc/om3/actions/workflows/release-go-docker-push-version.yml)

*The Go port and continuation of [opensvc/opensvc](https://github.com/opensvc/opensvc).*

om3 is a cluster orchestrator for small-to-mid clusters — a coherent alternative to
stitching together systemd, container runtimes, and ad-hoc scripts, with symmetric
orchestration on every node.

## Features

* Targeted at 1-32 node clusters, human-readable service configurations and status
* Highly redundant cluster communications (unicast, multicast, disk, third-site relay)
* Deploy application stacks on containers, persistent or volatile volumes, or
  low-level resources — disks, volume groups, filesystems, IP addresses, app launchers
* Native, zeroconf, encrypted datastores, with keys exposed as files for your deployments
* Manage everything through the API, the `om` command line, or the `ox` terminal UI

## Install

See the [installation guide](https://book.opensvc.com/agent/install.html) — the
complete documentation lives there too.

## Play

Deploy a simple service:
```
om test/svc/simple deploy --kw fs#1.type=flag --kw nodes='*' --kw orchestrate=ha
```
Then open an `ox` session and watch it move around the cluster.

## Examples

Browse [ready-to-use templates](https://github.com/opensvc/opensvc_templates) — VIPs,
cluster DNS, ingress gateways, ACME, HA NFS, meshed VPN backend networks, and more.

## AI client

`om ai` obtains a short-lived OpenSVC access JWT from the daemon and contacts
the independent AI agent over HTTPS on TCP. The default remote endpoint is
`https://ai-agent.opensvc.com` (port 443). Provision this DNS name and HTTPS
service, or select your deployment with `OPENSVC_AI_AGENT_URL`:

```bash
export OPENSVC_AI_AGENT_URL=https://agent.example.com:8090
om ai ask "Assess the health of my cluster"
om ai chat
om ai list
```

The URL must be an HTTPS origin: no credentials, path, query or fragment.
Certificate chain and hostname verification are mandatory, with TLS 1.2 or
newer. Public certificates use system roots. For a private TLS CA, optionally
set `OPENSVC_AI_AGENT_CA_FILE` to an absolute path to a PEM CA bundle (maximum
1 MiB); this bundle replaces system roots for the agent connection. This
setting controls TLS trust, not JWT signature validation.

There is no localhost or Unix-socket fallback, no insecure TLS mode, and no
proxy or redirect forwarding. The daemon connection used to obtain the JWT is
unchanged; this migration does not add OAuth login, provider credentials, or
multi-cluster routing. The agent must still trust the target cluster's public
JWT signing key.

## Docker Image Signing

All OpenSVC Docker images published to `ghcr.io/opensvc` are signed with [Cosign](https://docs.sigstore.dev/cosign/) using Sigstore's keyless signing via GitHub Actions OIDC. The signing is performed automatically by the [release workflow](.github/workflows/release-go-docker-push-version.yml) on Git tag creation.

To verify an image signature:

```bash
cosign verify \
  --certificate-oidc-issuer=https://token.actions.githubusercontent.com \
  --certificate-identity-regexp='^https://github.com/opensvc/om3/.github/workflows/release-go-docker-push-version.yml@' \
  ghcr.io/opensvc/om:TAG
```

Replace `TAG` with the version (e.g., `3.0.0-rc30`). Note that the `v` prefix from Git tags is stripped in Docker image tags.
