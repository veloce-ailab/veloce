package middleware

import "testing"

// Every spelling of the same phone number must share one rate-limit bucket,
// otherwise the per-identity limit on SMS codes and phone login is bypassed by
// simply re-spelling the number.
func TestNormalizeRateLimitIdentityCollapsesPhoneSpellings(t *testing.T) {
	want := normalizeRateLimitIdentity("13800138000")
	if want == "" {
		t.Fatal("a valid phone number must produce an identity key")
	}
	for _, spelling := range []string{
		"+8613800138000",
		"8613800138000",
		"138 0013 8000",
		"138-0013-8000",
		"  +86 138-0013-8000  ",
	} {
		if got := normalizeRateLimitIdentity(spelling); got != want {
			t.Errorf("normalizeRateLimitIdentity(%q) = %q, want %q", spelling, got, want)
		}
	}
}

func TestNormalizeRateLimitIdentityKeepsDistinctPhonesApart(t *testing.T) {
	first := normalizeRateLimitIdentity("13800138000")
	second := normalizeRateLimitIdentity("13800138001")
	if first == second {
		t.Fatal("different phone numbers must not share a bucket")
	}
}

func TestNormalizeRateLimitIdentityLowercasesNonPhoneValues(t *testing.T) {
	if got := normalizeRateLimitIdentity("  User@Example.COM "); got != "user@example.com" {
		t.Fatalf("normalizeRateLimitIdentity = %q, want the trimmed lowercase email", got)
	}
	if got := normalizeRateLimitIdentity("   "); got != "" {
		t.Fatalf("normalizeRateLimitIdentity(blank) = %q, want empty", got)
	}
}
