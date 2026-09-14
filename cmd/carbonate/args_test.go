package main

import (
	"slices"
	"testing"
)

func TestReorderArgs(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		takesValue map[string]bool
		want       []string
	}{
		{
			name:       "already in flag-first order",
			args:       []string{"-session", "x", "alice"},
			takesValue: authFlagsWithValues,
			want:       []string{"-session", "x", "alice"},
		},
		{
			name:       "positional first",
			args:       []string{"alice", "-session", "x"},
			takesValue: authFlagsWithValues,
			want:       []string{"-session", "x", "alice"},
		},
		{
			name:       "double dash flag",
			args:       []string{"alice", "--session", "x"},
			takesValue: authFlagsWithValues,
			want:       []string{"--session", "x", "alice"},
		},
		{
			name:       "inline value does not consume the next argument",
			args:       []string{"alice", "-session=x"},
			takesValue: authFlagsWithValues,
			want:       []string{"-session=x", "alice"},
		},
		{
			name:       "boolean flag does not swallow the positional",
			args:       []string{"alice", "-h"},
			takesValue: authFlagsWithValues,
			want:       []string{"-h", "alice"},
		},
		{
			name:       "everything after -- is positional",
			args:       []string{"-session", "x", "--", "-weird-name"},
			takesValue: authFlagsWithValues,
			want:       []string{"-session", "x", "-weird-name"},
		},
		{
			name:       "value that looks like a flag is preserved",
			args:       []string{"-session", "-x", "alice"},
			takesValue: authFlagsWithValues,
			want:       []string{"-session", "-x", "alice"},
		},
		{
			name:       "multiple flags with values",
			args:       []string{"-addr", "1.2.3.4:80", "-session", "x"},
			takesValue: serveFlagsWithValues,
			want:       []string{"-addr", "1.2.3.4:80", "-session", "x"},
		},
		{
			name:       "trailing value-flag with no value",
			args:       []string{"alice", "-session"},
			takesValue: authFlagsWithValues,
			want:       []string{"-session", "alice"},
		},
		{
			name:       "no arguments",
			args:       nil,
			takesValue: authFlagsWithValues,
			want:       nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reorderArgs(tt.args, tt.takesValue)

			if !slices.Equal(got, tt.want) {
				t.Errorf("reorderArgs(%q) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

func TestIsFlag(t *testing.T) {
	tests := map[string]bool{
		"-session": true,
		"--addr":   true,
		"-h":       true,
		"alice":    false,
		"-":        false,
		"":         false,
	}

	for arg, want := range tests {
		if got := isFlag(arg); got != want {
			t.Errorf("isFlag(%q) = %v, want %v", arg, got, want)
		}
	}
}
