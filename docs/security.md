# Security model

## What GitHub verifies, and what agentsign adds

GitHub marks an SSH-signed commit **Verified** when the signing key and the committer email both belong to the same account. With every agent on one machine account, agent A could sign a commit carrying agent B's email and GitHub would still mark it Verified.

agentsign closes this gap in two places:

1. **Locally:** the signer reads the object being signed and refuses to sign unless the committer (or tagger) email is the calling agent's own email.
2. **In CI (the real boundary):** `agentsign verify` checks every commit whose committer is a registered agent. The signature must be valid, and the key's principal in `allowed_signers` must equal the committer email.

## Threats

| Threat | Mitigation |
|---|---|
| Agent A signs as agent B | Keys are stored per agent; in a sandbox, A can't read B's folder. The signer refuses mismatched emails. CI checks that the signer matches the committer. |
| A key or token leaks | Each token reaches only that agent's repos. Revoke just that agent's token and key. Use short token expiry. |
| An agent signs or pushes as the human | The shared config resets inherited credential helpers and sets `user.useConfigOnly`. The signer refuses when no agent is found. The helper returns nothing. Remove human credentials from agent hosts. |
| `init` takes over a person's git config | `init` needs an explicit scope. It refuses `--global` or `--system` when that config holds a person's identity, signing setup or credential helpers, unless `--agent-host` is given. `--file` with `GIT_CONFIG_GLOBAL` keeps agents separate on shared machines. |
| Human commits pass unchecked | `--humans github-verified` accepts only commits GitHub marks Verified, and fails closed when the API can't be reached. |
| A commit uses a made-up agent email | `--agent-domain` fails any committer on the agent domain that isn't registered. |
| A revoked agent's email is reused | Revoked agents stay in `allowed_signers` as `# agentsign-revoked:` markers and always fail. |
| An agent stores its token where others can read it (`gh auth login` into a shared home) | The `gh` shim passes the token in `GH_TOKEN` for each call and refuses `gh auth login`/`setup-git` for agents. `doctor` warns about stored gh tokens and tokens in the environment. The shim applies the agent's repo list to gh calls, from `-R`, arguments or the checkout's remotes, and gives no token when it can't tell. |
| An agent is identified by the wrong rule | Path rules match only folders the agent's sandbox exposes. If agents can't see each other's folders, they can't claim each other. Outside those folders no agent is identified, and signing and pushes are refused. |
| A forged `Agent:` trailer | `verify` requires the trailer to match the signing agent, and fails trailers on commits no agent signed. |
| Forged author text | Only matters if CI is bypassed, so make the check required. |
| Bot PRs are merged into main | Require human review on main. |
| Secrets appear on the command line or in logs | Tokens are read only from stdin or files. No credential is printed except in `credential get` output to git. |

## Known limitations

- **An agent can read its own credentials.** agentsign runs in the agent's process space. Each credential is limited to that agent, but the agent can copy it. Phase 4 (broker mode) moves keys and tokens into a daemon outside the sandbox.
- **Agent detection trusts the environment.** Any process that can set `AGENTSIGN_AGENT` can choose an agent. However, it can only use that agent's key if it can read the agent's folder. Isolate agent folders with filesystem permissions or sandbox mounts.
- **Windows:** Unix file mode checks are skipped. Protect the store with folder ACLs.

## Reporting vulnerabilities

See [SECURITY.md](../SECURITY.md).
