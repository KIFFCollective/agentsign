# Releasing

Releases are built by [.github/workflows/release.yml](../.github/workflows/release.yml) when a version tag is pushed.

```sh
git checkout main && git pull
git tag -s v0.1.0 -m "v0.1.0"   # or -a if you don't sign tags
git push origin v0.1.0
```

The workflow:

1. runs the tests;
2. builds `agentsign` for Linux, macOS and Windows on amd64 and arm64, with `CGO_ENABLED=0`, `-trimpath` and commit timestamps, so builds are reproducible;
3. publishes a GitHub release with the archives and `checksums.txt`, and a changelog generated from merged PRs;
4. signs `checksums.txt` keylessly with Sigstore cosign, as `checksums.txt.sigstore.json`;
5. records SLSA build provenance for every archive, using GitHub artifact attestations.

Tags with a suffix (e.g. `v0.2.0-rc.1`) are marked as pre-releases.

Every push also runs a `release-dry-run` CI job: `goreleaser check` plus a snapshot build. A broken release config fails CI before anyone tags.

## Verifying a download

```sh
# 1. The archive matches the published checksum.
sha256sum --ignore-missing -c checksums.txt

# 2. checksums.txt was signed by this repo's release workflow for a v* tag.
cosign verify-blob --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp '^https://github.com/KIFFCollective/agentsign/\.github/workflows/release\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt

# 3. Or check the archive's build provenance directly.
gh attestation verify agentsign_0.1.0_linux_amd64.tar.gz -R KIFFCollective/agentsign
```

## Before the first release

- **Artifact attestations** work on public repos with no setup. Private repos need GitHub Enterprise Cloud.
- **Protect `v*` tags** (Settings > Rules > Rulesets, target tags `v*`, restrict creation) so only maintainers can trigger releases.
