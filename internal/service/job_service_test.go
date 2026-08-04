package service

import (
	"errors"
	"testing"
)

func TestCreateJobInputValidate(t *testing.T) {
	valid := CreateJobInput{Team: "platform", Image: "busybox:1.36", AcceleratorType: "mock-gpu", AcceleratorCount: 1}
	if err := valid.Validate(); err != nil {
		t.Errorf("expected valid input to pass, got %v", err)
	}

	cases := []struct {
		name  string
		input CreateJobInput
	}{
		{"missing team", CreateJobInput{Image: "busybox", AcceleratorType: "mock-gpu", AcceleratorCount: 1}},
		{"missing image", CreateJobInput{Team: "platform", AcceleratorType: "mock-gpu", AcceleratorCount: 1}},
		{"missing acceleratorType", CreateJobInput{Team: "platform", Image: "busybox", AcceleratorCount: 1}},
		{"zero acceleratorCount", CreateJobInput{Team: "platform", Image: "busybox", AcceleratorType: "mock-gpu"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.input.Validate()
			if !errors.Is(err, ErrValidation) {
				t.Errorf("expected ErrValidation, got %v", err)
			}
		})
	}
}

// TestParseDLQEntriesKeepsMalformedPayloads guards a real bug: a payload
// that dead-lettered *because* it failed to parse as JobSubmission must
// still show up (as Raw), not be silently dropped.
func TestParseDLQEntriesKeepsMalformedPayloads(t *testing.T) {
	payloads := [][]byte{
		[]byte(`{"id":"job-1","team":"platform","image":"busybox","acceleratorType":"mock-gpu","acceleratorCount":1}`),
		[]byte(`not valid json`),
	}

	entries := parseDLQEntries(payloads)

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Submission == nil || entries[0].Submission.ID != "job-1" {
		t.Errorf("expected first entry parsed as a submission, got %+v", entries[0])
	}
	if entries[1].Submission != nil || entries[1].Raw != "not valid json" {
		t.Errorf("expected second entry as raw fallback, got %+v", entries[1])
	}
}

func TestParseDLQEntriesEmpty(t *testing.T) {
	if entries := parseDLQEntries(nil); len(entries) != 0 {
		t.Errorf("expected no entries for empty input, got %v", entries)
	}
}
