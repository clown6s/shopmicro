package handler

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/bytedance/gopkg/cloud/metainfo"
	"github.com/go-sql-driver/mysql"

	v1 "shopmicro/user/kitex_gen/api/user/v1"
	"shopmicro/user/module"

	"shopmicro/pkg/errcode"
	"shopmicro/pkg/jwt"
	"shopmicro/user/conf"
)

// ===========================
// Test helpers
// ===========================

const (
	testJWTSecret     = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabc" // 65 chars >= 64
	testJWTIssuer     = "test-issuer"
	testRefreshSecret = "zyxwvutsrqponmlkjihgfedcbaZYXWVUTSRQPONMLKJIHGFEDCBA9876543210zyx" // different from JWT secret
)

func newTestHandler(store UserStore, rdb RedisOps) *UserServiceImpl {
	jwtExpHrs := 24
	gen, _ := jwt.NewGenerator(testJWTSecret, time.Duration(jwtExpHrs)*time.Hour, testJWTIssuer)
	rtExpHrs := jwtExpHrs * 7
	rtGen, _ := jwt.NewGenerator(testRefreshSecret, time.Duration(rtExpHrs)*time.Hour, testJWTIssuer)
	return &UserServiceImpl{
		userDal:   store,
		jwtGen:    gen,
		jwtExpHrs: jwtExpHrs,
		rtGen:     rtGen,
		rtExpHrs:  rtExpHrs,
		rdb:       rdb,
		sec: conf.Security{
			PasswordMinLen: 8, PasswordMaxLen: 128, PasswordBcryptCost: 12,
			RegisterLimitMax: 5, RegisterLimitWindow: 3600,
			RefreshLimitMax: 10, RefreshLimitWindow: 60,
			LoginMaxFailures: 10, LoginLockoutSec: 900,
		},
	}
}

func errCode(err error) uint32 {
	var biz *errcode.BizError
	if errors.As(err, &biz) {
		return biz.Code
	}
	return 0
}

// hashPassword bcrypt hashes a password with the same SHA-256 pre-hash used by handler.
func hashPassword(pwd string) string {
	preHash := sha256.Sum256([]byte(pwd))
	h, _ := bcrypt.GenerateFromPassword(preHash[:], 12)
	return string(h)
}

// ===========================
// Mock implementations
// ===========================

// testUserStore is a controllable mock implementing UserStore.
type testUserStore struct {
	mu        sync.Mutex
	users     map[int64]*module.User
	phoneIdx  map[string]int64 // phone -> user ID
	nextID    int64
	createErr error
	getErr    error
	updateErr error
	deleteErr error
	listErr   error
}

func newTestUserStore() *testUserStore {
	return &testUserStore{
		users:    make(map[int64]*module.User),
		phoneIdx: make(map[string]int64),
		nextID:   1,
	}
}

func (s *testUserStore) Create(_ context.Context, user *module.User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.createErr != nil {
		return s.createErr
	}
	if _, exists := s.phoneIdx[user.Phone]; exists {
		return &mysql.MySQLError{Number: 1062}
	}
	user.ID = s.nextID
	s.nextID++
	s.users[user.ID] = user
	s.phoneIdx[user.Phone] = user.ID
	return nil
}

