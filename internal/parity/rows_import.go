package parity

// The row of the pipeline importer, in a file of its own.

func init() {
	rows = append(rows,
		Row{Route: "POST /api/v1/import/pipeline", MCP: "import_pipeline", CLI: "import pipeline", Terraform: "not needed by terraform", Status: Equal},
	)
}
