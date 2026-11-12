Plan: agentsign (working name), per-agent git identities on one GitHub account

1. Problem

Teams running many coding agents (10–50) on shared infrastructure want three things:
- each agent shows up as itself in git history, not as the human operator
- each agent's commits are cryptographically attributable to that agent
- each agent can be limited to certain repos and revoked on its own

Today's options are poor at fleet scale:

┌─────────────────────────────┬───────────────────────────────────────────────────────────────────────────────────┐
│          Approach           │                                      Problem                                      │
├─────────────────────────────┼───────────────────────────────────────────────────────────────────────────────────┤
│ Human's own token           │ Everything looks like the human                                                   │
├─────────────────────────────┼───────────────────────────────────────────────────────────────────────────────────┤
│ One GitHub App per agent    │ Standard and GitHub-native, but N Apps to create, install and rotate. Apps on     │
│                             │ personal accounts reportedly can't open PRs                                       │
├─────────────────────────────┼───────────────────────────────────────────────────────────────────────────────────┤
│ One machine account per     │ Against GitHub's terms (one machine account per person); a paid seat each in an   │
│ agent                       │ org                                                                               │
├─────────────────────────────┼───────────────────────────────────────────────────────────────────────────────────┤
│ One App with a custom       │ GitHub stops signing the commit when a custom author is set; the names are        │
│ author per agent            │ unverified text                                                                   │
└─────────────────────────────┴───────────────────────────────────────────────────────────────────────────────────┘

