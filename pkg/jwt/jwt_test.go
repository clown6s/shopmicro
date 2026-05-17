package jwt

import (
	"testing"
	"time"
)

const testSecret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" // 64 chars
const testIssuer = "shopmicro-test"

func TestNewGenerator_ShortSecret(t *testing.T) {
	_, err := NewGenerator("short", time.Hour, testIssuer)
	if err != ErrSecretWeak {
		t.Fatalf("expected ErrSecretWeak, got %v", err)
	}
}

func TestNewGenerator_ValidSecret(t *testing.T) {
	gen, err := NewGenerator(testSecret, time.Hour, testIssuer)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gen == nil {
		t.Fatal("expected non-nil generator")
	}
}

func TestNewGenerator_NoIssuer(t *testing.T) {
	gen, err := NewGenerator(testSecret, time.Hour, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gen.issuer != "" {
		t.Fatal("expected empty issuer")
	}
}

func TestNewParser_ShortSecret(t *testing.T) {
	_, err := NewParser("short", testIssuer)
	if err != ErrSecretWeak {
		t.Fatalf("expected ErrSecretWeak, got %v", err)
	}
}

func TestNewParser_ValidSecret(t *testing.T) {
	parser, err := NewParser(testSecret, testIssuer)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parser == nil {
		t.Fatal("expected non-nil parser")
	}
}

func TestGenerateAndParse(t *testing.T) {
	gen, err := NewGenerator(testSecret, 24*time.Hour, testIssuer)
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

	parser, err := NewParser(testSecret, testIssuer)
	if err != nil {
		t.Fatal(err)
	}
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
	if claims.Issuer != testIssuer {
		t.Fatalf("expected issuer %q, got %q", testIssuer, claims.Issuer)
	}
}

func TestGenerateAndParse_NoIssuer(t *testing.T) {
	// When issuer is empty on both sides, no issuer check is performed
	gen, _ := NewGenerator(testSecret, 24*time.Hour, "")
	parser, _ := NewParser(testSecret, "")
	token, _ := gen.Generate(42)
	claims, err := parser.Parse(token)
	if err != nil {
		t.Fatal(err)
	}
	if claims.UserID != 42 {
		t.Fatalf("expected user_id 42, got %d", claims.UserID)
	}
}

func TestParse_ExpiredToken(t *testing.T) {
	gen, err := NewGenerator(testSecret, -time.Hour, testIssuer)
	if err != nil {
		t.Fatal(err)
	}

	token, err := gen.Generate(1)
	if err != nil {
		t.Fatal(err)
	}

	parser, err := NewParser(testSecret, testIssuer)
	if err != nil {
		t.Fatal(err)
	}
	_, err = parser.Parse(token)
	if err != ErrTokenExpired {
		t.Fatalf("expected ErrTokenExpired, got %v", err)
	}
}

func TestParse_WrongSecret(t *testing.T) {
	gen, err := NewGenerator(testSecret, time.Hour, testIssuer)
	if err != nil {
		t.Fatal(err)
	}

	token, err := gen.Generate(1)
	if err != nil {
		t.Fatal(err)
	}

	wrongParser, err := NewParser("wrong_secret_0123456789abcdef_0123456789abcdef_ZZYY_ccdd_eeff001", testIssuer)
	if err != nil {
		t.Fatal(err)
	}
	_, err = wrongParser.Parse(token)
	if err != ErrTokenInvalid {
		t.Fatalf("expected ErrTokenInvalid, got %v", err)
	}
}

func TestParse_MalformedToken(t *testing.T) {
	parser, err := NewParser(testSecret, testIssuer)
	if err != nil {
		t.Fatal(err)
	}
	_, err = parser.Parse("not.a.valid.jwt")
	if err != ErrTokenInvalid {
		t.Fatalf("expected ErrTokenInvalid, got %v", err)
	}
}

func TestParse_AlgorithmConfusion(t *testing.T) {
	malformedToken := "eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0.eyJ1c2VyX2lkIjoxfQ."

	parser, err := NewParser(testSecret, testIssuer)
	if err != nil {
		t.Fatal(err)
	}
	_, err = parser.Parse(malformedToken)
	if err != ErrTokenInvalid {
		t.Fatalf("expected ErrTokenInvalid (algorithm confusion blocked), got %v", err)
	}
}

func TestParse_IssuerMismatch(t *testing.T) {
	gen, _ := NewGenerator(testSecret, time.Hour, "issuer-A")
	parser, _ := NewParser(testSecret, "issuer-B")
	token, _ := gen.Generate(1)
	_, err := parser.Parse(token)
	if err != ErrTokenIssuer {
		t.Fatalf("expected ErrTokenIssuer, got %v", err)
	}
}

func TestParse_IssuerMismatch_EmptyParser(t *testing.T) {
	// Generator sets issuer, parser has no issuer configured → should be accepted
	// because parser.issuer == "" means "don't check"
	gen, _ := NewGenerator(testSecret, time.Hour, "issuer-A")
	parser, _ := NewParser(testSecret, "")
	token, _ := gen.Generate(1)
	_, err := parser.Parse(token)
	if err != nil {
		t.Fatalf("expected no error when parser issuer is empty, got %v", err)
	}
}

func TestClaims_UserIDOnly(t *testing.T) {
	gen, _ := NewGenerator(testSecret, time.Hour, testIssuer)
	token, _ := gen.Generate(99)
	parser, _ := NewParser(testSecret, testIssuer)
	claims, err := parser.Parse(token)
	if err != nil {
		t.Fatal(err)
	}
	if claims.UserID != 99 {
		t.Fatalf("expected user_id 99, got %d", claims.UserID)
	}
}
