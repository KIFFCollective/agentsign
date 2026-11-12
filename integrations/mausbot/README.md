# MausBot integration

This is the reference integration. Each MausBot bot runs as an agent, identified by `OMB_BOT_ID`. MausBot sets that variable at `server/agents-turn.ts:40`, and the sandbox passes it through.

## Image

Add the agentsign binary and `openssh-client` (the current image has no `ssh-keygen`), and link `git` to agentsign. See [Dockerfile.snippet](Dockerfile.snippet).

### Every `git` goes through agentsign

The image puts a symlink `/usr/local/bin/git → agentsign` ahead of `/usr/bin/git` on PATH. When agentsign runs under the name `git`:

- **With `OMB_BOT_ID` set,** it runs the real git with the bot's identity (`GIT_AUTHOR_*`, `GIT_COMMITTER_*`). On `git commit` it also adds the bot's trailer, e.g. `Agent: Pip <pip@troupe.nexus>`. Bots just run `git commit`, with no wrapper and no prompt instructions to forget.
- **With no bot identified** (an operator shell, image build steps), it runs `/usr/bin/git` unchanged.

The real git is pinned with `git = "/usr/bin/git"` in [config.toml](config.toml). Nested git calls (hooks, submodules) go straight to it.

Limits:
- **Absolute paths bypass the shim.** A tool that calls `/usr/bin/git` directly skips it. Signing and credentials still go through agentsign via the shared gitconfig; only the identity variables and trailer are missed.
- **Only `git commit` gets the trailer.** Commits made by `git merge`, `cherry-pick`, `revert` and `rebase` don't. `verify` still checks their signatures, and the `agentsign/signer` status still names the bot.

## Config

- Put [config.toml](config.toml) at `/etc/agentsign/config.toml`. Each bot's store is inside its own workspace, which other bots can't see: `/data/.openmausbot/workspaces/<bot-id>/.agentsign/`.
- Put the shared git config ([gitconfig](gitconfig)) in the sandbox's shared home, or generate it with:

  ```sh
  agentsign init --system --exe /usr/local/bin/agentsign \
    --store '/data/.openmausbot/workspaces/{agent}/.agentsign' --env OMB_BOT_ID
  ```

## Network

HTTPS through the sandbox's CONNECT proxy works as is. SSH (port 22) is not used.

## Claude Code

Turn off Claude Code's own commit attribution so the agent identity is the only one:

```json
{"attribution": {"commit": "", "pr": "", "sessionUrl": false}}
```

With the git shim, bots need no commit instructions: the trailer is added for them. (Without the shim, tell each bot to end every commit message with a blank line and the line printed by `agentsign trailer`.) To make the trailer mandatory, set `require-trailer: true` on the verify Action.
