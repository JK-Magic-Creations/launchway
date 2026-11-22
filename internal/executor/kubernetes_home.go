package executor

import "fmt"

// stepHomeVolume is where the pod's home volume is mounted: an emptyDir, which Kubernetes makes writable by every
// user, so it is not a home directory itself (tools such as platformdirs refuse a home that another user can
// write to).
const stepHomeVolume = "/home/launchway"

// stepHome is the home directory of every container of a restricted step pod, HOME: a directory inside the home
// volume that the step's own user makes, private to it (mode 0700, see homeSetup), so that tools that keep files or
// caches under $HOME work for a user the image does not know.
const stepHome = stepHomeVolume + "/user"

// homeSetup is the shell command that makes the step user's home directory and the cache, configuration and data
// directories under it, owned by the user running it and private to it. It runs first in the pod, as the step's own user
// (the uid is the owner of what it makes), before the workspace is uploaded.
func homeSetup(home string) string {
	return fmt.Sprintf(`umask 077 && mkdir -p %[1]s/.cache %[1]s/.config %[1]s/.local/share && chmod 0700 %[1]s %[1]s/.cache %[1]s/.config %[1]s/.local %[1]s/.local/share`, home)
}
