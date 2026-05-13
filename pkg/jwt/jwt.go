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
}

func NewGenerator(secret string, expiration time.Duration) (*Generator, error) {
	if len(secret) < 32 {
		return nil, ErrSecretWeak
	}
	return &Generator{secret: []byte(secret), expiration: expiration}, nil
}

func (g *Generator) Generate(userID int64) (string, error) {
	now := time.Now()
	claims := Claims{
		UserID: userID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(g.expiration)),
			IssuedAt:  jwt.NewNumericDate(now),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(g.secret)
}

// Parser JWT 解析器
type Parser struct {
	secret []byte
}

func NewParser(secret string) *Parser {
	return &Parser{secret: []byte(secret)}
}

// InitError Parse validates the parser's secret length before parsing.
// Callers should check InitError after calling NewParser if strict
// startup validation is desired.
func (p *Parser) InitError() error {
	if len(p.secret) < 32 {
		return ErrSecretWeak
	}
	return nil
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
	return claims, nil
}