Existing tools are all App-based, young or abandoned: true-line/agent-bot-identity (https://github.com/true-line/agent-bot-identity), Argon-Sky/agent-identity-launcher (https://github.com/Argon-Sky/agent-identity-launcher) (archived), gh-app-auth (https://github.com/AmadeusITGroup/gh-app-auth).

2. Approach

One machine account, many agent identities, each with its own key, verified by CI.

- One GitHub machine account (e.g. acme-bots) has access to the repos.
- Per agent:
  - its own verified email on that account (dev@bots.acme.dev)
  - its own SSH signing key on that account (GitHub has no limit)
  - its own fine-grained token from that account, limited to that agent's repos (gives per-agent repo access and revocation)
- Each agent commits as Dev <dev@bots.acme.dev> (author and committer), signed with its own key. GitHub marks it Verified.
- What GitHub doesn't check: it only verifies "key on account + email on account", so agent A could sign with agent B's email. Closed by an allowed_signers file in the repo plus a required CI check that each commit's committer email was signed by that agent's key.
- One shared git config for every agent. The tool works out which agent is calling at runtime, from an environment variable (e.g. $OMB_BOT_ID), the repo's location, or an explicit flag. No per-agent git config files are needed, which matters in sandboxes where agents share a home folder.

What sets it apart: no token minting, no App per agent, works on personal-account repos, plain git underneath, and per-agent verification that's stronger than GitHub's own per-account check.

3. Scope

Goals (v1)
- A credential helper that returns the calling agent's own token for HTTPS push and pull.
- A signing wrapper that signs with the calling agent's own SSH key.
- Commit identity (author and committer) set per agent without per-agent git config.
- A CLI to create, list, rotate and revoke agents; print public keys and emails for upload; and generate allowed_signers.
- A GitHub Action that verifies PR commits against allowed_signers.
- A documented security model, and works in containers and sandboxes (no keyring, no TTY, HTTPS-only networks).

Non-goals (v1)
- Hiding an agent's own credentials from that agent. The tool runs in the agent's own process space, and each credential is limited to that agent. Phase 4 covers keeping keys out of the agent's reach entirely.
- GitHub App support (the tools above cover it).
- Creating fine-grained tokens automatically. GitHub has no API for it, so it stays a documented manual step.

4. Architecture

                 shared ~/.gitconfig (same for every agent)
                 ├─ credential.https://github.com.helper = agentsign credential
                 ├─ gpg.format = ssh, commit.gpgsign = true
                 ├─ gpg.ssh.program = agentsign sign
                 └─ user.signingKey = agentsign            (placeholder; the wrapper resolves it)

 agent process ──► git ──► agentsign ──► work out the agent:
                                         1. --agent flag
                                         2. env var (configurable list, e.g. AGENTSIGN_AGENT, OMB_BOT_ID)
                                         3. path rule (repo under /workspaces/<id>/)
                              │
                              └─► agent store: <root>/<agent-id>/
                                     agent.toml      name, email, allowed hosts and repos
                                     signing_key     ed25519, 0600
                                     token           fine-grained token, 0600 (or a pointer to an outside store)

Parts, all in one static binary:

Command: agentsign credential get|store|erase
What it does: Git credential helper (https://git-scm.com/docs/api-credentials). Works out the agent, checks host and
  path against agent.toml, and prints username and password. Ignores store and erase. Says nothing (exit 0) for hosts
  it doesn't handle or when no agent is found, so other helpers still work. Supports password_expiry_utc where git is
  new enough.
────────────────────────────────────────
Command: agentsign sign …
What it does: Drop-in for git's gpg.ssh.program. For signing (-Y sign), it swaps in the agent's key. For verify and
  find-principals calls, it passes straight through to ssh-keygen. Refuses to sign if no agent is found, so nothing is
  ever signed by accident.
────────────────────────────────────────
Command: agentsign git … / agentsign env
What it does: Identity: env prints GIT_AUTHOR_* and GIT_COMMITTER_* for the agent. git is an optional wrapper that
  exports them and runs the real git. Platforms can instead set the variables at agent start.
────────────────────────────────────────
Command: agentsign init
What it does: Creates the agent store and the shared git config (--system, --global or --file).
────────────────────────────────────────
Command: agentsign agent add <id> --name --email [--repo owner/name…]
What it does: Generates the signing key and writes agent.toml. Prints the public key and email to add to GitHub.
────────────────────────────────────────
Command: agentsign agent token <id>
What it does: Stores the agent's token (read from stdin, never from command-line arguments).
────────────────────────────────────────
Command: agentsign agent list|show|rotate|revoke
What it does: Day-to-day management. revoke deletes local material and prints the GitHub steps.
────────────────────────────────────────
Command: agentsign signers [--out .github/allowed_signers]
What it does: Generates allowed_signers (<email> namespaces="git" <pubkey>) for all active agents.
────────────────────────────────────────
Command: agentsign github sync (optional)
What it does: Uses an admin token from the machine account to upload signing keys (POST /user/ssh_signing_keys), add
  emails (POST /user/emails; the verification click stays manual) and list drift.
────────────────────────────────────────
Command: agentsign doctor
What it does: Checks: ssh-keygen present, git version, file permissions, agent found, token reaches the repo, test
  signature, allowed_signers up to date.

GitHub Action (agentsign/verify-action):
- For each commit in the PR range:
  a. If the committer email belongs to a registered agent: run git verify-commit --raw with allowed_signers, require a valid signature, and require the signer to equal the committer email. Git doesn't enforce that match itself, so the Action does.
  b. Otherwise apply a policy for humans, e.g. accept GitHub's own commit.verification.verified (covers web-flow merges) or require an allow-list.
- Optional: require author == committer for agent commits.
- Output: a summary table (commit, agent, key fingerprint, result) and a failing check status. Meant to be a required check.

Language: Go. One static binary for Linux, macOS and Windows; no runtime dependencies; common for git credential helpers. The Action reuses the same verification code.

5. Security model

┌────────────────────────┬────────────────────────────────────────────────────────────────────────────────────────┐
│         Threat         │                                       Mitigation                                       │
├────────────────────────┼────────────────────────────────────────────────────────────────────────────────────────┤
│ Agent A signs as agent │ Keys stored per agent; in a sandbox, A can't read B's folder. CI checks the signer     │
│  B                     │ matches the committer email.                                                           │
├────────────────────────┼────────────────────────────────────────────────────────────────────────────────────────┤
│ An agent's key or      │ The token only reaches that agent's repos. Revoke only that agent's token and key.     │
│ token leaks            │ Short token expiry recommended.                                                        │
├────────────────────────┼────────────────────────────────────────────────────────────────────────────────────────┤
│ Agent signs or pushes  │ The shared config never falls back to the human. The signer refuses with no agent      │
│ as the human           │ found; the helper returns nothing. Docs: remove human credentials from agent hosts.    │
├────────────────────────┼────────────────────────────────────────────────────────────────────────────────────────┤
│ Forged author text     │ Only matters if CI is bypassed; branch protection makes the check required.            │
├────────────────────────┼────────────────────────────────────────────────────────────────────────────────────────┤
│ Merging bot PRs into   │ Documented: require human review on main.                                              │
│ main                   │                                                                                        │
├────────────────────────┼────────────────────────────────────────────────────────────────────────────────────────┤
│ Secrets on the command │ Tokens only via stdin or files. No credential is ever printed except in credential get │
│  line or in logs       │  output to git.                                                                        │
└────────────────────────┴────────────────────────────────────────────────────────────────────────────────────────┘

Documented limitation: an agent can read its own credentials. Planned (phase 4): a broker mode. A small daemon outside the sandbox holds keys and tokens; the helper and signer reach it over a unix socket bound into the sandbox, with the agent's identity given by the platform rather than claimed by the agent. Signing can use user.signingKey = key::<pubkey> with an ssh-agent socket, so private keys never enter the sandbox.

6. Integrations

- Generic: any setup where agents run as processes with a distinguishing environment variable or folder (Claude Code, Codex, devcontainers, CI runners).
- MausBot (first reference integration):
  - Image: add the agentsign binary and openssh-client (the current image has no ssh-keygen).
  - Shared config in the sandbox's shared home. Agent found via OMB_BOT_ID, which MausBot sets at server/agents-turn.ts:40 and the sandbox lets through.
  - Agent store: /data/.openmausbot/workspaces/<bot-id>/.agentsign/, which other bots can't see.
  - Network: HTTPS through the sandbox's CONNECT proxy works as-is; SSH (port 22) is avoided.
  - Turn off Claude Code's own attribution: {"attribution":{"commit":"","pr":"","sessionUrl":false}}.
  - Optional upstream proposal: MausBot's credential broker injects GitHub tokens server-side, making the phase 4 broker native.
- Docs recipes: Docker and Compose, devcontainer, a plain Linux host, and a GitHub org and branch-protection setup.

7. Repo layout

agentsign/
├─ cmd/agentsign/            main
├─ internal/resolve/         working out which agent is calling (flag, env, path)
├─ internal/store/           agent.toml, key and token files, permission checks
├─ internal/credential/      git credential protocol (parse and emit, expiry)
├─ internal/sign/            ssh-keygen wrapper (sign swaps the key; verify passes through)
├─ internal/signers/         allowed_signers generation and parsing
├─ internal/verify/          commit verification (shared with the Action)
├─ internal/github/          optional sync (signing keys, emails)
├─ action/                   verify-action (composite or Docker action using the binary)
├─ integrations/mausbot/     Dockerfile snippet, gitconfig, docs
├─ docs/                     quickstart, security model, recipes, FAQ (ToS, Apps compared)
└─ test/e2e/                 real-git tests; optional live GitHub tests

8. Testing

- Unit: credential protocol parsing (blank-line end, unknown keys, url= vs host and path), agent detection order, allowed_signers round trip, refusal paths.
- Integration (no network): real git and ssh-keygen in temp folders. Commit as agents A and B, check git verify-commit and principal matching, and confirm a commit signed by A with B's email fails. Run the credential helper against a local HTTPS git server (e.g. git http-backend behind a TLS test server) to confirm per-agent tokens are sent.
- Sandbox test: run under bubblewrap with a shared home and per-agent folders, mirroring MausBot.
- Live e2e (optional, gated on secrets): a test org repo. Push signed commits as two agents, check the GitHub API shows verified: true, and run the Action on a PR containing a forged commit.
- CI matrix: Linux, macOS, Windows; git 2.34+ (SSH signing needs 2.34).

9. Milestones

┌───────────┬─────────────────────────────────────────────────────────────────────────────────────────────────────┐
│   Phase   │                                             Deliverable                                             │
├───────────┼─────────────────────────────────────────────────────────────────────────────────────────────────────┤
│ 0: Spike  │ Shell prototype: credential helper + signer + allowed_signers. Shows a "Verified" commit for two    │
│ (days)    │ agents on one account, and a forged commit caught locally.                                          │
├───────────┼─────────────────────────────────────────────────────────────────────────────────────────────────────┤
│ 1: MVP    │ Go binary: init, agent add/token/list/show, credential, sign, env, signers, doctor. Verify Action.  │
│ v0.1      │ Quickstart and security docs. Releases with checksums.                                              │
├───────────┼─────────────────────────────────────────────────────────────────────────────────────────────────────┤
│ 2: v0.2   │ github sync (signing keys and emails), rotate and revoke flows, the git wrapper, Windows support, a │
│           │  MausBot integration guide and image snippet.                                                       │
├───────────┼─────────────────────────────────────────────────────────────────────────────────────────────────────┤
│           │ Other backends for the agent store (secret files mounted by platforms, 1Password, env), a           │
│ 3: v0.3   │ multi-account option (one machine account per org), Gitea and GitLab (SSH signing works the same;   │
│           │ check their email rules).                                                                           │
├───────────┼─────────────────────────────────────────────────────────────────────────────────────────────────────┤
│ 4: v1.0   │ Broker mode (daemon + unix socket + ssh-agent signing; keys never inside the sandbox). Stable       │
│           │ config format. Security review.                                                                     │
└───────────┴─────────────────────────────────────────────────────────────────────────────────────────────────────┘

10. Open-source setup

- License: Apache-2.0, which includes a patent grant and suits a security tool. Peers use MIT, which is also fine.
- Governance: SECURITY.md with private disclosure, CONTRIBUTING.md with DCO sign-off, CODEOWNERS on internal/sign and internal/verify.
- Releases: goreleaser; signed checksums; SLSA provenance via GitHub Actions; reproducible builds; Homebrew tap and .deb later.
- The project eats its own cooking: its own repo requires the verify Action, and release automation commits as an agentsign-managed agent.
- Docs: a "Why not GitHub Apps?" comparison page, a GitHub terms note (one machine account per person), and a threat-model page.

11. Open questions

1. Name: agentsign is a placeholder; check package and repo name availability.
2. Emails: recommend a dedicated subdomain with email routing, or support plus-addressing? Check that GitHub accepts plus-addresses as separate verified emails.
3. Fine-grained token expiry: the maximum depends on org policy. Should doctor warn about expiring tokens using a stored expiry date?
4. Does GitHub's Verified popup name the specific signing key clearly enough for humans, or should the Action post a PR comment mapping commits to agents?
5. Squash merges: the merge commit on main is GitHub-signed, and per-agent signatures stay only on the PR's commits. Document this, or recommend rebase/merge commits for agent PRs?
6. Agent detection from env vars: should the tool trust any variable name it's configured with, or require platforms to set an AGENTSIGN_AGENT variable it controls?