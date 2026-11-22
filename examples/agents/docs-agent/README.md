# Reference agent

A small script that shows the whole loop an agent walks, with no model: claim,
work on a branch, open a pull request, link it, ask for a review, or hand the
item back with a reason. Read it before writing your own; it uses only the
command line, and the same calls exist in the HTTP API and the MCP server.

Register an agent (a person who maintains the level does this), which prints its
token once:

    launchway work agents create docs --scope installation --project acme/shop \
      --label agent:docs --type task --type issue

Give it an item by labeling it `agent:docs` (or assigning it to `docs`), then run:

    LAUNCHWAY_SERVER=https://launchway.example LAUNCHWAY_TOKEN=lwa_... \
      AGENT_REPO=acme/shop ./agent.sh

The pull request waits for a person: under the default policy an agent's change
needs a person's approval, and an agent can never approve its own work.
