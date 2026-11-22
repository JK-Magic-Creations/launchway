package importer

import (
	"strings"
)

func (h *hostedReader) jobRunsOn(j *Job, n *N) {
	// runs-on
	ro := n.Get("runs-on")
	if ro != nil {
		lbl := strings.Join(ro.Strings(), ", ")
		if g := ro.Get("labels"); g != nil {
			lbl = strings.Join(g.Strings(), ", ")
		}
		if windowsMac.MatchString(lbl) {
			h.rp.note("hosted.job.runs_on_os", n.LineOf("runs-on"), lbl)
			j.NoteLines = append(j.NoteLines, " NEEDS A PERSON: this job ran on "+oneLine(lbl, 60)+" and now runs in a Linux image")
		} else if lbl != "" && !strings.Contains(lbl, "${{") && !h.seenRunsOn[lbl] {
			h.seenRunsOn[lbl] = true
			h.rp.note("hosted.job.runs_on", n.LineOf("runs-on"), lbl)
		}
	}
}

func (h *hostedReader) jobContainer(hj *hostedJob, n *N) {
	j := hj.j
	if ct := n.Get("container"); ct != nil {
		img := ct.Str()
		if ct.IsMap() {
			img = ct.Get("image").Str()
			var skipped []string
			for _, k := range []string{"options", "volumes", "ports", "credentials"} {
				if ct.Has(k) {
					skipped = append(skipped, k)
				}
			}
			if len(skipped) > 0 {
				h.rp.note("hosted.job.container_options", ct.LineOf(skipped[0]), strings.Join(skipped, ", "))
			}
			for i, k := range ct.Get("env").Keys {
				j.Env = append(j.Env, KV{k, h.xlate(hj, ct.Get("env").Vals[i].Str(), ct.Line)})
			}
		}
		j.Image = h.xlate(hj, img, ct.Line)
		h.rp.note("hosted.job.container", n.LineOf("container"))
	}
}

func (h *hostedReader) jobServices(hj *hostedJob, n *N) {
	j := hj.j
	if sv := n.Get("services"); sv.IsMap() {
		for i, name := range sv.Keys {
			s := sv.Vals[i]
			svc := Service{Name: name, Image: h.xlate(hj, s.Get("image").Str(), s.Line)}
			if svc.Image == "" {
				continue
			}
			for k, ek := range s.Get("env").Keys {
				svc.Env = append(svc.Env, KV{ek, h.xlate(hj, s.Get("env").Vals[k].Str(), s.Line)})
			}
			if c := s.Get("command").Str(); c != "" {
				svc.Command = strings.Fields(c)
			}
			if ports := s.Get("ports").Strings(); len(ports) > 0 {
				pp := ports[0]
				if k := strings.LastIndex(pp, ":"); k >= 0 {
					pp = pp[k+1:]
				}
				svc.Port = strings.Split(pp, "/")[0]
			}
			j.Services = append(j.Services, svc)
			h.rp.note("hosted.job.services", sv.LineOf(name), name)
		}
	}
}

// jobSteps reads the job's steps and settles its delivery, image and template.
func (h *hostedReader) jobSteps(hj *hostedJob, n *N, id, env string) {
	j := hj.j
	dir := h.defDir
	steps := n.Get("steps")
	var cmds strings.Builder
	for _, s := range steps.List() {
		h.step(hj, s, dir)
	}
	for _, s := range j.Steps {
		cmds.WriteString(s.Run + "\n")
	}
	if len(j.Steps) == 0 && len(j.Artifacts)+len(j.Caches) == 0 {
		j.Steps = []Step{{Run: "true"}}
	}
	j.Delivers, j.DeliverWhy = delivers(id, cmds.String(), env != "")
	if j.Image2 != nil && j.Image2.Push {
		j.Delivers, j.DeliverWhy = true, "it pushes an image"
	}
	for _, s := range j.Steps {
		if strings.Contains(s.Run, "${{") {
			j.NoteLines = append(j.NoteLines, " NEEDS A PERSON: some ${{ }} expressions below are left as written")
			break
		}
	}
	h.rp.note("hosted.job", n.Line).Step = id
	if hj.setupIm != "" && j.Image == "" {
		j.Image = hj.setupIm
	}
	if len(j.Steps) > 0 && j.Image2 != nil {
		// build-push has its own step; remaining commands stay with the job only if they exist
		if allEmpty(j.Steps) {
			j.Steps = nil
		}
	}
	j.Image = betterJavaImage(j)
	detectTemplate(j)
}
