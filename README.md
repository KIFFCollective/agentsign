# agentsign

Per-agent git identities on one GitHub machine account. (`agentsign` is a working name.)

Each coding agent gets its own commit email, SSH signing key and fine-grained token on a single machine account (e.g. `acme-bots`). Its commits show as **Verified** on GitHub under its own name. A CI check then confirms that each commit was signed by the key of the agent named as committer. GitHub doesn't check that part.

- No GitHub App per agent and no token minting. Works on personal-account repos.
- One shared git config for every agent. The calling agent is found at runtime from `--agent`, an environment variable or the repo's location.
- Plain git underneath: a credential helper, a `gpg.ssh.program` wrapper and environment variables.

See [docs/SPEC.md](docs/SPEC.md) for the design and [docs/security.md](docs/security.md) for the threat model.

> **Status:** early development (phase 1, MVP). Not yet released.

## Quickstart

```sh
go install github.com/KIFFCollective/agentsign/cmd/agentsign@latest

# Once per host: config, agent store and the shared git config.
agentsign init --global --env OMB_BOT_ID --path-rule '/workspaces/{agent}/'

# Once per agent.
agentsign agent add dev --name Dev --email dev@bots.acme.dev --repo acme/app
agentsign agent token dev --expires 2027-01-31 < token.txt

# In each repo.
agentsign signers --out .github/allowed_signers
```

Then, as the agent:

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
- uses: actions/checkout@v4
  with: { fetch-depth: 0 }
- uses: KIFFCollective/agentsign/action@main
  with:
    signers: .github/allowed_signers
    agent-domains: bots.acme.dev
```

Make the check required in branch protection.

## Development

```sh
go test ./...            # unit + e2e (e2e needs git 2.34+ and ssh-keygen)
go test -short ./...     # unit only
go build ./cmd/agentsign
```

## License

[Apache-2.0](LICENSE)
