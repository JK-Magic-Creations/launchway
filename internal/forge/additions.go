package forge

// Addition is something a connected host offers beyond the built-in forge: its
// own auto-merge, a merge queue, required-review rules, a native checks page.
// Capabilities say what a host cannot do; additions say what it adds, so that
// a paid product is never reduced to the least every host has. Launchway shows
// each one in place, marked as coming from that host.
type Addition struct {
	// Key names it for the API ("auto_merge").
	Key string `json:"key"`
	// Title is a few words; What is one plain sentence about it.
	Title string `json:"title"`
	What  string `json:"what"`
	// Drivable says Launchway can switch it on itself (for example for a
	// write-back pull request); if false it is the host's, used on the host.
	Drivable bool `json:"drivable"`
}

// AdditionAutoMerge is the key of a host's own auto-merge of a pull request.
const AdditionAutoMerge = "auto_merge"
