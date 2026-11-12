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
2. **SSH and GPG keys > New SSH key:** paste the public key that `agent add` printed (`agentsign agent show dev` prints it again). Set **Key type: Signing key**. The default, *Authentication key*, is filed separately and GitHub won't use it to verify commits: they show as *Unverified (unknown_key)*.
3. **Developer settings > Fine-grained tokens > Generate new token:**

   | Setting | Value |
   |---|---|
   | Resource owner | **The owner of the repos.** For org repos that is the **organization**, not the machine account. A token owned by the machine account can't touch org repos (pushes fail with 403). It can't be changed later, so recreate the token if it's wrong. |
   | Repository access | *Only select repositories*: this agent's repos. |
   | Contents | Read and write (push commits). |
   | Pull requests | Read and write, if the agent opens PRs. |
   | Workflows | Read and write, if the agent edits files under `.github/workflows/`. |
   | Expiration | Your choice. Shorter limits the damage of a leak. |

   Organizations can require approval for fine-grained tokens. If so, approve the request under the org's **Settings > Personal access tokens > Pending requests** before the token works.

   Then store it:

   ```sh
   agentsign agent token dev < token.txt
   ```

GitHub has no API for creating fine-grained tokens, so step 3 stays manual.

Check everything as the agent:

```sh
AGENTSIGN_AGENT=dev agentsign doctor --remote https://github.com/acme/app.git
```

`doctor` asks GitHub whether the token is accepted, which account it belongs to and when it expires. It checks that the key is registered as a **signing** key, and does a dry-run push to prove the token can write. (A read check alone proves nothing on public repos, because they can be read without a token.) Use `--offline` to skip the GitHub checks.

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
- its git identity: link `git` (and `gh`) to agentsign so it is automatic (see "Shims" below), or use `eval "$(agentsign env)"` / `agentsign git ...`

Run `agentsign doctor --remote https://github.com/acme/app.git` as the agent to check the setup end to end (see step 3).

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

## How agent commits look on GitHub

GitHub links each commit to the account that owns its email. Every agent's email is on the one machine account, so GitHub shows the machine account's avatar and login for every agent. The git author name ("Pip") is stored in the commit but barely shown. agentsign adds two things so you can still see who made a commit.

### The `Agent:` trailer

Agent commits end with a trailer naming the agent:

```
Add pip.txt

Agent: Pip <pip@troupe.nexus>
```

- `agentsign git commit ...` adds it automatically.
- With the `git` shim (below), a plain `git commit` gets it automatically.
- If an agent runs plain `git commit` and writes its own messages, tell it to end every message with the output of `agentsign trailer`. For example, put this in the agent's instructions: *"End every commit message with a blank line and the line printed by `agentsign trailer`."*

On its own the trailer is just text, so `verify` checks it:

- On an agent commit, the trailer must name the agent whose key signed it, with the same name and email.
- On a commit not signed by a registered agent, an `Agent:` trailer fails. It claims agent authorship that nothing backs up.
- With `--require-trailer` (Action input `require-trailer: true`), agent commits without one fail too.

### The `agentsign/signer` commit status

The Action posts a commit status on every commit it checks. It appears in the checks list on the commit page and beside each commit in a PR:

- ✅ `Signed by Pip <pip@troupe.nexus> · key SHA256:aZ9N…`
- ❌ `signed with odin@troupe.nexus's key but committer is pip@troupe.nexus`

This needs `statuses: write` in the workflow's permissions. If the token can't post statuses, for example on a PR from a fork, the Action warns and carries on. Turn it off with `post-statuses: false`.

## Shims: plain `git` and `gh`

On agent hosts and containers, link both tools to agentsign ahead of the real ones on PATH:

```sh
ln -s /usr/local/bin/agentsign /usr/local/bin/git
ln -s /usr/local/bin/agentsign /usr/local/bin/gh
```

Agents then need nothing beyond `git` and `gh`:

- **`git`:** when an agent is identified, it runs the real git with the agent's identity, and `git commit` adds the `Agent:` trailer. Otherwise, or when the caller sets its own `GIT_AUTHOR_*`/`GIT_COMMITTER_*`, it runs git unchanged.
- **`gh`:** when an agent is identified, it runs the real GitHub CLI with the agent's token in `GH_TOKEN`, for that call only, so the token never lands in `~/.config/gh/hosts.yml`. `gh auth login`, `setup-git` and similar are refused for agents. Otherwise gh runs unchanged.

  For agents with a repo list, `gh` follows the same list as git. The shim works out which repos a call touches: `-R`/`--repo`, `$GH_REPO`, GitHub URLs and `gh repo <cmd> OWNER/REPO` in the arguments, `repos/OWNER/REPO/...` endpoints for `gh api`, otherwise the checkout's GitHub remotes (only the `gh repo set-default` one, if set). If any of them isn't on the list, the call is refused. If it can't tell (`gh api graphql`, or no checkout and no `-R`), gh runs without the token and says to pass `-R`.

The real binaries are found on PATH (skipping agentsign itself), or pinned with `git = "..."` and `gh = "..."` in the config. `agentsign doctor` warns about stored gh tokens or `GH_TOKEN`/`GITHUB_TOKEN` in the environment, which bypass agentsign.

## Squash merges

When GitHub squashes a PR, the commit on `main` is signed by GitHub, not by the agent. The agent's signatures survive only on the PR's own commits. They are checked there, before the merge. To keep per-agent signatures on `main`, use rebase or merge commits for agent PRs.
