package main

import (
	"errors"
	"fmt"
	"strings"
)

const repoUsage = `usage:
  agentsign agent repo list <id>
  agentsign agent repo add <id> <owner/name|owner/*>...
  agentsign agent repo remove <id> <owner/name|owner/*>...
  agentsign agent repo restrict <id> <owner/name|owner/*>...   (from all repos to just these)
  agentsign agent repo allow-all <id>`

// agentRepo manages the repos an agent's token may be sent for.
func (a *app) agentRepo(args []string) error {
	if len(args) < 2 {
		return errors.New(repoUsage)
	}
	op, id, repos := args[0], args[1], args[2:]
	s, ag, err := a.loadAgent(id)
	if err != nil {
		return err
	}
	switch op {
	case "list", "ls":
		if len(repos) > 0 {
			return errors.New(repoUsage)
		}
		if len(ag.Repos) == 0 {
			fmt.Fprintln(a.stdout, "all repos (limited by the token's own scope on GitHub)")
			return nil
		}
		for _, r := range ag.Repos {
			fmt.Fprintln(a.stdout, r)
		}
		return nil

	case "add":
		if len(repos) == 0 {
			return errors.New(repoUsage)
		}
		if ag.Revoked {
			return fmt.Errorf("agent %s is revoked", id)
		}
		if len(ag.Repos) == 0 {
			return fmt.Errorf("agent %s already allows all repos; adding %s would narrow it to just those. "+
				"To do that on purpose, run 'agentsign agent repo restrict %s %s'", id, strings.Join(repos, " "), id, strings.Join(repos, " "))
		}
		added, err := ag.AddRepos(repos...)
		if err != nil {
			return err
		}
		if len(added) == 0 {
			fmt.Fprintln(a.stdout, "Nothing to add: already listed.")
			return nil
		}
		if err := s.Save(ag); err != nil {
			return err
		}
		fmt.Fprintf(a.stdout, "Added %s to agent %s.\n", strings.Join(added, ", "), id)
		a.printTokenReminder()
		return nil

	case "restrict":
		// Like add, but allowed when the list is empty (all repos): it
		// narrows the agent from all repos to exactly these.
		if len(repos) == 0 {
			return errors.New("usage: agentsign agent repo restrict <id> <owner/name|owner/*>...")
		}
		if ag.Revoked {
			return fmt.Errorf("agent %s is revoked", id)
		}
		if _, err := ag.AddRepos(repos...); err != nil {
			return err
		}
		if err := s.Save(ag); err != nil {
			return err
		}
		fmt.Fprintf(a.stdout, "Agent %s now allows: %s\n", id, strings.Join(ag.Repos, ", "))
		a.printTokenReminder()
		return nil

	case "remove", "rm":
		if len(repos) == 0 {
			return errors.New(repoUsage)
		}
		if len(ag.Repos) == 0 {
			return fmt.Errorf("agent %s allows all repos; there is nothing to remove. "+
				"Use 'agentsign agent repo restrict %s <repo>...' to list the repos it should keep", id, id)
		}
		before := append([]string(nil), ag.Repos...)
		removed, missing, err := ag.RemoveRepos(repos...)
		if err != nil {
			return err
		}
		for _, m := range missing {
			if via := ag.MatchingRepo(m); via != "" {
				a.warnf("%s is not listed; it is allowed through %s", m, via)
			} else {
				a.warnf("%s is not listed", m)
			}
		}
		if len(removed) == 0 {
			return exitError(1)
		}
		// An empty list means "all repos", so removing the last entry would
		// widen access instead of narrowing it.
		if len(ag.Repos) == 0 {
			ag.Repos = before
			return fmt.Errorf("refusing to remove the last repo of agent %s: an empty list allows ALL repos. "+
				"Run 'agentsign agent repo allow-all %s' if that is what you want, or revoke the agent", id, id)
		}
		if err := s.Save(ag); err != nil {
			return err
		}
		fmt.Fprintf(a.stdout, "Removed %s from agent %s.\n", strings.Join(removed, ", "), id)
		a.printTokenReminder()
		return nil

	case "allow-all":
		if len(repos) > 0 {
			return errors.New(repoUsage)
		}
		if len(ag.Repos) == 0 {
			fmt.Fprintf(a.stdout, "Agent %s already allows all repos.\n", id)
			return nil
		}
		ag.Repos = nil
		if err := s.Save(ag); err != nil {
			return err
		}
		fmt.Fprintf(a.stdout, "Agent %s now allows all repos (limited by the token's own scope on GitHub).\n", id)
		return nil

	default:
		return fmt.Errorf("unknown repo command %q\n%s", op, repoUsage)
	}
}

func (a *app) printTokenReminder() {
	fmt.Fprintln(a.stdout, "Also update the token's repository access on GitHub to match: the token is the real limit.")
}
