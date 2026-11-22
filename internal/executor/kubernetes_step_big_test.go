package executor

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// scoped gives a step the cache and exclude hints the pipeline would give it.
func scoped(s *Spec, cache, exclude []string) {
	if len(cache) > 0 {
		s.Cache = &CacheScope{Paths: cache, Back: cache}
	}
	s.Exclude = exclude
}

// bigPool makes the trees a real pipeline leaves in its workspace, from nothing real: a Go module cache and build cache
// (declared as one cache), an npm cache (another), and the dependency directories of two web projects, in all
// about 3 GB in 150,000 files. dir is where they are made; the files are never read by anything but tar.
func bigPool(t testing.TB, dir string, scale int) map[string][2]int {
	shape := map[string][2]int{ // tree → files, bytes per file
		"go-mod":   {40000 / scale, 25000},
		"go-build": {20000 / scale, 50000},
		"npm":      {10000 / scale, 30000},
		"web-nm":   {40000 / scale, 8000},
		"docs-nm":  {30000 / scale, 8000},
	}
	var wg sync.WaitGroup
	for name, sh := range shape {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, sh[1])
			for i := range buf {
				buf[i] = byte(i * 7)
			}
			for i := 0; i < sh[0]; i++ {
				p := filepath.Join(dir, name, fmt.Sprintf("d%03d", i%400), fmt.Sprintf("f%d", i))
				if i%400 == i/400 { // a directory at a time
					os.MkdirAll(filepath.Dir(p), 0o755)
				}
				if err := os.WriteFile(p, buf, 0o644); err != nil {
					os.MkdirAll(filepath.Dir(p), 0o755)
					if err = os.WriteFile(p, buf, 0o644); err != nil {
						t.Error(err)
						return
					}
				}
				old := time.Now().Add(-48 * time.Hour)
				os.Chtimes(p, old, old)
			}
		}()
	}
	wg.Wait()
	return shape
}

func treeFiles(t testing.TB, root, under string) []PlacedFile {
	var out []PlacedFile
	filepath.WalkDir(filepath.Join(root), func(p string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			rel, _ := filepath.Rel(root, p)
			out = append(out, PlacedFile{Path: filepath.ToSlash(filepath.Join(under, rel)), Local: p, Mode: 0o644, ModTime: time.Now().Add(-48 * time.Hour).UnixNano()})
		}
		return nil
	})
	return out
}

