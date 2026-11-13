# agentsign

> **Status:** early development. v0.1.0 is the first release; expect breaking changes before v1.0. We uh, don't even know if this is a good idea atm.

Per-agent git identities on one GitHub machine account. (`agentsign` is a working name.)

Each coding agent gets its own commit email, SSH signing key and fine-grained token on a single machine account (e.g. `acme-bots`). Its commits show as **Verified** on GitHub. Each commit names its agent in an `Agent:` trailer and in a per-commit status, because GitHub itself shows every agent as the shared account. A CI check then confirms that each commit was signed by the key of the agent named as committer. GitHub doesn't check that part.

- No GitHub App per agent and no token minting. Works on personal-account repos.
- One shared git config for every agent. The calling agent is found at runtime from `--agent`, an environment variable or the repo's location.
- Plain git underneath: a credential helper, a `gpg.ssh.program` wrapper and environment variables.

See [docs/SPEC.md](docs/SPEC.md) for the design and [docs/security.md](docs/security.md) for the threat model.



## Install

Download a binary for your platform from [Releases](https://github.com/KIFFCollective/agentsign/releases). Each release has a checksum file signed with Sigstore and build provenance attached; see [docs/releasing.md](docs/releasing.md#verifying-a-download) for how to verify a download. Or build from source:

```sh
go install github.com/KIFFCollective/agentsign/cmd/agentsign@latest
```

## Quickstart

```sh

# Once per host: config, agent store and the shared git config.
# --global takes over all git on this account: agent hosts only. On your own
# machine use --file ~/.config/agentsign/gitconfig and GIT_CONFIG_GLOBAL.
agentsign init --global --env OMB_BOT_ID --path-rule '/workspaces/{agent}/'

# Once per agent.
agentsign agent add dev --name Dev --email dev@bots.acme.dev --repo acme/app
agentsign agent token dev --expires 2027-01-31 < token.txt

# In each repo.
agentsign signers --out .github/allowed_signers
```

Then, as the agent (on hosts with the [git and gh shims](docs/quickstart.md#shims-plain-git-and-gh), plain `git` and `gh` do all of this and nothing else is needed):

```sh
export AGENTSIGN_AGENT=dev
eval "$(agentsign env)"          # sets GIT_AUTHOR_* / GIT_COMMITTER_*
git commit -m "..."              # signed with dev's key
git push                         # authenticated with dev's token
agentsign doctor                 # check everything is wired up
```

See [docs/quickstart.md](docs/quickstart.md) for the GitHub side (emails, signing keys, tokens, branch protection).

### Repo access

`--repo` limits which repos an agent's token is sent for. Repeat it for several repos, use `owner/*` for all of an owner's repos, or leave it out for all repos. Change the list later with `agentsign agent repo list|add|remove|restrict|allow-all`. The token's own scope on GitHub is the real limit, so keep the two in step. See [Repo access](docs/quickstart.md#repo-access).

## Verifying in CI

```yaml
on: pull_request
permissions:
  contents: read
  statuses: write             # for the per-commit "agentsign/signer" status
jobs:
  verify:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v5
        with: { fetch-depth: 0 }
      - uses: KIFFCollective/agentsign/action@main
        with:
          signers: .github/allowed_signers
          agent-domains: bots.acme.dev
          humans: github-verified   # or allow (default) / deny
```

Make the check required in branch protection.

On GitHub, every agent shows as the machine account. To show who really made each commit, agentsign adds an `Agent: Pip <pip@bots.acme.dev>` trailer to commit messages and the Action posts an `agentsign/signer` status on every commit. `verify` checks both against the signing key. See [How agent commits look on GitHub](docs/quickstart.md#how-agent-commits-look-on-github).

## Development

```sh
go test ./...            # unit + e2e (e2e needs git 2.34+ and ssh-keygen)
go test -short ./...     # unit only
go build ./cmd/agentsign
```

## License

[Apache-2.0](LICENSE)
