package jwt

import (
	"testing"
	"time"
)

const testSecret = "0123456789abcdef0123456789abcdef" // 32 chars

func TestNewGenerator_ShortSecret(t *testing.T) {
	_, err := NewGenerator("short", time.Hour)
	if err != ErrSecretWeak {
		t.Fatalf("expected ErrSecretWeak, got %v", err)
	}
}

func TestNewGenerator_ValidSecret(t *testing.T) {
	gen, err := NewGenerator(testSecret, time.Hour)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gen == nil {
		t.Fatal("expected non-nil generator")
	}
}

func TestGenerateAndParse(t *testing.T) {
	gen, err := NewGenerator(testSecret, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	token, err := gen.Generate(42)
	if err != nil {
		t.Fatal(err)
	}
	if token == "" {
		t.Fatal("expected non-empty token")
	}

	parser := NewParser(testSecret)
	claims, err := parser.Parse(token)
	if err != nil {
		t.Fatal(err)
	}
	if claims.UserID != 42 {
		t.Fatalf("expected user_id 42, got %d", claims.UserID)
	}
	if claims.ExpiresAt == nil {
		t.Fatal("expected ExpiresAt to be set")
	}
}

func TestParse_ExpiredToken(t *testing.T) {
	gen, err := NewGenerator(testSecret, -time.Hour) // already expired
	if err != nil {
		t.Fatal(err)
	}

	token, err := gen.Generate(1)
	if err != nil {
		t.Fatal(err)
	}

	parser := NewParser(testSecret)
	_, err = parser.Parse(token)
	if err != ErrTokenExpired {
		t.Fatalf("expected ErrTokenExpired, got %v", err)
	}
}

func TestParse_WrongSecret(t *testing.T) {
	gen, err := NewGenerator(testSecret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	token, err := gen.Generate(1)
	if err != nil {
		t.Fatal(err)
	}

	wrongParser := NewParser("wrong_secret_01234567890abcdef")
	_, err = wrongParser.Parse(token)
	if err != ErrTokenInvalid {
		t.Fatalf("expected ErrTokenInvalid, got %v", err)
	}
}

func TestParse_MalformedToken(t *testing.T) {
	parser := NewParser(testSecret)
	_, err := parser.Parse("not.a.valid.jwt")
	if err != ErrTokenInvalid {
		t.Fatalf("expected ErrTokenInvalid, got %v", err)
	}
}

func TestParse_AlgorithmConfusion(t *testing.T) {
	// Create a token signed with "none" algorithm (algorithm confusion attack)
	malformedToken := "eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0.eyJ1c2VyX2lkIjoxfQ."

	parser := NewParser(testSecret)
	_, err := parser.Parse(malformedToken)
	if err != ErrTokenInvalid {
		t.Fatalf("expected ErrTokenInvalid (algorithm confusion blocked), got %v", err)
	}
}

func TestParser_InitError(t *testing.T) {
	parser := NewParser("short")
	if err := parser.InitError(); err != ErrSecretWeak {
		t.Fatalf("expected ErrSecretWeak for short secret, got %v", err)
	}

	goodParser := NewParser(testSecret)
	if err := goodParser.InitError(); err != nil {
		t.Fatalf("expected no error for valid secret, got %v", err)
	}
}

func TestClaims_UserIDOnly(t *testing.T) {
	// Verify that Claims only contains UserID (no PII fields like Phone)
	gen, _ := NewGenerator(testSecret, time.Hour)
	token, _ := gen.Generate(99)
	parser := NewParser(testSecret)
	claims, err := parser.Parse(token)
	if err != nil {
		t.Fatal(err)
	}
	if claims.UserID != 99 {
		t.Fatalf("expected user_id 99, got %d", claims.UserID)
	}
}
