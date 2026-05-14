package jwt

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var (
	ErrTokenInvalid = errors.New("token is invalid")
	ErrTokenExpired = errors.New("token is expired")
	ErrSecretWeak   = errors.New("jwt secret must be at least 32 characters")
	ErrTokenIssuer  = errors.New("token issuer mismatch")
)

// Claims 自定义 JWT 载荷
type Claims struct {
	UserID int64 `json:"user_id"`
	jwt.RegisteredClaims
}

// Generator JWT 签发器
type Generator struct {
	secret     []byte
	expiration time.Duration
	issuer     string
}

// NewGenerator creates a JWT generator with the given HMAC secret and expiration.
// issuer is optional — if empty, the "iss" claim is omitted.
func NewGenerator(secret string, expiration time.Duration, issuer string) (*Generator, error) {
	if len(secret) < 32 {
		return nil, ErrSecretWeak
	}
	return &Generator{secret: []byte(secret), expiration: expiration, issuer: issuer}, nil
}

func (g *Generator) Generate(userID int64) (string, error) {
	now := time.Now()
	claims := Claims{
		UserID: userID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(g.expiration)),
			IssuedAt:  jwt.NewNumericDate(now),
			Issuer:    g.issuer,
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(g.secret)
}

// Parser JWT 解析器
type Parser struct {
	secret []byte
	issuer string
}

// NewParser creates a JWT parser with the given HMAC secret.
// Returns an error if the secret is shorter than 32 characters.
// If issuer is non-empty, tokens with a different "iss" claim are rejected.
func NewParser(secret string, issuer string) (*Parser, error) {
	if len(secret) < 32 {
		return nil, ErrSecretWeak
	}
	return &Parser{secret: []byte(secret), issuer: issuer}, nil
}

func (p *Parser) Parse(tokenStr string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(t *jwt.Token) (interface{}, error) {
		// 防止算法混淆攻击：只允许 HMAC 签名
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return p.secret, nil
	})
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrTokenExpired
		}
		return nil, ErrTokenInvalid
	}
	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, ErrTokenInvalid
	}
	// Verify issuer if configured
	if p.issuer != "" && claims.Issuer != p.issuer {
		return nil, ErrTokenIssuer
	}
	return claims, nil
}