func (s *testUserStore) GetByID(_ context.Context, id int64) (*module.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getErr != nil {
		return nil, s.getErr
	}
	u, ok := s.users[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return u, nil
}

func (s *testUserStore) GetByPhone(_ context.Context, phone string) (*module.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getErr != nil {
		return nil, s.getErr
	}
	id, ok := s.phoneIdx[phone]
	if !ok {
		return nil, sql.ErrNoRows
	}
	u, ok := s.users[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return u, nil
}

func (s *testUserStore) Update(_ context.Context, id int64, updates map[string]interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.updateErr != nil {
		return s.updateErr
	}
	u, ok := s.users[id]
	if !ok {
		return sql.ErrNoRows
	}
	if v, ok := updates["real_name"]; ok {
		u.RealName = v.(string)
	}
	if v, ok := updates["phone"]; ok {
		delete(s.phoneIdx, u.Phone)
		u.Phone = v.(string)
		s.phoneIdx[u.Phone] = id
	}
	return nil
}

func (s *testUserStore) Delete(_ context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deleteErr != nil {
		return s.deleteErr
	}
	u, ok := s.users[id]
	if !ok {
		return sql.ErrNoRows
	}
	delete(s.users, id)
	delete(s.phoneIdx, u.Phone)
	return nil
}

func (s *testUserStore) List(_ context.Context, page, pageSize int32, name, phone string) ([]*module.User, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listErr != nil {
		return nil, 0, s.listErr
	}
	var result []*module.User
	for _, u := range s.users {
		result = append(result, u)
	}
	return result, int64(len(result)), nil
}

// testRedisOps is a controllable mock implementing RedisOps.
type testRedisOps struct {
	mu            sync.Mutex
	data          map[string]string
	sets          map[string]map[string]bool
	errs          map[string]error // key -> error to return
	scriptResults map[string]interface{}
}

func newTestRedisOps() *testRedisOps {
	return &testRedisOps{
		data:          make(map[string]string),
		sets:          make(map[string]map[string]bool),
		errs:          make(map[string]error),
		scriptResults: make(map[string]interface{}),
	}
}

func (r *testRedisOps) Get(_ context.Context, key string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err, ok := r.errs[key]; ok {
		return "", err
	}
	return r.data[key], nil
}

func (r *testRedisOps) Del(_ context.Context, keys ...string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, k := range keys {
		delete(r.data, k)
		delete(r.sets, k)
	}
	return nil
}

func (r *testRedisOps) Set(_ context.Context, key string, value interface{}, _ time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data[key] = toString(value)
	return nil
}

func (r *testRedisOps) SAddExpire(_ context.Context, key string, _ time.Duration, members ...interface{}) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sets[key] == nil {
		r.sets[key] = make(map[string]bool)
	}
	for _, m := range members {
		r.sets[key][toString(m)] = true
	}
	return nil
}

func (r *testRedisOps) SRem(_ context.Context, key string, members ...interface{}) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.sets[key]; ok {
		for _, m := range members {
			delete(s, toString(m))
		}
	}
	return nil
}

func (r *testRedisOps) GetDel(_ context.Context, key string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	val := r.data[key]
	delete(r.data, key)
	return val, nil
}

func (r *testRedisOps) RunScript(_ context.Context, src string, keys []string, args ...interface{}) (interface{}, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// For rate limit scripts, return 0 (not limited)
	if src == rateLimitScript || src == loginFailIncrScript {
		return int64(0), nil
	}
	// For revoke token scripts, return 1 (success)
	if src == revokeTokensScript {
		return int64(1), nil
	}
	// For refresh token script, simulate successful rotation
	if src == refreshTokensScript {
		// Simulate: old RT exists in Redis → GETDEL succeeds → return "ok:AT:RT"
		// Check if the old RT key has a value in mock data
		rtKey := ""
		if len(keys) >= 3 {
			rtKey = keys[2]
		}
		if rtKey != "" {
			if _, exists := r.data[rtKey]; exists {
				// Simulate GETDEL: delete old RT and return success
				delete(r.data, rtKey)
				// Return fake AT:RT pair
				return "ok:fakeAccessToken:fakeRefreshToken", nil
			}
			// RT key not found in mock data → GETDEL returns nil → simulate RT already consumed
			return nil, nil
		}
		// No rtKey provided, return nil (RT not found)
		return nil, nil
	}
	// Check for script-specific error overrides
	if err, ok := r.errs[src]; ok {
		return nil, err
	}
	return int64(0), nil
}

func toString(v interface{}) string {
	if v == nil {
		return ""
	}
	switch val := v.(type) {
	case string:
		return val
	case int64:
		return fmt.Sprintf("%d", val)
	default:
		return fmt.Sprintf("%v", val)
	}
}

// ctxWithMeta creates a context with persistent metainfo key-value pairs.
func ctxWithMeta(pairs ...string) context.Context {
	ctx := context.Background()
	for i := 0; i < len(pairs); i += 2 {
		ctx = metainfo.WithPersistentValue(ctx, pairs[i], pairs[i+1])
	}
	return ctx
}

// ===========================
// RegisterUser tests
// ===========================

func TestRegisterUser_Success(t *testing.T) {
	store := newTestUserStore()
	rdb := newTestRedisOps()
	svc := newTestHandler(store, rdb)

	_, err := svc.RegisterUser(context.Background(), &v1.RegisterUserReq{
		Phone:    "13800138000",
		Password: "password1",
	})
	if err != nil {
		t.Fatalf("RegisterUser success: unexpected error: %v", err)
	}
}

