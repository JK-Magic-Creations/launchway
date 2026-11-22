package importer

// What the writer itself adds to the findings of a reader. These are not
// constructs of a source format, so they are not in the guide's tables by format.

var homeCacheExports = map[string]string{
	"~/.npm":            `export npm_config_cache="$PWD/.cache/npm"`,
	"~/.cache/pip":      `export PIP_CACHE_DIR="$PWD/.cache/pip"`,
	"~/go/pkg/mod":      `export GOMODCACHE="$PWD/.cache/go-mod"`,
	"~/.m2/repository":  `export MAVEN_OPTS="$MAVEN_OPTS -Dmaven.repo.local=$PWD/.cache/m2"`,
	"~/.gradle/caches":  `export GRADLE_USER_HOME="$PWD/.cache/gradle"`,
	"~/.cache/yarn":     `export YARN_CACHE_FOLDER="$PWD/.cache/yarn"`,
	"~/.cargo/registry": `export CARGO_HOME="$PWD/.cache/cargo"`,
}

// HomeCachePath says where a home-directory cache path lives once moved below the workspace.
func HomeCachePath(p string) (string, bool) {
	switch p {
	case "~/.npm":
		return ".cache/npm", true
	case "~/.cache/pip":
		return ".cache/pip", true
	case "~/go/pkg/mod":
		return ".cache/go-mod", true
	case "~/.m2/repository":
		return ".cache/m2", true
	case "~/.gradle/caches":
		return ".cache/gradle", true
	case "~/.cache/yarn":
		return ".cache/yarn", true
	case "~/.cargo/registry":
		return ".cache/cargo", true
	}
	return "", false
}

var writerTable = Table{
	{Code: "write.template", Name: "A job that is the standard job of a language", Class: Different,
		Note: "Step %s uses the built-in template launchway/%s instead of your own commands: it runs the standard commands and also records test and coverage reports",
		Next: "Compare its log with the old job's. To keep your own commands, write the step by hand (the wizard has a switch) or use the template's test_command setting"},
	{Code: "write.image.default", Name: "A job with no image", Class: Different,
		Note: "The steps %s name no container image, so they run in %s instead of a hosted runner's machine image, which has many tools installed",
		Next: "Name an image that has the tools the commands need (set image: on the step), or install them in the script"},
	{Code: "write.secret", Name: "A secret used by name", Class: Different,
		Note: "Secret %s is read from the Station's secret store %q by name, instead of from the old system's own storage; no value was copied",
		Next: "Create the secret in the store under this name (names only here; the value is never in these files)"},
	{Code: "write.dry.gated", Name: "A step that delivers, in a dry import", Class: Different,
		Note: "Step %s delivers (%s) and is switched off by the dry import: it is skipped until the input enable_delivery is true",
		Next: "Compare results with the old system first, then set the default of the input enable_delivery to true"},
	{Code: "write.image.registry", Name: "Pushing an image", Class: Different,
		Note: "Step %s pushes the image to repository %s of Launchway's built-in registry instead of the registry the old file named",
		Next: "To push elsewhere, name a registry your Stations list in the step's tags (registry:)"},
	{Code: "write.shell.bash", Name: "A script that uses bash features", Class: Different,
		Note: "Step %s runs with sh -e in Launchway but the old file used bash features, so a line may fail instead of running as before",
		Next: "Use an image that has bash and write bash -c '...' around the script, or rewrite the lines for sh"},
	{Code: "write.matrix.values", Name: "A matrix value Launchway cannot loop over", Class: Manual,
		Note: "The matrix key %s has a name or a value that Launchway's loops do not allow (letters, digits and . _ + - only), so the job is written without it",
		Next: "Rename the value, or write one step for each combination"},
	{Code: "write.needs.unknown", Name: "A dependency on a job that was not found", Class: Manual,
		Note: "The job needs %s, which is not in the files that were read (needed by %s)",
		Next: "Add the file that defines it, or remove the dependency"},
	{Code: "write.fetch.unknown", Name: "Downloading an artifact nobody keeps", Class: Manual,
		Note: "The job downloads the artifact %s, but no job it depends on keeps one by that name",
		Next: "Make the job that produces it a dependency, or add the files to the job that uses them"},
	{Code: "write.approvers.users", Name: "Approvers named by user", Class: Different,
		Note: "Step %s asks the users named in the old file, as written there; Launchway matches them against its own user names",
		Next: "Check the names are Launchway users, or use groups"},
}
