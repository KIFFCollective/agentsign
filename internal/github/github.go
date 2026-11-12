// Package github will implement `agentsign github sync` (v0.2): uploading
// agents' signing keys (POST /user/ssh_signing_keys), adding their emails
// (POST /user/emails; verification stays a manual click) and reporting drift
// between the agent store and the machine account.
package github
