# MausBot integration

This is the reference integration. Each MausBot bot runs as an agent, identified by `OMB_BOT_ID`. MausBot sets that variable at `server/agents-turn.ts:40`, and the sandbox passes it through.

## Image

Add the agentsign binary and `openssh-client` (the current image has no `ssh-keygen`). See [Dockerfile.snippet](Dockerfile.snippet).

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

Then add this to each bot's instructions, so its commits name it on GitHub:

> End every commit message with a blank line and the line printed by `agentsign trailer`.

The `agentsign/signer` status from the verify Action shows the agent even when a bot forgets.
