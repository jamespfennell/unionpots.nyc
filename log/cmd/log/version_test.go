package main

import "testing"

func TestVersionText(t *testing.T) {
	commit, buildDate = "b8e67d8f2c1a9e", "2026-10-04T18:22:05Z"
	defer func() { commit, buildDate = "", "" }()
	if got := versionText(); got != "b8e67d8 · built 4 Oct 2026" {
		t.Errorf("versionText = %q", got)
	}
	buildDate = ""
	if got := versionText(); got != "b8e67d8" {
		t.Errorf("without a date: %q", got)
	}
}