func TestRegisterUser_PhoneExists(t *testing.T) {
	store := newTestUserStore()
	rdb := newTestRedisOps()
	svc := newTestHandler(store, rdb)

	// Register first user
	_, err := svc.RegisterUser(context.Background(), &v1.RegisterUserReq{
		Phone:    "13800138000",
		Password: "password1",
	})
	if err != nil {
		t.Fatalf("first register: unexpected error: %v", err)
	}

	// Try same phone again
	_, err = svc.RegisterUser(context.Background(), &v1.RegisterUserReq{
		Phone:    "13800138000",
		Password: "password2",
	})
	if err == nil {
		t.Fatal("expected error for duplicate phone")
	}
	if code := errCode(err); code != errcode.CodeUserPhoneExists {
		t.Fatalf("expected code %d, got %d", errcode.CodeUserPhoneExists, code)
	}
}

func TestRegisterUser_PasswordWeak(t *testing.T) {
	store := newTestUserStore()
	rdb := newTestRedisOps()
	svc := newTestHandler(store, rdb)

	tests := []struct {
		name string
		pwd  string
	}{
		{"too short", "short1"},
		{"no digit", "abcdefgh"},
		{"no letter", "12345678"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.RegisterUser(context.Background(), &v1.RegisterUserReq{
				Phone:    "13800138000",
				Password: tt.pwd,
			})
			if err == nil {
				t.Fatal("expected error for weak password")
			}
			if code := errCode(err); code != errcode.CodeUserPwdWeak {
				t.Fatalf("expected code %d, got %d", errcode.CodeUserPwdWeak, code)
			}
		})
	}
}

func TestRegisterUser_InvalidPhone(t *testing.T) {
	store := newTestUserStore()
	rdb := newTestRedisOps()
	svc := newTestHandler(store, rdb)

	tests := []struct {
		name  string
		phone string
		code  uint32
	}{
		{"empty", "", errcode.CodeUserPhoneRequired},
		{"too short", "138001380", errcode.CodeUserPhoneFormat},
		{"bad prefix", "12800138000", errcode.CodeUserPhoneFormat},
		{"too long", "138001380001", errcode.CodeUserPhoneFormat},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.RegisterUser(context.Background(), &v1.RegisterUserReq{
				Phone:    tt.phone,
				Password: "password1",
			})
			if err == nil {
				t.Fatal("expected error for invalid phone")
			}
			if code := errCode(err); code != tt.code {
				t.Fatalf("expected code %d, got %d", tt.code, code)
			}
		})
	}
}

// ===========================
// Login tests
// ===========================

func TestLogin_Success(t *testing.T) {
	store := newTestUserStore()
	rdb := newTestRedisOps()
	svc := newTestHandler(store, rdb)

	// Pre-create a user with hashed password
	pwd := "password1"
	store.users[1] = &module.User{
		ID:       1,
		Password: hashPassword(pwd),
		Phone:    "13800138000",
	}
	store.phoneIdx["13800138000"] = 1

	resp, err := svc.Login(context.Background(), &v1.LoginReq{
		Phone:    "13800138000",
		Password: pwd,
	})
	if err != nil {
		t.Fatalf("Login success: unexpected error: %v", err)
	}
	if resp.Token == "" {
		t.Fatal("expected non-empty token")
	}
	if resp.RefreshToken == "" {
		t.Fatal("expected non-empty refresh token")
	}
	if resp.Id != 1 {
		t.Fatalf("expected user id 1, got %d", resp.Id)
	}
}

func TestLogin_WrongPassword(t *testing.T) {
	store := newTestUserStore()
	rdb := newTestRedisOps()
	svc := newTestHandler(store, rdb)

	pwd := "password1"
	store.users[1] = &module.User{
		ID:       1,
		Password: hashPassword(pwd),
		Phone:    "13800138000",
	}
	store.phoneIdx["13800138000"] = 1

	_, err := svc.Login(context.Background(), &v1.LoginReq{
		Phone:    "13800138000",
		Password: "wrongpassword1",
	})
	if err == nil {
		t.Fatal("expected error for wrong password")
	}
	if code := errCode(err); code != errcode.CodeUserPhonePwdWrong {
		t.Fatalf("expected code %d, got %d", errcode.CodeUserPhonePwdWrong, code)
	}
}

