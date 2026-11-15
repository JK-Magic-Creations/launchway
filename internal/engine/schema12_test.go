package engine

// newestLinear is a pipeline that uses keys of the newest schema (12:
// reports.findings), for the compatibility tests.
const newestLinear = `
pipeline: demo
steps:
  a: {image: x, run: a, reports: {tests: "reports/*.xml", findings: "reports/*.sarif"}}
  b: {image: x, run: b, needs: a, reports: {tests: "reports/*.xml", findings: "reports/*.sarif"}}
  c: {image: x, run: c, needs: b, reports: {tests: "reports/*.xml", findings: "reports/*.sarif"}}
`
