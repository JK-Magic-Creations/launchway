package source

import "strings"

// skipMarkers are the words in a commit's subject that ask for no run. They
// are the ones people already write, so a commit made for another Git host
// keeps working: [skip ci], [ci skip], [no ci], [skip pipeline] and
// [skip launchway], in any case, anywhere in the subject line.
var skipMarkers = []string{"[skip ci]", "[ci skip]", "[no ci]", "[skip pipeline]", "[skip launchway]"}

// skipMarker returns the marker a commit subject carries, or "".
func skipMarker(subject string) string {
	s := strings.ToLower(subject)
	for _, m := range skipMarkers {
		if strings.Contains(s, m) {
			return m
		}
	}
	return ""
}
