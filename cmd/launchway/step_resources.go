package main

import (
	"flag"
	"fmt"
	"net/url"
	"os"
)

const stepResourcesUsage = `Usage:
  launchway step-resources get [--json]               the default CPU and memory of each kind of step, and the limits
  launchway step-resources set-limits                 change the limits (administrators)
                     [--max-cpu Q] [--max-memory Q] [--memory-per-thread Q]
  launchway step-resources set-default KIND           change the default of a step kind (administrators)
                     [--cpu Q] [--cpu-limit Q] [--memory Q] [--memory-limit Q]

A flag you give sets that value ("" clears it); the others keep what they are. Quantities are
written as in a pipeline: 500m or 2 for CPU, 512Mi or 6Gi for memory. Changes need the admin
role on every project and are recorded in the audit trail.
`

type stepResourceDefault struct {
	Kind        string `json:"kind"`
	CPU         string `json:"cpu"`
	CPULimit    string `json:"cpu_limit"`
	Memory      string `json:"memory"`
	MemoryLimit string `json:"memory_limit"`
	UpdatedAt   int64  `json:"updated_at"`
	UpdatedBy   string `json:"updated_by"`
}

type stepResourceLimits struct {
	MaxCPU          string `json:"max_cpu"`
	MaxMemory       string `json:"max_memory"`
	MemoryPerThread string `json:"memory_per_thread"`
	UpdatedAt       int64  `json:"updated_at"`
	UpdatedBy       string `json:"updated_by"`
}

type stepResourcesView struct {
	Defaults []stepResourceDefault `json:"defaults"`
	Limits   stepResourceLimits    `json:"limits"`
	Kinds    []string              `json:"kinds"`
}

func cmdStepResources(args []string) error {
	return runClient(func() error {
		if len(args) == 0 {
			fmt.Fprint(os.Stderr, stepResourcesUsage)
			return cliErr{exitUsage, ""}
		}
		c, err := loadCredentials()
		if err != nil {
			return err
		}
		cl := newClient(c, os.Stdout)
		switch args[0] {
		case "get":
			return cl.stepResourcesGet(args[1:])
		case "set-limits":
			return cl.stepResourcesSetLimits(args[1:])
		case "set-default":
			return cl.stepResourcesSetDefault(args[1:])
		}
		return usageErr("unknown command step-resources %s\n\n%s", args[0], stepResourcesUsage)
	})
}

func orUnset(s string) string {
	if s == "" {
		return "not set"
	}
	return s
}

func (cl *client) stepResourcesGet(args []string) error {
	fs := flag.NewFlagSet("step-resources get", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	var v stepResourcesView
	if err := cl.call("GET", "/api/v1/step-resources", nil, &v); err != nil {
		return err
	}
	return cl.print(v, *asJSON, func() {
		for _, d := range v.Defaults {
			fmt.Fprintf(cl.out, "default for %s: cpu %s (up to %s), memory %s (up to %s)\n", d.Kind,
				orUnset(d.CPU), orUnset(d.CPULimit), orUnset(d.Memory), orUnset(d.MemoryLimit))
		}
		fmt.Fprintf(cl.out, "limits: a step may ask for at most %s CPU and %s memory; one parallel job needs %s\n",
			orUnset(v.Limits.MaxCPU), orUnset(v.Limits.MaxMemory), orUnset(v.Limits.MemoryPerThread))
	})
}

// visited reports which flags were given, so that only those are changed.
func visited(fs *flag.FlagSet) map[string]bool {
	m := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { m[f.Name] = true })
	return m
}

func (cl *client) stepResourcesSetLimits(args []string) error {
	fs := flag.NewFlagSet("step-resources set-limits", flag.ContinueOnError)
	maxCPU := fs.String("max-cpu", "", "the most CPU a step may ask for")
	maxMem := fs.String("max-memory", "", "the most memory a step may ask for")
	perThread := fs.String("memory-per-thread", "", "the memory one parallel job is assumed to need")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	given := visited(fs)
	if len(pos) != 0 || !(given["max-cpu"] || given["max-memory"] || given["memory-per-thread"]) {
		return usageErr("launchway step-resources set-limits [--max-cpu Q] [--max-memory Q] [--memory-per-thread Q]")
	}
	var cur stepResourcesView
	if err := cl.call("GET", "/api/v1/step-resources", nil, &cur); err != nil {
		return err
	}
	body := map[string]string{"max_cpu": cur.Limits.MaxCPU, "max_memory": cur.Limits.MaxMemory, "memory_per_thread": cur.Limits.MemoryPerThread}
	if given["max-cpu"] {
		body["max_cpu"] = *maxCPU
	}
	if given["max-memory"] {
		body["max_memory"] = *maxMem
	}
	if given["memory-per-thread"] {
		body["memory_per_thread"] = *perThread
	}
	var out stepResourceLimits
	if err := cl.call("PUT", "/api/v1/step-resources/limits", body, &out); err != nil {
		return err
	}
	return cl.print(out, *asJSON, func() {
		fmt.Fprintf(cl.out, "Limits are now: at most %s CPU and %s memory; one parallel job needs %s.\n",
			orUnset(out.MaxCPU), orUnset(out.MaxMemory), orUnset(out.MemoryPerThread))
	})
}

func (cl *client) stepResourcesSetDefault(args []string) error {
	fs := flag.NewFlagSet("step-resources set-default", flag.ContinueOnError)
	cpu := fs.String("cpu", "", "CPU promised")
	cpuLimit := fs.String("cpu-limit", "", "the most CPU a step may use")
	mem := fs.String("memory", "", "memory promised")
	memLimit := fs.String("memory-limit", "", "the most memory a step may use")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	given := visited(fs)
	if len(pos) != 1 || !(given["cpu"] || given["cpu-limit"] || given["memory"] || given["memory-limit"]) {
		return usageErr("launchway step-resources set-default KIND [--cpu Q] [--cpu-limit Q] [--memory Q] [--memory-limit Q]")
	}
	var cur stepResourceDefault
	if err := cl.call("GET", "/api/v1/step-resources/defaults/"+url.PathEscape(pos[0]), nil, &cur); err != nil {
		return err
	}
	body := map[string]string{"cpu": cur.CPU, "cpu_limit": cur.CPULimit, "memory": cur.Memory, "memory_limit": cur.MemoryLimit}
	for _, f := range []struct {
		flag, key, val string
	}{{"cpu", "cpu", *cpu}, {"cpu-limit", "cpu_limit", *cpuLimit}, {"memory", "memory", *mem}, {"memory-limit", "memory_limit", *memLimit}} {
		if given[f.flag] {
			body[f.key] = f.val
		}
	}
	var out stepResourceDefault
	if err := cl.call("PUT", "/api/v1/step-resources/defaults/"+url.PathEscape(pos[0]), body, &out); err != nil {
		return err
	}
	return cl.print(out, *asJSON, func() {
		fmt.Fprintf(cl.out, "Default for %s is now: cpu %s (up to %s), memory %s (up to %s).\n", out.Kind,
			orUnset(out.CPU), orUnset(out.CPULimit), orUnset(out.Memory), orUnset(out.MemoryLimit))
	})
}