func TestLogin_PhoneNotFound(t *testing.T) {
	store := newTestUserStore()
	rdb := newTestRedisOps()
	svc := newTestHandler(store, rdb)

	_, err := svc.Login(context.Background(), &v1.LoginReq{
		Phone:    "13800138000",
		Password: "password1",
	})
	if err == nil {
		t.Fatal("expected error for phone not found")
	}
	if code := errCode(err); code != errcode.CodeUserPhonePwdWrong {
		t.Fatalf("expected code %d, got %d", errcode.CodeUserPhonePwdWrong, code)
	}
}

// ===========================
// Logout tests
// ===========================

func TestLogout_Success(t *testing.T) {
	store := newTestUserStore()
	rdb := newTestRedisOps()
	svc := newTestHandler(store, rdb)

	// Pre-populate a token set in Redis
	tokenKey := "shopmicro:user:token:1"
	rdb.sets[tokenKey] = map[string]bool{"test-token": true}

	ctx := ctxWithMeta("user_id", "1", "token", "test-token")
	_, err := svc.Logout(ctx, &v1.LogoutReq{})
	if err != nil {
		t.Fatalf("Logout success: unexpected error: %v", err)
	}
}

func TestLogout_MissingUserID(t *testing.T) {
	store := newTestUserStore()
	rdb := newTestRedisOps()
	svc := newTestHandler(store, rdb)

	// No user_id in metainfo
	ctx := ctxWithMeta("token", "test-token")
	_, err := svc.Logout(ctx, &v1.LogoutReq{})
	if err == nil {
		t.Fatal("expected error for missing user_id")
	}
	if code := errCode(err); code != errcode.CodeUserUnauthorized {
		t.Fatalf("expected code %d, got %d", errcode.CodeUserUnauthorized, code)
	}
}

// ===========================
// RefreshToken tests
// ===========================

func TestRefreshToken_Success(t *testing.T) {
	store := newTestUserStore()
	rdb := newTestRedisOps()
	svc := newTestHandler(store, rdb)

	// Pre-create a user
	store.users[1] = &module.User{ID: 1, Phone: "13800138000"}
	store.phoneIdx["13800138000"] = 1

	// Generate a valid refresh token
	rt, err := svc.rtGen.Generate(1)
	if err != nil {
		t.Fatalf("generate refresh token: %v", err)
	}

	// Store it in Redis (key = refreshTokenPrefix + token, value = userID)
	rtKey := refreshTokenPrefix + rt
	rdb.data[rtKey] = "1"

	resp, err := svc.RefreshToken(context.Background(), &v1.RefreshTokenReq{
		RefreshToken: rt,
	})
	if err != nil {
		t.Fatalf("RefreshToken success: unexpected error: %v", err)
	}
	if resp.Token == "" {
		t.Fatal("expected non-empty new token")
	}
	if resp.RefreshToken == "" {
		t.Fatal("expected non-empty new refresh token")
	}
}

func TestRefreshToken_InvalidRefreshToken(t *testing.T) {
	store := newTestUserStore()
	rdb := newTestRedisOps()
	svc := newTestHandler(store, rdb)

	_, err := svc.RefreshToken(context.Background(), &v1.RefreshTokenReq{
		RefreshToken: "invalid-token-value",
	})
	if err == nil {
		t.Fatal("expected error for invalid refresh token")
	}
	if code := errCode(err); code != errcode.CodeUserTokenInvalid {
		t.Fatalf("expected code %d, got %d", errcode.CodeUserTokenInvalid, code)
	}
}

func TestRefreshToken_EmptyRefreshToken(t *testing.T) {
	store := newTestUserStore()
	rdb := newTestRedisOps()
	svc := newTestHandler(store, rdb)

	_, err := svc.RefreshToken(context.Background(), &v1.RefreshTokenReq{})
	if err == nil {
		t.Fatal("expected error for empty refresh token")
	}
	if code := errCode(err); code != errcode.CodeUserTokenRequired {
		t.Fatalf("expected code %d, got %d", errcode.CodeUserTokenRequired, code)
	}
}

// ===========================
// UpdateUser tests
// ===========================

