package importer

import (
	"regexp"
	"strings"
)

// Which commands deliver something: push an image, publish a package, release,
// deploy. Used by every reader, so a dry import can switch them off. The match
// is on the text of the commands and on the job's name; it never runs anything.
var deliverRe = regexp.MustCompile(`(?m)(^|[;&|(]\s*|\s)(docker\s+push|buildah\s+push|podman\s+push|skopeo\s+copy|helm\s+(upgrade|install|push)|kubectl\s+(apply|rollout|set\s+image|create|delete|replace|patch)|terraform\s+apply|tofu\s+apply|npm\s+publish|yarn\s+publish|twine\s+upload|cargo\s+publish|gh\s+release|git\s+push|aws\s+(s3\s+(sync|cp)|ecs\s+update|lambda\s+update|cloudformation\s+deploy)|gcloud\s+\S+(\s+\S+)*\s+deploy|ansible-playbook|argocd\s+app\s+sync|scp\s|rsync\s.*:|mvn\s.*\bdeploy\b|gradle(w)?\s.*\bpublish|flyctl\s+deploy|serverless\s+deploy|cdk\s+deploy|pulumi\s+up|az\s+\S+\s+(deploy|create))`)
var deliverName = regexp.MustCompile(`(?i)^(deploy|release|publish|promote|ship|rollout|rollback|push-image)([-_ ]|$)|([-_ ])(deploy|release|publish|prod|production)$`)

// delivers says whether a job delivers, and why in a few words.
func delivers(name, commands string, hasEnvironment bool) (bool, string) {
	if m := deliverRe.FindStringSubmatch(commands); m != nil {
		return true, "it runs " + strings.Join(strings.Fields(m[2]), " ")
	}
	if hasEnvironment {
		return true, "it targets a deployment environment"
	}
	if deliverName.MatchString(name) {
		return true, "its name says it delivers"
	}
	return false, ""
}
