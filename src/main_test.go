package main

import (
	"strings"
	"testing"
)

func TestSplitSectionsHeadingPathAndLines(t *testing.T) {
	body := "intro line\n\n# Title\n\ntext a\n\n## Part\n\ntext b\n\n### Detail\n\ntext c\n"
	sections := splitSections(body, 4)

	expected := []section{
		{headingPath: sectionIntroPath, lineStart: 5, lineEnd: 5, body: "intro line"},
		{headingPath: "Title", lineStart: 7, lineEnd: 9, body: "# Title\n\ntext a"},
		{headingPath: "Title > Part", lineStart: 11, lineEnd: 13, body: "## Part\n\ntext b"},
		{headingPath: "Title > Part > Detail", lineStart: 15, lineEnd: 17, body: "### Detail\n\ntext c"},
	}
	if len(sections) != len(expected) {
		t.Fatalf("got %d sections, want %d: %+v", len(sections), len(expected), sections)
	}
	for position, want := range expected {
		got := sections[position]
		if got != want {
			t.Errorf("section %d = %+v, want %+v", position, got, want)
		}
	}
}

func TestSplitSectionsIgnoresHeadingsInCodeFence(t *testing.T) {
	body := "# Title\n\n```\n# not a heading\n```\n\ntail\n"
	sections := splitSections(body, 0)

	if len(sections) != 1 {
		t.Fatalf("got %d sections, want 1: %+v", len(sections), sections)
	}
	if strings.Contains(sections[0].body, "tail") == false {
		t.Errorf("fenced heading cut the section: %+v", sections[0])
	}
}

func TestSplitSectionsSkippedLevelKeepsPath(t *testing.T) {
	body := "# Title\n\ntext\n\n### Deep\n\ntext\n"
	sections := splitSections(body, 0)

	if len(sections) != 2 {
		t.Fatalf("got %d sections, want 2", len(sections))
	}
	if sections[1].headingPath != "Title > Deep" {
		t.Errorf("heading path = %q, want %q", sections[1].headingPath, "Title > Deep")
	}
}

func TestSplitSectionsCutsLongBlockAtBlankLine(t *testing.T) {
	paragraph := strings.Repeat("word ", 500)
	body := "# Title\n\n" + paragraph + "\n\n" + paragraph + "\n\n" + paragraph + "\n"
	sections := splitSections(body, 0)

	if len(sections) < 2 {
		t.Fatalf("long block was not cut: %d section(s)", len(sections))
	}
	for position, part := range sections {
		if part.headingPath != "Title" {
			t.Errorf("part %d heading path = %q", position, part.headingPath)
		}
		if len(part.body) > sectionMaxChars*sectionHardLimitFactor {
			t.Errorf("part %d is %d characters", position, len(part.body))
		}
	}
}

func TestSplitSectionsCutsLongBlockWithoutBlankLine(t *testing.T) {
	line := strings.Repeat("x", 100)
	lines := make([]string, 200)
	for position := range lines {
		lines[position] = line
	}
	body := "# Table\n" + strings.Join(lines, "\n")
	sections := splitSections(body, 0)

	if len(sections) < 2 {
		t.Fatalf("block without blank line was not cut: %d section(s)", len(sections))
	}
	for position, part := range sections {
		if len(part.body) > sectionMaxChars*sectionHardLimitFactor+len(line) {
			t.Errorf("part %d is %d characters", position, len(part.body))
		}
	}
}

func TestBodyLineOffset(t *testing.T) {
	text := "---\ntitle: x\n---\nbody\n"
	_, body := parseFrontmatter(text)

	offset := bodyLineOffset(text, body)
	if offset != 3 {
		t.Errorf("offset = %d, want 3", offset)
	}
}

func TestSplitSectionsLongerFenceContainsShorterOne(t *testing.T) {
	body := "# Title\n\n````md\n```\n# comment\n```\n````\n\ntail\n"
	sections := splitSections(body, 0)

	if len(sections) != 1 {
		t.Fatalf("got %d sections, want 1: %+v", len(sections), sections)
	}
}

func TestSplitSectionsUnclosedFenceKeepsLaterHeadings(t *testing.T) {
	body := "# Title\n\n```\ncode\n\n# Next\n\ntext\n"
	sections := splitSections(body, 0)

	if len(sections) != 2 {
		t.Fatalf("got %d sections, want 2: %+v", len(sections), sections)
	}
	if sections[1].headingPath != "Next" {
		t.Errorf("heading path = %q, want %q", sections[1].headingPath, "Next")
	}
}

func TestSplitSectionsInlineBackticksAreNotAFence(t *testing.T) {
	body := "# Title\n\n```code```\n\n# Next\n\ntext\n"
	sections := splitSections(body, 0)

	if len(sections) != 2 {
		t.Fatalf("got %d sections, want 2: %+v", len(sections), sections)
	}
}

func TestSplitSectionsKeepsHashInsideHeading(t *testing.T) {
	cases := map[string]string{
		"## C#\n":        "C#",
		"## Issue #\n":   "Issue",
		"## Closed ##\n": "Closed",
	}
	for body, want := range cases {
		sections := splitSections(body, 0)
		if len(sections) != 1 {
			t.Fatalf("%q: got %d sections", body, len(sections))
		}
		if sections[0].headingPath != want {
			t.Errorf("%q: heading path = %q, want %q", body, sections[0].headingPath, want)
		}
	}
}

func TestSplitSectionsCountsCharactersNotBytes(t *testing.T) {
	paragraph := strings.Repeat("日本語", 400)
	body := "# Title\n\n" + paragraph + "\n\n" + paragraph + "\n"
	sections := splitSections(body, 0)

	if len(sections) != 1 {
		t.Errorf("2400 characters were cut into %d sections, want 1", len(sections))
	}
}
