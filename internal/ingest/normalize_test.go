package ingest

import "testing"

func TestCompanyKey(t *testing.T) {
	for in, want := range map[string]string{
		"Acme, Inc.":        "acme",
		"ACME inc":          "acme",
		"Acme Data GmbH":    "acme data",
		"Inc":               "inc", // a name that is only a suffix stays
		"  Zürich Labs Ltd": "zürich labs",
		"":                  "",
	} {
		if got := CompanyKey(in); got != want {
			t.Errorf("CompanyKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDedupKeyIgnoresCaseAndPunctuation(t *testing.T) {
	a := DedupKey("acme", "Senior Data Engineer (Remote)", "Berlin, Germany")
	b := DedupKey("acme", "senior data engineer - remote", "berlin germany")
	if a != b {
		t.Errorf("%q != %q", a, b)
	}
	if a == DedupKey("acme", "Staff Data Engineer", "Berlin, Germany") {
		t.Error("different titles must not share a key")
	}
}

func TestHTMLToText(t *testing.T) {
	in := `<div><h2>About</h2><p>We build   <b>pipelines</b>.</p><ul><li>Go</li><li>SQL &amp; dbt</li></ul>` +
		`<script>alert(1)</script><style>p{}</style><p>Remote<br>EU</p></div>`
	want := "About\nWe build pipelines.\nGo\nSQL & dbt\nRemote\nEU"
	if got := HTMLToText(in); got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
	if got := HTMLToText("<p>unclosed <b>tags"); got != "unclosed tags" {
		t.Errorf("malformed HTML: got %q", got)
	}
}

func TestContentHashChangesWithFields(t *testing.T) {
	j := NormalizedJob{Title: "Data Engineer", RemotePolicy: "unknown", EmploymentType: "unknown"}
	h1 := ContentHash(j, "Acme", "text")
	if h1 != ContentHash(j, "Acme", "text") {
		t.Fatal("hash is not stable")
	}
	j.LocationText = "Remote"
	if h1 == ContentHash(j, "Acme", "text") {
		t.Error("location change did not change the hash")
	}
}
