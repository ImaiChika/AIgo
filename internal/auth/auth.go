// Package auth 提供用户认证和权限管理功能。
// 包括 JWT 签发/验证、bcrypt 密码哈希、RBAC 角色权限控制。
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"aigo/internal/domain"

	"golang.org/x/crypto/bcrypt"
)

// 预定义错误，供调用方判断具体错误类型。
var (
	ErrInvalidCredentials = errors.New("用户名或密码错误")
	ErrUserDisabled       = errors.New("账号已被禁用")
	ErrInvalidToken       = errors.New("无效的登录凭证")
	ErrTokenExpired       = errors.New("登录已过期")
	ErrPermissionDenied   = errors.New("权限不足")
)

// User 用户信息（不含密码）。
type User struct {
	ID          string    `json:"id"`           // 用户唯一标识
	Username    string    `json:"username"`     // 登录用户名
	DisplayName string    `json:"display_name"` // 显示昵称
	Role        string    `json:"role"`         // 角色：admin/expert/teacher
	Enabled     bool      `json:"enabled"`      // 是否启用
	CreatedAt   time.Time `json:"created_at"`   // 创建时间
	UpdatedAt   time.Time `json:"updated_at"`   // 更新时间
}

// Claims JWT 声明（payload），包含用户基本信息和过期时间。
type Claims struct {
	UserID      string `json:"user_id"`      // 用户 ID
	Username    string `json:"username"`     // 用户名
	DisplayName string `json:"display_name"` // 昵称
	Role        string `json:"role"`         // 角色
	Exp         int64  `json:"exp"`          // 过期时间戳（Unix 秒）
}

// Service 认证服务，持有数据库连接和 JWT 密钥。
type Service struct {
	db        *sql.DB     // 数据库连接
	jwtSecret []byte      // JWT 签名密钥
	jwtExpiry time.Duration // JWT 有效期
}

// NewService 创建认证服务实例。
func NewService(db *sql.DB, jwtSecret string, expiry time.Duration) *Service {
	return &Service{
		db:        db,
		jwtSecret: []byte(jwtSecret),
		jwtExpiry: expiry,
	}
}

// InitAdmin 初始化默认管理员账号。如果已存在则跳过。
func (s *Service) InitAdmin(username, password, displayName string) error {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM users WHERE username=$1", username).Scan(&count)
	if err != nil {
		return err
	}
	if count > 0 {
		return nil // 已存在，不重复创建
	}

	// bcrypt 哈希密码（不可逆）
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

// Login 用户登录：验证密码 → 生成 JWT token。
func (s *Service) Login(username, password string) (string, *User, error) {
	var user User
	var passwordHash string
	var enabled bool

	// 从数据库查询用户
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

	// 检查账号是否被禁用
	if !enabled {
		return "", nil, ErrUserDisabled
	}

	// 验证密码
	if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(password)); err != nil {
		return "", nil, ErrInvalidCredentials
	}

	// 生成 JWT
	token, err := s.generateToken(&user)
	if err != nil {
		return "", nil, err
	}

	return token, &user, nil
}

// ValidateToken 验证 JWT 并返回声明内容。
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

	// 检查是否过期
	if time.Now().Unix() > claims.Exp {
		return nil, ErrTokenExpired
	}

	return &claims, nil
}

// GetUserByID 根据 ID 获取用户信息。
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

// SyncExpertUsers 同步专家角色用户到专家库。
// 检查所有 role=expert 的用户，如果专家库没有对应记录，自动创建。
func (s *Service) SyncExpertUsers(ctx context.Context, expertStore ExpertSyncStore) error {
	users, err := s.ListUsers()
	if err != nil {
		return err
	}
	for _, u := range users {
		if u.Role == "expert" {
			existing, _ := expertStore.GetExpert(ctx, u.ID)
			if existing == nil {
				expertStore.SaveExpert(ctx, domain.Expert{
					ID:      u.ID,
					Name:    u.DisplayName,
					Enabled: true,
				})
			}
		}
	}
	return nil
}

// ExpertSyncStore 同步所需的最小专家存储接口。
type ExpertSyncStore interface {
	GetExpert(ctx context.Context, id string) (*domain.Expert, error)
	SaveExpert(ctx context.Context, expert domain.Expert) error
}

// UpdateDisplayName 修改用户昵称。
func (s *Service) UpdateDisplayName(userID, displayName string) error {
	_, err := s.db.Exec("UPDATE users SET display_name=$1, updated_at=NOW() WHERE id=$2", displayName, userID)
	return err
}

// ListUsers 列出所有用户。
func (s *Service) ListUsers() ([]User, error) {
	rows, err := s.db.Query(`SELECT id, username, display_name, role, enabled, created_at, updated_at FROM users ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Role, &u.Enabled, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, u)
	}
	return result, nil
}

// CreateUser 创建新用户。
func (s *Service) CreateUser(username, password, displayName, role string) (*User, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	id := fmt.Sprintf("user-%d", time.Now().UnixNano())
	_, err = s.db.Exec(`INSERT INTO users (id, username, password_hash, display_name, role, enabled) VALUES ($1,$2,$3,$4,$5,true)`,
		id, username, string(hash), displayName, role)
	if err != nil {
		return nil, err
	}
	return s.GetUserByID(id)
}

// ChangePassword 修改密码（需要验证旧密码）。
func (s *Service) ChangePassword(userID, oldPassword, newPassword string) error {
	var hash string
	err := s.db.QueryRow("SELECT password_hash FROM users WHERE id=$1", userID).Scan(&hash)
	if err != nil {
		return err
	}
	// 验证旧密码
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(oldPassword)); err != nil {
		return fmt.Errorf("旧密码错误")
	}
	// 哈希新密码
	newHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("UPDATE users SET password_hash=$1, updated_at=NOW() WHERE id=$2", string(newHash), userID)
	return err
}

// generateToken 生成 JWT token。
func (s *Service) generateToken(user *User) (string, error) {
	claims := Claims{
		UserID:      user.ID,
		Username:    user.Username,
		DisplayName: user.DisplayName,
		Role:        user.Role,
		Exp:         time.Now().Add(s.jwtExpiry).Unix(),
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload, _ := json.Marshal(claims)
	payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
	sig := s.sign(header + "." + payloadB64)
	return header + "." + payloadB64 + "." + sig, nil
}

// sign 使用 HMAC-SHA256 签名。
func (s *Service) sign(data string) string {
	mac := hmac.New(sha256.New, s.jwtSecret)
	mac.Write([]byte(data))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// CheckPermission 检查角色是否有指定权限。
func CheckPermission(role, action string) bool {
	// 定义角色权限表
	permissions := map[string][]string{
		"admin": {"user:manage", "expert:create", "expert:update", "expert:list",
			"question:generate", "question:list", "question:view", "question:delete",
			"knowledge:import", "knowledge:list", "knowledge:search",
			"review:submit", "review:action", "review:view",
			"image:generate", "image:view", "image:review"},
		"expert": {"expert:list",
			"question:generate", "question:list", "question:view", "question:delete",
			"knowledge:import", "knowledge:list", "knowledge:search",
			"review:submit", "review:action", "review:view",
			"image:generate", "image:view", "image:review"},
		"teacher": {
			"question:generate", "question:list", "question:view",
			"knowledge:list", "knowledge:search",
			"review:submit", "review:view",
			"image:view"},
	}
	allowed, ok := permissions[role]
	if !ok {
		return false
	}
	for _, a := range allowed {
		if a == action {
			return true
		}
	}
	return false
}
