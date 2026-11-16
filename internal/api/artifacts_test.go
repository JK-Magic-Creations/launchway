package api

import "testing"

func TestAttachmentNames(t *testing.T) {
	for in, want := range map[string]string{
		"app":                  `attachment; filename="app"; filename*=UTF-8''app`,
		"dist/report.html":     `attachment; filename="report.html"; filename*=UTF-8''report.html`,
		`a"b;c%d.txt`:          `attachment; filename="a_b_c_d.txt"; filename*=UTF-8''a%22b%3Bc%25d.txt`,
		"résumé.pdf":           `attachment; filename="r_sum_.pdf"; filename*=UTF-8''r%C3%A9sum%C3%A9.pdf`,
		"x\r\nSet-Cookie: a=b": `attachment; filename="x__Set-Cookie: a=b"; filename*=UTF-8''xSet-Cookie%3A%20a%3Db`,
		"":                     `attachment; filename="download"; filename*=UTF-8''download`,
		"..":                   `attachment; filename="download"; filename*=UTF-8''download`,
	} {
		if got := attachment(in); got != want {
			t.Errorf("attachment(%q) = %s, want %s", in, got, want)
		}
	}
}
