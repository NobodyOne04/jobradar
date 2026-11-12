package ingest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

var (
	nonAlnum = regexp.MustCompile(`[^\p{L}\p{N}]+`) // any letter or digit, not only ASCII
	// Legal-form suffixes dropped from company names, so "Acme, Inc." and "Acme" match.
	legalSuffixes = map[string]bool{
		"inc": true, "llc": true, "ltd": true, "limited": true, "gmbh": true, "ag": true,
		"bv": true, "sa": true, "sas": true, "srl": true, "plc": true, "corp": true,
		"corporation": true, "co": true, "oy": true, "ab": true, "as": true, "pty": true,
	}
)

// normalizeWords lowercases and keeps letters and digits (any script) as separate words.
func normalizeWords(s string) []string {
	return strings.Fields(nonAlnum.ReplaceAllString(strings.ToLower(s), " "))
}

// CompanyKey is the company name normalized for matching across sources.
func CompanyKey(name string) string {
	words := normalizeWords(name)
	for len(words) > 1 && legalSuffixes[words[len(words)-1]] {
		words = words[:len(words)-1]
	}
	return strings.Join(words, " ")
}

// DedupKey identifies the same job across sources: company + title + location.
// Deliberately strict: a missed duplicate costs less than a wrong merge (ADR 0005).
func DedupKey(companyKey, title, location string) string {
	return companyKey + "|" + strings.Join(normalizeWords(title), " ") + "|" +
		strings.Join(normalizeWords(location), " ")
}

// ContentHash changes whenever a field the matcher reads changes.
func ContentHash(j NormalizedJob, companyName, descriptionText string) string {
	b, _ := json.Marshal(struct {
		NormalizedJob
		Company, Text string
	}{j, companyName, descriptionText})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// PayloadHash detects unchanged postings without normalizing them again.
func PayloadHash(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// blockTags end a line of text: paragraphs, list items and headings stay separate.
var blockTags = map[string]bool{
	"p": true, "div": true, "br": true, "li": true, "ul": true, "ol": true, "tr": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"section": true, "article": true, "table": true, "blockquote": true,
}

// HTMLToText turns a job description's HTML into plain text, one block per line.
func HTMLToText(s string) string {
	z := html.NewTokenizer(strings.NewReader(s))
	var b strings.Builder
	skip := 0 // inside <script> or <style>
	for {
		switch z.Next() {
		case html.ErrorToken: // io.EOF: tokenizing never fails on malformed HTML
			return tidyLines(b.String())
		case html.TextToken:
			if skip == 0 {
				b.Write(z.Text())
			}
		case html.StartTagToken, html.SelfClosingTagToken, html.EndTagToken:
			name, _ := z.TagName()
			tag := string(name)
			if tag == "script" || tag == "style" {
				if z.Token().Type == html.StartTagToken {
					skip++
				} else if skip > 0 {
					skip--
				}
			}
			if blockTags[tag] {
				b.WriteByte('\n')
			}
		}
	}
}

func tidyLines(s string) string {
	var out []string
	for line := range strings.SplitSeq(s, "\n") {
		if line = strings.Join(strings.Fields(line), " "); line != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}