func dirBytes(dir string) int64 {
	var n int64
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

// TestBigWorkspaceHandOver runs the shape of this repository's own pipeline (its nine script steps, each
// restoring the caches it declares, touching the directories it touches, saving its caches) on a workspace of
// about 3 GB in 150,000 files, through the fake cluster, and says what each step moved and took, and the
// Station's peak disk and memory. It needs LAUNCHWAY_BIG_DIR (where the files are made; about 15 GB are
// used) and LAUNCHWAY_BIG_NAME (a label for the output). LAUNCHWAY_BIG_SCALE divides the file counts.
func TestBigWorkspaceHandOver(t *testing.T) {
	base := os.Getenv("LAUNCHWAY_BIG_DIR")
	if base == "" {
		t.Skip("set LAUNCHWAY_BIG_DIR to measure a workspace of gigabytes")
	}
	scale, _ := strconv.Atoi(os.Getenv("LAUNCHWAY_BIG_SCALE"))
	if scale < 1 {
		scale = 1
	}
	t.Setenv("TMPDIR", base)
	pool := filepath.Join(base, "pool")
	if _, err := os.Stat(pool); err != nil {
		os.MkdirAll(pool, 0o755)
		bigPool(t, pool, scale)
	}
	c := newStepCluster(t, KubernetesOptions{})
	c.k.maxWorkspace, c.k.maxWorkspaces = 0, 0
	var up, down atomic.Int64
	inner := c.k.runIn
	c.k.runIn = func(ctx context.Context, pod, container string, cmd []string, stdin io.Reader, stdout, stderr io.Writer) error {
		joined := strings.Join(cmd, " ")
		if stdin != nil && strings.Contains(joined, "tar xf -") {
			stdin = meterReader{stdin, &up}
		}
		if stdout != nil && strings.Contains(joined, "tar -c") {
			stdout = meterWriter{stdout, &down}
		}
		return inner(ctx, pod, container, cmd, stdin, stdout, stderr)
	}

	// The checkout holds source only.
	ws := t.TempDir()
	for i := 0; i < 8000/scale; i++ {
		p := filepath.Join(ws, "src", fmt.Sprintf("p%d", i%200), fmt.Sprintf("f%d.go", i))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, make([]byte, 4096), 0o644)
		old := time.Now().Add(-time.Hour)
		os.Chtimes(p, old, old)
	}

	var peakDisk, peakHeap int64
	stop := make(chan struct{})
	var sampled sync.WaitGroup
	sampled.Add(1)
	go func() {
		defer sampled.Done()
		for {
			if n := dirBytes(c.k.workRoot()); n > atomic.LoadInt64(&peakDisk) {
				atomic.StoreInt64(&peakDisk, n)
			}
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			if int64(m.HeapSys) > atomic.LoadInt64(&peakHeap) {
				atomic.StoreInt64(&peakHeap, int64(m.HeapSys))
			}
			select {
			case <-stop:
				return
			case <-time.After(300 * time.Millisecond):
			}
		}
	}()

	goCache := []string{".cache/go-mod", ".cache/go-build"}
	npmCache := []string{".cache/npm"}
	steps := []struct {
		id     string
		cache  []string
		script string
	}{
		{"docs", npmCache, `cp -r $POOL/docs-nm docs-nm-tmp && mkdir -p docs && mv docs-nm-tmp docs/node_modules; echo x >> .cache/npm/d001/f1; mkdir -p site && echo built > site/index.html`},
		{"check-code", goCache, `echo x >> .cache/go-build/d001/f1; for i in 1 2 3; do echo n > .cache/go-build/new-$i; done`},
		{"test", goCache, `echo x >> .cache/go-build/d002/f2; for i in 1 2 3 4 5 6 7 8 9 10; do head -c 20000 /dev/zero > .cache/go-build/test-$i; done; mkdir -p reports && echo '{}' > reports/go-test.json`},
		{"security-go", goCache, `echo x >> .cache/go-build/d003/f3`},
		{"security-web", nil, `cp -r $POOL/web-nm web-nm-tmp && mkdir -p web && mv web-nm-tmp web/node_modules`},
		{"build-ui", npmCache, `[ -d web/node_modules ] || cp -r $POOL/web-nm web/node_modules; echo x >> .cache/npm/d002/f2; mkdir -p web/dist && head -c 3000000 /dev/zero > web/dist/app.js`},
		{"build-binary", goCache, `echo x >> .cache/go-build/d004/f4; mkdir -p bin && head -c 100000000 /dev/zero > bin/launchway`},
		{"gate-pipeline", nil, `ls bin/launchway >/dev/null`},
		{"image-context", nil, `ls bin/launchway web/dist >/dev/null`},
	}
	exclude := []string(nil)
	if os.Getenv("LAUNCHWAY_BIG_EXCLUDE") == "1" {
		exclude = []string{"web/node_modules", "docs/node_modules"}
	}
	treeOf := map[string]string{".cache/go-mod": "go-mod", ".cache/go-build": "go-build", ".cache/npm": "npm"}
	var totRun, totPlace, totStage time.Duration
	var totUp, totDown int64
	fmt.Printf("BIG %s: step, restore s, run s (fill + step + hand back), save-stage s, MiB into pods, MiB back\n", os.Getenv("LAUNCHWAY_BIG_NAME"))
	for _, st := range steps {
		s := stepSpec(t, ws, "run_big", st.id, "64Mi", "export POOL="+pool+"\n"+st.script)
		scoped(&s, st.cache, exclude)
		time.Sleep(2200 * time.Millisecond) // the pod's start, which the fake does not take: a real step is seconds after the one before
		t0 := time.Now()
		for _, cp := range st.cache {
			files := treeFiles(t, filepath.Join(pool, treeOf[cp]), cp)
			if err := c.k.PlaceFiles(context.Background(), s, files, func(string, string) {}); err != nil {
				t.Fatal(err)
			}
		}
		place := time.Since(t0)
		up0, down0 := up.Load(), down.Load()
		t1 := time.Now()
		var l lines
		code, err := c.k.Run(context.Background(), s, l.sink)
		if err != nil || code != 0 {
			t.Fatalf("%s: %d %v\n%s", st.id, code, err, l.text())
		}
		run := time.Since(t1)
		t2 := time.Now()
		if len(st.cache) > 0 {
			sd := t.TempDir()
			if _, err := c.k.StageFiles(context.Background(), s, st.cache, StageLimits{MaxFiles: 1000000, MaxFileBytes: 8 << 30, MaxBytes: 32 << 30, NoGit: true}.withDefaults(), sd); err != nil {
				t.Fatal(err)
			}
			os.RemoveAll(sd)
		}
		stage := time.Since(t2)
		if d, ok := interface{}(c.k).(StepScratcher); ok {
			d.DropScratch(s)
		}
		os.RemoveAll(c.dir) // the fake pod's directories go with the pods
		os.MkdirAll(c.dir, 0o755)
		mu, md := up.Load()-up0, down.Load()-down0
		fmt.Printf("BIG %s: %-13s %6.1f %6.1f %6.1f %8.0f %8.0f\n", os.Getenv("LAUNCHWAY_BIG_NAME"), st.id, place.Seconds(), run.Seconds(), stage.Seconds(), float64(mu)/(1<<20), float64(md)/(1<<20))
		totRun, totPlace, totStage, totUp, totDown = totRun+run, totPlace+place, totStage+stage, totUp+mu, totDown+md
	}
	close(stop)
	sampled.Wait()
	var status string
	if b, err := os.ReadFile("/proc/self/status"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "VmHWM") {
				status = strings.TrimSpace(line)
			}
		}
	}
	fmt.Printf("BIG %s: TOTAL restore %.1fs run %.1fs save-stage %.1fs; into pods %.0f MiB, back %.0f MiB; Station peak disk of workspaces %.2f GiB; Go heap peak %.0f MiB; %s\n",
		os.Getenv("LAUNCHWAY_BIG_NAME"), totPlace.Seconds(), totRun.Seconds(), totStage.Seconds(), float64(totUp)/(1<<20), float64(totDown)/(1<<20),
		float64(atomic.LoadInt64(&peakDisk))/(1<<30), float64(atomic.LoadInt64(&peakHeap))/(1<<20), status)
}
