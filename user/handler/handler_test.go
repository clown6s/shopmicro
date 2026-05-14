package handler

import (
	"testing"
)

func TestEmailRegex(t *testing.T) {
	tests := []struct {
		email string
		valid bool
	}{
		{"test@example.com", true},
		{"user.name+tag@domain.co", true},
		{"a@b.cd", true},
		{"", false},
		{"invalid", false},
		{"@", false},
		{"@domain.com", false},
		{"no-at-sign.com", false},
		{"spaces in@email.com", false},
	}
	for _, tt := range tests {
		got := emailRegex.MatchString(tt.email)
		if got != tt.valid {
			t.Fatalf("emailRegex(%q) = %v, want %v", tt.email, got, tt.valid)
		}
	}
}

func TestPasswordValid(t *testing.T) {
	tests := []struct {
		pwd   string
		valid bool
	}{
		{"abc12345", true},
		{"ABC12345", true},
		{"abcDEFGH1", true},
		{"short1", false},     // too short
		{"allletters", false}, // no digit
		{"12345678", false},   // no letter
		{"abc 1234", false},   // space
		{"", false},           // empty
	}
	for _, tt := range tests {
		got := passwordValid(tt.pwd)
		if got != tt.valid {
			t.Fatalf("passwordValid(%q) = %v, want %v", tt.pwd, got, tt.valid)
		}
	}
}

func TestPhoneRegex(t *testing.T) {
	tests := []struct {
		phone string
		valid bool
	}{
		{"13800138000", true},
		{"19999999999", true},
		{"12800138000", false},  // 2 not in [3-9]
		{"1380013800", false},   // too short
		{"138001380001", false}, // too long
		{"", false},
	}
	for _, tt := range tests {
		got := phoneRegex.MatchString(tt.phone)
		if got != tt.valid {
			t.Fatalf("phoneRegex(%q) = %v, want %v", tt.phone, got, tt.valid)
		}
	}
}

func TestParseInt64(t *testing.T) {
	tests := []struct {
		input string
		want  int64
		err   bool
	}{
		{"42", 42, false},
		{"0", 0, false},
		{"9223372036854775807", 9223372036854775807, false},
		{"", 0, true},
		{"abc", 0, true},
		{"12.5", 0, true},
		{"-1", 0, true},
	}
	for _, tt := range tests {
		got, err := parseInt64(tt.input)
		if tt.err && err == nil {
			t.Fatalf("parseInt64(%q) expected error, got nil", tt.input)
		}
		if !tt.err && err != nil {
			t.Fatalf("parseInt64(%q) unexpected error: %v", tt.input, err)
		}
		if !tt.err && got != tt.want {
			t.Fatalf("parseInt64(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestMaskPhone(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"13800138000", "****8000"},
		{"123", "****"},
		{"", "****"},
	}
	for _, tt := range tests {
		got := maskPhone(tt.input)
		if got != tt.want {
			t.Fatalf("maskPhone(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
