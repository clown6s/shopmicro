package jwt

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var (
	ErrTokenInvalid  = errors.New("token is invalid")
	ErrTokenExpired  = errors.New("token is expired")
	ErrSecretWeak    = errors.New("jwt secret must be at least 64 characters")
	ErrTokenIssuer   = errors.New("token issuer mismatch")
	ErrTokenAudience = errors.New("token audience mismatch")
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
	audience   []string
}

// NewGenerator 使用给定的 HMAC 密钥和过期时间创建 JWT 生成器。
// issuer 是可选的 — 如果为空，则省略 "iss" 声明。
// audience 是可选的 — 如果非空，则设置 "aud" 声明。
func NewGenerator(secret string, expiration time.Duration, issuer string) (*Generator, error) {
	if len(secret) < 64 {
		return nil, ErrSecretWeak
	}
	return &Generator{secret: []byte(secret), expiration: expiration, issuer: issuer}, nil
}

// WithAudience 设置 audience 声明。返回生成器本身以支持链式调用。
// 必须在 Generate() 之前调用。
func (g *Generator) WithAudience(aud ...string) *Generator {
	g.audience = aud
	return g
}

func (g *Generator) Generate(userID int64) (string, error) {
	now := time.Now()
	claims := Claims{
		UserID: userID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(g.expiration)),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			Issuer:    g.issuer,
		},
	}
	// P2-08: 如果已配置则设置 audience 声明
	if len(g.audience) > 0 {
		claims.Audience = g.audience
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(g.secret)
}

// Parser JWT 解析器
type Parser struct {
	secret   []byte
	issuer   string
	audience []string
}

// NewParser 使用给定的 HMAC 密钥创建 JWT 解析器。
// 如果密钥长度不足 64 字符则返回错误。
// 如果 issuer 非空，则具有不同 "iss" 声明的 token 会被拒绝。
func NewParser(secret string, issuer string) (*Parser, error) {
	if len(secret) < 64 {
		return nil, ErrSecretWeak
	}
	return &Parser{secret: []byte(secret), issuer: issuer}, nil
}

// WithAudience 设置期望的 audience。返回解析器本身以支持链式调用。
// 如果设置，没有匹配 "aud" 声明的 token 会被拒绝。
func (p *Parser) WithAudience(aud ...string) *Parser {
	p.audience = aud
	return p
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
	// 如果已配置则验证 issuer
	if p.issuer != "" && claims.Issuer != p.issuer {
		return nil, ErrTokenIssuer
	}
	// P2-08: 如果已配置则验证 audience
	if len(p.audience) > 0 {
		audMatch := false
		for _, expected := range p.audience {
			for _, got := range claims.Audience {
				if got == expected {
					audMatch = true
					break
				}
			}
			if audMatch {
				break
			}
		}
		if !audMatch {
			return nil, ErrTokenAudience
		}
	}
	return claims, nil
}
