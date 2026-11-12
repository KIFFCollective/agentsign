# MausBot integration

This is the reference integration. Each MausBot bot is an agent. Bots need nothing beyond `git` and `gh`: the image routes both through agentsign, which works out which bot is calling from the folder it runs in.

## How a bot is identified

MausBot runs each conversation in `/data/.openmausbot/task-workspaces/<bot-id>/<thread-id>/`. The sandbox binds that folder, and the bot's home `/data/.openmausbot/workspaces/<bot-id>/`, at the same paths, and each bot sees only its own. [config.toml](config.toml) has a path rule for each, so any git or gh call inside them is that bot's. A bot can't claim another bot's identity, because it can't see the other bot's folders.

`OMB_BOT_ID` is **not** used. MausBot sets it only for its own agents MCP helper process, not for the engine's shell, so agentsign never sees it.

Limits:
- **A bot working outside its folders isn't identified.** For example, a bot whose *Working Folder* setting points elsewhere, or that clones into `/tmp`. Its commits are refused with "no agent identified … run `agentsign doctor`". That's the right failure: nothing is signed or pushed under the wrong name. Keep Working Folder empty, or add a path rule for it.
- **Fix upstream:** MausBot could pass `OMB_BOT_ID` into the engine's environment (`server/drivers/claude.ts`, where that env is built; the sandbox's allow-list already lets it through). agentsign would then use it with `env = ["OMB_BOT_ID"]`, alongside the path rules.

## Image

See [Dockerfile.snippet](Dockerfile.snippet). It installs git, `openssh-client` (for `ssh-keygen`) and `gh`, installs agentsign from the release, and links `/usr/local/bin/git` and `/usr/local/bin/gh` to it. `/usr/local/bin` comes before `/usr/bin` on PATH. The real binaries are pinned in [config.toml](config.toml) (`git = "/usr/bin/git"`, `gh = "/usr/bin/gh"`).

### git

Inside a bot's folders, `git` runs with the bot's identity, and `git commit` adds `Agent: <Name> <email>`. Signing and push credentials come from the shared [gitconfig](gitconfig).

Elsewhere, git runs unchanged. It also runs unchanged when the caller sets its own `GIT_AUTHOR_*` or `GIT_COMMITTER_*`, as MausBot's checkpoint commits do.

### gh

Inside a bot's folders, `gh` runs with the bot's own token in `GH_TOKEN`, for that one call. The token is never written to disk, and it replaces any `GH_TOKEN`/`GITHUB_TOKEN` in the environment. `gh auth login`, `logout`, `refresh`, `setup-git` and `switch` are refused for bots.

The bot's repo list applies to gh as it does to git. The repo is taken from `-R`, `$GH_REPO`, URLs or `OWNER/REPO` arguments, `gh api repos/OWNER/REPO/...`, or the checkout's remotes. A repo off the list is refused. When the repo can't be worked out, gh runs without a token. That happens with `gh api graphql`, or outside a checkout with no `-R`. Bots should pass `-R OWNER/REPO` when not in a checkout.

This matters because the bots share a home. A token stored by `gh auth login` (`~/.config/gh/hosts.yml`) is readable by every bot, and gh would use it with no repo limit. `agentsign doctor` warns if it finds one, or a token in the environment.

Limits:
- **Absolute paths bypass the shims.** A tool calling `/usr/bin/git` or `/usr/bin/gh` directly skips them. For git, signing and credentials still go through agentsign; only the identity and trailer are missed.
- **Only `git commit` gets the trailer.** Merges, cherry-picks, reverts and rebases don't. `verify` still checks their signatures, and the `agentsign/signer` status still names the bot.

## Config

- [config.toml](config.toml) → `/etc/agentsign/config.toml`
- [gitconfig](gitconfig) → `/etc/gitconfig`. Make sure no `~/.gitconfig` in the shared home sets `user.name`, `user.email` or a credential helper.

Run `agentsign agent add` and `agentsign signers` as an operator with **all of `/data` mounted**, not from inside a bot's sandbox, which only sees its own home. Run `agentsign doctor` from inside a bot's folder to check that bot.

## Network

HTTPS through the sandbox's CONNECT proxy works as is. SSH (port 22) is not used.

## Claude Code

Turn off Claude Code's own commit attribution so the agent identity is the only one:

```json
{"attribution": {"commit": "", "pr": "", "sessionUrl": false}}
```

Bots need no git or gh instructions.