func TestUpdateUser_Success(t *testing.T) {
	store := newTestUserStore()
	rdb := newTestRedisOps()
	svc := newTestHandler(store, rdb)

	store.users[1] = &module.User{ID: 1, Phone: "13800138000", RealName: "old"}
	store.phoneIdx["13800138000"] = 1

	newName := "new name"
	ctx := ctxWithMeta("user_id", "1")
	_, err := svc.UpdateUser(ctx, &v1.UpdateUserReq{
		Id:       1,
		RealName: &newName,
	})
	if err != nil {
		t.Fatalf("UpdateUser success: unexpected error: %v", err)
	}
	if store.users[1].RealName != "new name" {
		t.Fatalf("expected RealName 'new name', got %q", store.users[1].RealName)
	}
}

func TestUpdateUser_InvalidID(t *testing.T) {
	store := newTestUserStore()
	rdb := newTestRedisOps()
	svc := newTestHandler(store, rdb)

	ctx := ctxWithMeta("user_id", "1")
	_, err := svc.UpdateUser(ctx, &v1.UpdateUserReq{Id: 0})
	if err == nil {
		t.Fatal("expected error for invalid id")
	}
	if code := errCode(err); code != errcode.CodeUserInvalidID {
		t.Fatalf("expected code %d, got %d", errcode.CodeUserInvalidID, code)
	}
}

func TestUpdateUser_NoFields(t *testing.T) {
	store := newTestUserStore()
	rdb := newTestRedisOps()
	svc := newTestHandler(store, rdb)

	ctx := ctxWithMeta("user_id", "1")
	_, err := svc.UpdateUser(ctx, &v1.UpdateUserReq{Id: 1})
	if err == nil {
		t.Fatal("expected error for no update fields")
	}
	if code := errCode(err); code != errcode.CodeUserNothingUpdate {
		t.Fatalf("expected code %d, got %d", errcode.CodeUserNothingUpdate, code)
	}
}

func TestUpdateUser_Forbidden(t *testing.T) {
	store := newTestUserStore()
	rdb := newTestRedisOps()
	svc := newTestHandler(store, rdb)

	store.users[1] = &module.User{ID: 1, Phone: "13800138000"}
	store.phoneIdx["13800138000"] = 1

	// user_id=2 tries to update user 1
	ctx := ctxWithMeta("user_id", "2")
	newName := "hacked"
	_, err := svc.UpdateUser(ctx, &v1.UpdateUserReq{
		Id:       1,
		RealName: &newName,
	})
	if err == nil {
		t.Fatal("expected error for ownership mismatch")
	}
	if code := errCode(err); code != errcode.CodeUserForbidden {
		t.Fatalf("expected code %d, got %d", errcode.CodeUserForbidden, code)
	}
}

// ===========================
// DeleteUser tests
// ===========================

func TestDeleteUser_Success(t *testing.T) {
	store := newTestUserStore()
	rdb := newTestRedisOps()
	svc := newTestHandler(store, rdb)

	store.users[1] = &module.User{ID: 1, Phone: "13800138000"}
	store.phoneIdx["13800138000"] = 1

	ctx := ctxWithMeta("user_id", "1")
	_, err := svc.DeleteUser(ctx, &v1.DeleteUserReq{Id: 1})
	if err != nil {
		t.Fatalf("DeleteUser success: unexpected error: %v", err)
	}
	if _, ok := store.users[1]; ok {
		t.Fatal("user should be deleted")
	}
}

func TestDeleteUser_NotFound(t *testing.T) {
	store := newTestUserStore()
	rdb := newTestRedisOps()
	svc := newTestHandler(store, rdb)

	ctx := ctxWithMeta("user_id", "999")
	_, err := svc.DeleteUser(ctx, &v1.DeleteUserReq{Id: 999})
	if err == nil {
		t.Fatal("expected error for user not found")
	}
	if code := errCode(err); code != errcode.CodeUserNotFound {
		t.Fatalf("expected code %d, got %d", errcode.CodeUserNotFound, code)
	}
}

func TestDeleteUser_InvalidID(t *testing.T) {
	store := newTestUserStore()
	rdb := newTestRedisOps()
	svc := newTestHandler(store, rdb)

	ctx := ctxWithMeta("user_id", "1")
	_, err := svc.DeleteUser(ctx, &v1.DeleteUserReq{Id: 0})
	if err == nil {
		t.Fatal("expected error for invalid id")
	}
	if code := errCode(err); code != errcode.CodeUserInvalidID {
		t.Fatalf("expected code %d, got %d", errcode.CodeUserInvalidID, code)
	}
}
