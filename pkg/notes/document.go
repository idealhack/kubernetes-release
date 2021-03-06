/*
Copyright 2019 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package notes

import (
	"crypto/sha512"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pkg/errors"
)

// Document represents the underlying structure of a release notes document.
type Document struct {
	NewFeatures    []string            `json:"new_features"`
	ActionRequired []string            `json:"action_required"`
	APIChanges     []string            `json:"api_changes"`
	Duplicates     map[string][]string `json:"duplicate_notes"`
	SIGs           map[string][]string `json:"sigs"`
	BugFixes       []string            `json:"bug_fixes"`
	Uncategorized  []string            `json:"uncategorized"`
}

// CreateDocument assembles an organized document from an unorganized set of
// release notes
func CreateDocument(notes ReleaseNotes, history ReleaseNotesHistory) (*Document, error) {
	doc := &Document{
		NewFeatures:    []string{},
		ActionRequired: []string{},
		APIChanges:     []string{},
		Duplicates:     map[string][]string{},
		SIGs:           map[string][]string{},
		BugFixes:       []string{},
		Uncategorized:  []string{},
	}

	for _, pr := range history {
		note := notes[pr]

		if note.ActionRequired {
			doc.ActionRequired = append(doc.ActionRequired, note.Markdown)
		} else if note.Feature {
			doc.NewFeatures = append(doc.NewFeatures, note.Markdown)
		} else if note.Duplicate {
			header := prettifySigList(note.SIGs)
			existingNotes, ok := doc.Duplicates[header]
			if ok {
				doc.Duplicates[header] = append(existingNotes, note.Markdown)
			} else {
				doc.Duplicates[header] = []string{note.Markdown}
			}
		} else {
			categorized := false

			for _, sig := range note.SIGs {
				categorized = true
				notesForSIG, ok := doc.SIGs[sig]
				if ok {
					doc.SIGs[sig] = append(notesForSIG, note.Markdown)
				} else {
					doc.SIGs[sig] = []string{note.Markdown}
				}
			}
			isBug := false
			for _, kind := range note.Kinds {
				switch kind {
				case "bug":
					// if the PR has kind/bug, we want to make a note of it, but we don't
					// include it in the Bug Fixes section until we haven't processed all
					// kinds and determined that it has no other categorization label.
					isBug = true
				case "feature":
					continue
				case "api-change", "new-api":
					categorized = true
					doc.APIChanges = append(doc.APIChanges, note.Markdown)
				}
			}

			// if the note has not been categorized so far, we can toss in one of two
			// buckets
			if !categorized {
				if isBug {
					doc.BugFixes = append(doc.BugFixes, note.Markdown)
				} else {
					doc.Uncategorized = append(doc.Uncategorized, note.Markdown)
				}
			}
		}
	}
	return doc, nil
}

// RenderMarkdown accepts a Document and writes a version of that document to
// supplied io.Writer in markdown format.
func RenderMarkdown(w io.Writer, doc *Document, bucket, tars, prevTag, newTag string) error {
	if err := createDownloadsTable(w, bucket, tars, prevTag, newTag); err != nil {
		return err
	}

	// we always want to render the document with SIGs in alphabetical order
	sortedSIGs := []string{}
	for sig := range doc.SIGs {
		sortedSIGs = append(sortedSIGs, sig)
	}
	sort.Strings(sortedSIGs)

	// this is a helper so that we don't have to check err != nil on every write

	// first, we create a long-lived err that we can re-use
	var err error

	// write is a helper that writes a string to the in-scope io.Writer w
	write := func(s string) {
		// if write has already failed, just return and don't do anything
		if err != nil {
			return
		}
		// perform the write
		_, err = w.Write([]byte(s))
	}

	// writeNote encapsulates the pre-processing that might happen on a note text
	// before it gets bulleted and written to the io.Writer
	writeNote := func(s string) {
		if !strings.HasPrefix(s, "- ") {
			s = "- " + s
		}
		write(s + "\n")
	}

	// the "Action Required" section
	if len(doc.ActionRequired) > 0 {
		write("## Action Required\n\n")
		for _, note := range doc.ActionRequired {
			writeNote(note)
		}
		write("\n\n")
	}

	// the "New Feautres" section
	if len(doc.NewFeatures) > 0 {
		write("## New Features\n\n")
		for _, note := range doc.NewFeatures {
			writeNote(note)
		}
		write("\n\n")
	}

	// the "API Changes" section
	if len(doc.APIChanges) > 0 {
		write("### API Changes\n\n")
		for _, note := range doc.APIChanges {
			writeNote(note)
		}
		write("\n\n")
	}

	// the "Duplicate Notes" section
	if len(doc.Duplicates) > 0 {
		write("### Notes from Multiple SIGs\n\n")
		for header, notes := range doc.Duplicates {
			write(fmt.Sprintf("#### %s\n\n", header))
			for _, note := range notes {
				writeNote(note)
			}
			write("\n")
		}
		write("\n")
	}

	// each SIG gets a section (in alphabetical order)
	if len(sortedSIGs) > 0 {
		write("### Notes from Individual SIGs\n\n")
		for _, sig := range sortedSIGs {
			write("#### SIG " + prettySIG(sig) + "\n\n")
			for _, note := range doc.SIGs[sig] {
				writeNote(note)
			}
			write("\n")
		}
		write("\n\n")
	}

	// the "Bug Fixes" section
	if len(doc.BugFixes) > 0 {
		write("### Bug Fixes\n\n")
		for _, note := range doc.BugFixes {
			writeNote(note)
		}
		write("\n\n")
	}

	// we call the uncategorized notes "Other Notable Changes". ideally these
	// notes would at least have a SIG label.
	if len(doc.Uncategorized) > 0 {
		write("### Other Notable Changes\n\n")
		for _, note := range doc.Uncategorized {
			writeNote(note)
		}
		write("\n\n")
	}

	return err
}

// prettySIG takes a sig name as parsed by the `sig-foo` label and returns a
// "pretty" version of it that can be printed in documents
func prettySIG(sig string) string {
	parts := strings.Split(sig, "-")
	for i, part := range parts {
		switch part {
		case "vsphere":
			parts[i] = "vSphere"
		case "vmware":
			parts[i] = "VMWare"
		case "openstack":
			parts[i] = "OpenStack"
		case "api", "aws", "cli", "gcp":
			parts[i] = strings.ToUpper(part)
		default:
			parts[i] = strings.Title(part)
		}
	}
	return strings.Join(parts, " ")
}

func prettifySigList(sigs []string) string {
	sigList := ""

	// sort the list so that any group of SIGs with the same content gives us the
	// same result
	sort.Strings(sigs)

	for i, sig := range sigs {
		if i == 0 {
			sigList = fmt.Sprintf("SIG %s", prettySIG(sig))
		} else if i == (len(sigs) - 1) {
			sigList = fmt.Sprintf("%s, and SIG %s", sigList, prettySIG(sig))
		} else {
			sigList = fmt.Sprintf("%s, SIG %s", sigList, prettySIG(sig))
		}
	}

	return sigList
}

// createDownloadsTable creates the markdown table with the links to the tarballs.
// The function does nothing if the `tars` variable is empty.
func createDownloadsTable(w io.Writer, bucket, tars, prevTag, newTag string) error {
	// Do not add the table if not explicitly requested
	if tars == "" {
		return nil
	}
	if prevTag == "" || newTag == "" {
		return errors.New("release tags not specified")
	}

	fmt.Fprintf(w, "# %s\n\n", newTag)
	fmt.Fprintf(w, "[Documentation](https://docs.k8s.io)\n\n")

	fmt.Fprintf(w, "## Downloads for %s\n\n", newTag)

	urlPrefix := fmt.Sprintf("https://storage.googleapis.com/%s/release", bucket)
	if bucket == "kubernetes-release" {
		urlPrefix = "https://dl.k8s.io"
	}

	for _, item := range []struct {
		heading  string
		patterns []string
	}{
		{"", []string{"kubernetes.tar.gz", "kubernetes-src.tar.gz"}},
		{"Client Binaries", []string{"kubernetes-client*.tar.gz"}},
		{"Server Binaries", []string{"kubernetes-server*.tar.gz"}},
		{"Node Binaries", []string{"kubernetes-node*.tar.gz"}},
	} {
		if item.heading != "" {
			fmt.Fprintf(w, "### %s\n\n", item.heading)
		}
		fmt.Fprintln(w, "filename | sha512 hash")
		fmt.Fprintln(w, "-------- | -----------")

		for _, pattern := range item.patterns {
			pattern := filepath.Join(tars, pattern)

			matches, err := filepath.Glob(pattern)
			if err != nil {
				return err
			}

			for _, file := range matches {
				f, err := os.Open(file)
				if err != nil {
					return err
				}
				defer f.Close()

				h := sha512.New()
				if _, err := io.Copy(h, f); err != nil {
					return err
				}

				fileName := filepath.Base(file)
				fmt.Fprintf(w,
					"[%s](%s/%s/%s) | `%x`\n",
					fileName, urlPrefix, newTag, fileName, h.Sum(nil),
				)
			}
		}

		fmt.Fprintln(w, "")
	}

	fmt.Fprintf(w, "## Changelog since %s\n\n", prevTag)
	return nil
}
