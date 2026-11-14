package pipeline

import "strings"

// Starter returns a small pipeline for a repository that has none yet,
// named after the project. It is valid as it stands, so it can be tried as a
// draft run before it is saved.
func Starter(project string) string {
	name := project
	if !nameRe.MatchString(name) {
		name = "my-service"
	}
	return strings.ReplaceAll(starter, "{{NAME}}", name)
}

const starter = `# This repository has no pipeline yet. This is a starting point:
# change it, try it with "Run this draft", then save it.
pipeline: {{NAME}}
on:
  push: { branches: [main] }

steps:
  check:
    description: Show what was checked out
    image: docker.io/library/alpine:3.22
    run: |
      echo "Commit $LAUNCHWAY_COMMIT on $LAUNCHWAY_BRANCH"
      ls -la

  test:
    description: Run the tests
    image: docker.io/library/alpine:3.22
    needs: check
    run: echo "Replace this with your test command"
`
