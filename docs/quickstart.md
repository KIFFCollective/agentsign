# Quickstart

## 1. GitHub machine account

Create one machine account (e.g. `acme-bots`) and give it access to the repos agents work on. GitHub's terms allow one machine account per person. Agents share it; they don't each get one.

## 2. Install and initialise on the agent host

```sh
agentsign init --global \
  --store /var/lib/agentsign/agents \
  --env OMB_BOT_ID \
  --path-rule '/workspaces/{agent}/'
```

This writes:

- `config.toml`: store location and agent detection rules. The default location is `~/.config/agentsign/config.toml`. Set `AGENTSIGN_CONFIG` to use another one.
- The shared git config:
  - `credential.https://github.com.helper`: an empty entry (drops inherited helpers), then agentsign.
  - `credential.https://github.com.useHttpPath = true`, so per-repo limits can be enforced.
  - `gpg.format = ssh`, `gpg.ssh.program = <agentsign>`, `user.signingKey = agentsign` (a placeholder).
  - `commit.gpgSign = true`, `tag.gpgSign = true`.
  - `user.useConfigOnly = true`: git refuses to commit unless the agent's identity is set.

Use `--dry-run` to see the changes first.

### Choosing a scope

You must pick one scope. There is no default.

| Scope | Use it when |
|---|---|
| `--global` | The host (or the user account the agents run as) is used only by agents. |
| `--system` | A dedicated agent host or image where every user is an agent. |
| `--file FILE` | Anywhere else, **including a person's own machine**. Start each agent with `GIT_CONFIG_GLOBAL=FILE`. Your own git config is untouched. |

With `--global` or `--system`, *every* git command run under that config signs and pushes as an agent, and refuses when no agent is set. Because of this, `init` checks the target config first. If it finds a person's settings, it stops and lists them:

- `user.name`, `user.email` or `user.signingKey`
- `gpg.format`, `gpg.program` or `gpg.ssh.program`
- any credential helper

Values that agentsign wrote itself don't count, so re-running `init` on an agent host works. If the host really is only for agents, re-run with `--agent-host` to go ahead anyway.

On Windows, `--system` will usually be refused, because Git for Windows sets `credential.helper = manager` system-wide.

## 3. Add an agent

```sh
agentsign agent add dev --name Dev --email dev@bots.acme.dev --repo acme/app
```

Then, signed in to GitHub as the machine account:

1. **Emails:** add `dev@bots.acme.dev` and click the verification link.
2. **SSH and GPG keys > New SSH key:** key type *Signing key*. Paste the public key that `agent add` printed (`agentsign agent show dev` prints it again).
3. **Developer settings > Fine-grained tokens:** create a token with resource owner and repository access limited to this agent's repos. Give it *Contents: read and write*, plus *Pull requests* if the agent opens PRs. Then store it:

   ```sh
   agentsign agent token dev --expires 2027-01-31 < token.txt
   ```

GitHub has no API for creating fine-grained tokens, so step 3 stays manual.

## Repo access

Each agent has a list of repos that agentsign will send its token for. When git asks for credentials, the helper compares the repo in the request with the list and returns nothing if it isn't there.

This list is a local guard. **The real limit is the token's own repository access on GitHub.** Keep the two in step: when you change one, change the other.

| You want | Do this |
|---|---|
| One repo | `agent add dev ... --repo acme/app` |
| Several repos | Repeat the flag: `--repo acme/app --repo acme/api` |
| Every repo of an owner | `--repo 'acme/*'` (quote it so the shell doesn't expand it) |
| All repos | Leave out `--repo`. The token's scope still applies. |

Entries are `owner/name` or `owner/*`. Matching ignores case and a trailing `.git`. `*/*` and partial wildcards such as `acme/app-*` are not supported.

To change the list later:

```sh
agentsign agent repo list dev                    # show the list ("all repos" if empty)
agentsign agent repo add dev acme/web 'acme/*'   # add one or more
agentsign agent repo remove dev acme/api         # remove one or more
agentsign agent repo restrict dev acme/app       # from all repos to just these
agentsign agent repo allow-all dev               # clear the list: all repos
```

An empty list means *all repos*, which leads to a few safety rules:

- `remove` refuses to remove an agent's last repo, because that would widen access. Use `allow-all` if that is what you want, or `revoke` the agent.
- `add` refuses when the agent already allows all repos, because adding would narrow access. Use `restrict` to do that on purpose.
- `remove` only removes exact entries. If you remove `acme/app` while `acme/*` is listed, you get a warning that `acme/app` is still allowed through `acme/*`.

Changes take effect on the agent's next git call. The per-repo check needs git to send the repo path. `agentsign init` turns this on with `credential.https://github.com.useHttpPath=true`. If the path is missing, the helper refuses and prints a warning.

You can also edit `repos = [...]` in the agent's `agent.toml` by hand. `agentsign agent show <id>` prints the agent's folder.

## 4. Run the agent

The agent process needs:

- the agent id: `AGENTSIGN_AGENT=dev`, a configured variable such as `OMB_BOT_ID`, or a repo under a path rule
- its git identity: `eval "$(agentsign env)"`, or use `agentsign git ...` instead of `git`

Run `agentsign doctor --remote https://github.com/acme/app.git` as the agent to check the setup end to end.

## 5. Protect the repo

1. Generate and commit `allowed_signers`:

   ```sh
   agentsign signers --out .github/allowed_signers
   ```

   Regenerate it whenever agents are added, rotated or revoked.

2. Add the verify Action (see the README) and make it a **required** status check.
3. Choose a policy for commits whose committer isn't an agent (`humans`):

   | Policy | Effect |
   |---|---|
   | `allow` (default) | Accepted; left to other checks such as review. |
   | `deny` | Only agent commits are allowed. GitHub's own merge commits fail too. |
   | `github-verified` | Accepted only if GitHub marks the commit Verified. This covers GitHub's own web-flow merge commits. It uses the GitHub API with the job's token and fails closed if the API can't be reached. |

   With `github-verified`, **also set `agent-domains`**. GitHub marks any email on the machine account as Verified. Without it, an agent email that is missing from `allowed_signers` (for example a new agent before you regenerate the file) would pass as a human. `verify` warns when it isn't set.
4. Require human review on the default branch.

## Squash merges

When GitHub squashes a PR, the commit on `main` is signed by GitHub, not by the agent. The agent's signatures survive only on the PR's own commits. They are checked there, before the merge. To keep per-agent signatures on `main`, use rebase or merge commits for agent PRs.
