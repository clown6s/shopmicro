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
	// P1-SEC-01 fix: JWT 密钥最小长度从 32 提高到 64 字符以增强安全性
	ErrSecretWeak  = errors.New("jwt secret must be at least 64 characters")
	ErrTokenIssuer = errors.New("token issuer mismatch")
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
	// P1-SEC-01 fix: 提高密钥最小长度要求到 64 字符
	if len(secret) < 64 {
		return nil, ErrSecretWeak
	}
	return &Generator{secret: []byte(secret), expiration: expiration, issuer: issuer}, nil
}

func (g *Generator) Generate(userID int64) (string, error) {
	now := time.Now()
	// P2-SEC-02 fix: 添加 NotBefore 字段防止"未来时间戳"攻击
	claims := Claims{
		UserID: userID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(g.expiration)),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
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
// Returns an error if the secret is shorter than 64 characters.
// If issuer is non-empty, tokens with a different "iss" claim are rejected.
func NewParser(secret string, issuer string) (*Parser, error) {
	// P1-SEC-01 fix: 保持与 Generator 一致的密钥长度要求
	if len(secret) < 64 {
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
