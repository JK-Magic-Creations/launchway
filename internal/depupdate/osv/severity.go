package osv

import (
	"math"
	"sort"
	"strings"
)

// Severity names the record's severity and gives a 0-10 score. The first
// source that answers wins: a CVSS v3 vector's base score, a score written
// as a number, then the database's own word (GitHub advisories write
// LOW, MODERATE, HIGH, CRITICAL).
func Severity(v Vuln) (string, float64) {
	for _, s := range v.Severity {
		if strings.HasPrefix(s.Score, "CVSS:3") {
			if sc, ok := cvss3(s.Score); ok {
				return word(sc), sc
			}
		}
	}
	switch strings.ToUpper(v.DBSpec.Severity) {
	case "CRITICAL":
		return "critical", 9.5
	case "HIGH":
		return "high", 8
	case "MODERATE", "MEDIUM":
		return "medium", 5.5
	case "LOW":
		return "low", 2.5
	}
	return "unknown", 0
}

func word(s float64) string {
	switch {
	case s >= 9:
		return "critical"
	case s >= 7:
		return "high"
	case s >= 4:
		return "medium"
	case s > 0:
		return "low"
	}
	return "unknown"
}

var (
	wAV  = map[string]float64{"N": 0.85, "A": 0.62, "L": 0.55, "P": 0.2}
	wAC  = map[string]float64{"L": 0.77, "H": 0.44}
	wUI  = map[string]float64{"N": 0.85, "R": 0.62}
	wCIA = map[string]float64{"H": 0.56, "L": 0.22, "N": 0}
)

// cvss3 computes the base score of a CVSS v3.x vector.
func cvss3(vec string) (float64, bool) {
	m := map[string]string{}
	for _, p := range strings.Split(vec, "/")[1:] {
		if k, v, ok := strings.Cut(p, ":"); ok {
			m[k] = v
		}
	}
	changed := m["S"] == "C"
	pr := map[string]float64{"N": 0.85, "L": 0.62, "H": 0.27}
	if changed {
		pr["L"], pr["H"] = 0.68, 0.5
	}
	av, ok1 := wAV[m["AV"]]
	ac, ok2 := wAC[m["AC"]]
	p, ok3 := pr[m["PR"]]
	ui, ok4 := wUI[m["UI"]]
	c, ok5 := wCIA[m["C"]]
	i, ok6 := wCIA[m["I"]]
	a, ok7 := wCIA[m["A"]]
	if !(ok1 && ok2 && ok3 && ok4 && ok5 && ok6 && ok7) || (m["S"] != "C" && m["S"] != "U") {
		return 0, false
	}
	iss := 1 - (1-c)*(1-i)*(1-a)
	var impact float64
	if changed {
		impact = 7.52*(iss-0.029) - 3.25*math.Pow(iss-0.02, 15)
	} else {
		impact = 6.42 * iss
	}
	expl := 8.22 * av * ac * p * ui
	if impact <= 0 {
		return 0, true
	}
	if changed {
		return roundUp(math.Min(1.08*(impact+expl), 10)), true
	}
	return roundUp(math.Min(impact+expl, 10)), true
}

func roundUp(x float64) float64 {
	n := int(math.Round(x * 100000))
	if n%10000 == 0 {
		return float64(n) / 100000
	}
	return (math.Floor(float64(n)/10000) + 1) / 10
}

func sortMatches(m []Match) {
	sort.SliceStable(m, func(i, j int) bool {
		if m[i].Score != m[j].Score {
			return m[i].Score > m[j].Score
		}
		if m[i].Dep.Name != m[j].Dep.Name {
			return m[i].Dep.Name < m[j].Dep.Name
		}
		return m[i].ID < m[j].ID
	})
}
