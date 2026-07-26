package service

import "testing"

func TestNormalizePhone(t *testing.T) {
	valid := map[string]string{
		"13712345678":     "13712345678",
		"+8613712345678":  "13712345678",
		"8613712345678":   "13712345678",
		" 137 1234 5678 ": "13712345678",
		"137-1234-5678":   "13712345678",
		"19912345678":     "19912345678",
	}
	for input, want := range valid {
		got, err := NormalizePhone(input)
		if err != nil {
			t.Errorf("NormalizePhone(%q) unexpected error: %v", input, err)
			continue
		}
		if got != want {
			t.Errorf("NormalizePhone(%q) = %q, want %q", input, got, want)
		}
	}

	invalid := []string{"", "12345", "12712345678", "137123456789", "abc", "+15551234567"}
	for _, input := range invalid {
		if _, err := NormalizePhone(input); err == nil {
			t.Errorf("NormalizePhone(%q) expected error, got none", input)
		}
	}
}
