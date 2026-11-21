package config

// AuditConfig is the audit stream: every audit entry also written as a JSON
// line to the server's standard output (docs/reference/audit-stream).
type AuditConfig struct {
	// Stdout turns the stream on or off; on when absent. The database record
	// is kept either way.
	Stdout *bool `yaml:"stdout"`
}

// AuditStdout says whether audit entries are also written to standard output.
func (c *Config) AuditStdout() bool { return c.Audit.Stdout == nil || *c.Audit.Stdout }
