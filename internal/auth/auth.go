package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

var (
	ErrInvalidCredentials = errors.New("用户名或密码错误")
	ErrUserDisabled       = errors.New("账号已被禁用")
	ErrInvalidToken       = errors.New("无效的登录凭证")
	ErrTokenExpired       = errors.New("登录已过期")
	ErrPermissionDenied   = errors.New("权限不足")
)

// User 用户信息。
type User struct {
	ID          string    `json:"id"`
	Username    string    `json:"username"`
	DisplayName string    `json:"display_name"`
	Role        string    `json:"role"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Claims JWT 声明。
type Claims struct {
	UserID      string `json:"user_id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	Exp         int64  `json:"exp"`
}

// Service 认证服务。
type Service struct {
	db        *sql.DB
	jwtSecret []byte
	jwtExpiry time.Duration
}

// NewService 创建认证服务。
func NewService(db *sql.DB, jwtSecret string, expiry time.Duration) *Service {
	return &Service{
		db:        db,
		jwtSecret: []byte(jwtSecret),
		jwtExpiry: expiry,
	}
}

// InitAdmin 初始化默认管理员账号。
func (s *Service) InitAdmin(username, password, displayName string) error {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM users WHERE username=$1", username).Scan(&count)
	if err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	_, err = s.db.Exec(`
		INSERT INTO users (id, username, password_hash, display_name, role, enabled)
		VALUES ($1, $2, $3, $4, 'admin', true)
	`, fmt.Sprintf("user-%d", time.Now().UnixNano()), username, string(hash), displayName)
	return err
}

// Login 用户登录。
func (s *Service) Login(username, password string) (string, *User, error) {
	var user User
	var passwordHash string
	var enabled bool

	err := s.db.QueryRow(`
		SELECT id, username, password_hash, display_name, role, enabled, created_at, updated_at
		FROM users WHERE username=$1
	`, username).Scan(&user.ID, &user.Username, &passwordHash, &user.DisplayName, &user.Role, &enabled, &user.CreatedAt, &user.UpdatedAt)
	if err == sql.ErrNoRows {
		return "", nil, ErrInvalidCredentials
	}
	if err != nil {
		return "", nil, err
	}

	if !enabled {
		return "", nil, ErrUserDisabled
	}

	if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(password)); err != nil {
		return "", nil, ErrInvalidCredentials
	}

	token, err := s.generateToken(&user)
	if err != nil {
		return "", nil, err
	}

	return token, &user, nil
}

// ValidateToken 验证 JWT 并返回 Claims。
func (s *Service) ValidateToken(token string) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, ErrInvalidToken
	}

	// 验证签名
	signingInput := parts[0] + "." + parts[1]
	expectedSig := s.sign(signingInput)
	actualSig := parts[2]
	if !hmac.Equal([]byte(expectedSig), []byte(actualSig)) {
		return nil, ErrInvalidToken
	}

	// 解码 payload
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrInvalidToken
	}

	var claims Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, ErrInvalidToken
	}

	// 检查过期
	if time.Now().Unix() > claims.Exp {
		return nil, ErrTokenExpired
	}

	return &claims, nil
}

// GetUserByID 根据 ID 获取用户。
func (s *Service) GetUserByID(id string) (*User, error) {
	var user User
	err := s.db.QueryRow(`
		SELECT id, username, display_name, role, enabled, created_at, updated_at
		FROM users WHERE id=$1
	`, id).Scan(&user.ID, &user.Username, &user.DisplayName, &user.Role, &user.Enabled, &user.CreatedAt, &user.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// ChangePassword 修改密码。
func (s *Service) ChangePassword(userID, oldPassword, newPassword string) error {
	var passwordHash string
	err := s.db.QueryRow("SELECT password_hash FROM users WHERE id=$1", userID).Scan(&passwordHash)
	if err != nil {
		return err
	}

	if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(oldPassword)); err != nil {
		return ErrInvalidCredentials
	}

	newHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	_, err = s.db.Exec("UPDATE users SET password_hash=$1, updated_at=NOW() WHERE id=$2", string(newHash), userID)
	return err
}

// CheckPermission 检查角色权限。
func CheckPermission(role, action string) bool {
	permissions := map[string][]string{
		"admin": {
			"question:generate", "question:list", "question:view",
			"knowledge:import", "knowledge:search", "knowledge:list",
			"expert:create", "expert:list", "expert:update",
			"flow:create", "flow:list",
			"review:submit", "review:action", "review:view",
			"image:generate", "image:review", "image:view",
			"export:questions",
			"user:manage",
		},
		"expert": {
			"question:list", "question:view",
			"knowledge:search", "knowledge:list",
			"review:action", "review:view",
			"image:review", "image:view",
			"export:questions",
		},
		"teacher": {
			"question:generate", "question:list", "question:view",
			"knowledge:import", "knowledge:search", "knowledge:list",
			"review:submit", "review:view",
			"image:generate", "image:view",
		},
	}

	allowed, ok := permissions[role]
	if !ok {
		return false
	}
	for _, p := range allowed {
		if p == action {
			return true
		}
	}
	return false
}

// generateToken 生成 JWT。
func (s *Service) generateToken(user *User) (string, error) {
	claims := Claims{
		UserID:      user.ID,
		Username:    user.Username,
		DisplayName: user.DisplayName,
		Role:        user.Role,
		Exp:         time.Now().Add(s.jwtExpiry).Unix(),
	}

	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))

	payloadBytes, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(payloadBytes)

	signingInput := header + "." + payload
	signature := s.sign(signingInput)

	return signingInput + "." + signature, nil
}

// sign HMAC-SHA256 签名。
func (s *Service) sign(data string) string {
	mac := hmac.New(sha256.New, s.jwtSecret)
	mac.Write([]byte(data))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
