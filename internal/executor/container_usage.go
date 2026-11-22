package executor

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The container executors (Podman and Docker) start one container per step
// and remove it when the step ends, so there is nothing to read afterwards.
// With a Usage to fill, the step runs under measureScript, which takes a
// reading before the step, one every second beside it and one after, into a
// file in the step's directory that the executor reads when the container is
// gone. The container is the step's alone, so its peak is the step's.

const usageFile = "usage.samples"

// measureScript runs the step script /launchway/script.sh and records its
// use. It passes the step's exit code on, and a signal to it on to the step.
// A container without a readable cgroup runs the step unmeasured.
const measureScript = `S='` + "%SAMPLE%" + `'
D=/launchway
sh -c "$S" sh once > $D/` + usageFile + ` 2>/dev/null
sh -c "$S" sh loop >> $D/` + usageFile + ` 2>/dev/null &
sp=$!
sh -e $D/script.sh &
pid=$!
trap 'kill -TERM $pid 2>/dev/null' TERM INT
wait $pid
code=$?
if kill -0 $pid 2>/dev/null; then wait $pid; code=$?; fi
kill $sp 2>/dev/null
sh -c "$S" sh once >> $D/` + usageFile + ` 2>/dev/null
exit $code`

// measureWrapper is measureScript with the sampler in it. The sampler's
// text is passed in a single-quoted shell string, so a quote in it is
// written out.
func measureWrapper() string {
	return strings.Replace(measureScript, "%SAMPLE%", strings.ReplaceAll(sampleScript, "'", `'\''`), 1)
}

// readUsageFile turns what measureScript recorded into a Usage. The first
// reading is the container's start; the last is its end.
func readUsageFile(metaDir string, took time.Duration, u *Usage) {
	f, err := os.Open(filepath.Join(metaDir, usageFile))
	if err != nil {
		u.notMeasured("the container has no cgroup that can be read, so what the step used is not known")
		return
	}
	defer f.Close()
	var all []sample
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if s, ok := parseSample(sc.Text(), time.Now()); ok {
			all = append(all, s)
		}
	}
	if len(all) < 2 {
		u.notMeasured("the container has no cgroup that can be read, so what the step used is not known")
		return
	}
	m := &meter{}
	m.setBase(all[0])
	for _, s := range all[1 : len(all)-1] {
		m.add(s)
	}
	r := m.result(all[len(all)-1], took)
	r.ReservedMemory, r.ReservedCPUMilli = u.ReservedMemory, u.ReservedCPUMilli
	*u = r
}
