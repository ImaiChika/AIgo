// Package auth 提供用户认证和权限管理功能。
// 包括 JWT 签发/验证、bcrypt 密码哈希、RBAC 角色权限控制。
//
// 权限模型：
//   - 用户可属于一个角色模板（roles 表，管理员可自定义 n 种角色）
//   - 用户可直接分配权限点（users.permissions）和题库范围（users.bank_ids）
//   - 有效权限 = 角色模板权限（全范围）∪ 直接分配权限（带题库范围）
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"aigo/internal/domain"
	"aigo/internal/review"

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
// Permissions 为有效权限并集（角色权限 ∪ 直接分配权限），供前端展示与判断。
// DirectPermissions 为用户直接分配的权限点（用户管理矩阵编辑用）。
type User struct {
	ID                string    `json:"id"`
	Username          string    `json:"username"`
	DisplayName       string    `json:"display_name"`
	Role              string    `json:"role"`               // 角色模板 ID（空=未分配角色）
	Permissions       []string  `json:"permissions"`        // 有效权限点列表
	DirectPermissions []string  `json:"direct_permissions"` // 直接分配的权限点
	BankIDs           []string  `json:"bank_ids"`           // 直接分配的题库范围（空=全部）
	Enabled           bool      `json:"enabled"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// Claims JWT 声明（payload），包含用户基本信息和过期时间。
type Claims struct {
	UserID      string `json:"user_id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	Exp         int64  `json:"exp"`
}

// Service 认证服务，持有数据库连接和 JWT 密钥。
type Service struct {
	db        *sql.DB
	jwtSecret []byte
	jwtExpiry time.Duration
}

// NewService 创建认证服务实例。
func NewService(db *sql.DB, jwtSecret string, expiry time.Duration) *Service {
	return &Service{
		db:        db,
		jwtSecret: []byte(jwtSecret),
		jwtExpiry: expiry,
	}
}

// ===== 内置角色 =====

// builtinRoles 内置角色模板（仅首次启动时写入，管理员后续可自由修改）。
func builtinRoles() []domain.Role {
	return []domain.Role{
		{
			ID:        "admin",
			Name:      "管理员",
			IsBuiltin: true,
			Permissions: []string{
				domain.PermUserManage, domain.PermRoleManage, domain.PermBankManage,
				domain.PermFlowManage, domain.PermExpertManage, domain.PermAuditView,
				domain.PermAICheck, domain.PermBatchRun, domain.PermKnowledgeMng,
				domain.PermImageGenerate, domain.PermImageReview,
				domain.PermQuestionView, domain.PermQuestionCreate, domain.PermQuestionEdit,
				domain.PermQuestionDelete, domain.PermQuestionGenerate, domain.PermQuestionDownload,
				domain.PermReviewDo, domain.PermReviewFinal,
				domain.PermStatsView,
			},
		},
		{
			ID:        "expert",
			Name:      "审题专家",
			IsBuiltin: true,
			Permissions: []string{
				domain.PermAICheck, domain.PermBatchRun, domain.PermKnowledgeMng,
				domain.PermImageGenerate, domain.PermImageReview,
				domain.PermQuestionView, domain.PermQuestionCreate, domain.PermQuestionEdit,
				domain.PermQuestionDelete, domain.PermQuestionGenerate, domain.PermQuestionDownload,
				domain.PermReviewDo,
				domain.PermStatsView,
			},
		},
		{
			ID:        "teacher",
			Name:      "命题教师",
			IsBuiltin: true,
			Permissions: []string{
				domain.PermAICheck, domain.PermBatchRun,
				domain.PermQuestionView, domain.PermQuestionCreate, domain.PermQuestionEdit,
				domain.PermQuestionGenerate, domain.PermQuestionDownload,
				domain.PermStatsView,
			},
		},
	}
}

// InitBuiltinRoles 写入内置角色模板（不存在才创建，不覆盖管理员修改）。
// 对已存在的内置角色：仅清理已失效的权限点（如被移除的权限），保留管理员自定义的有效权限。
func (s *Service) InitBuiltinRoles(ctx context.Context) error {
	for _, r := range builtinRoles() {
		existing, err := s.GetRole(ctx, r.ID)
		if err != nil {
			return err
		}
		if existing != nil {
			// 清理失效权限点（防止旧数据残留已移除的权限点导致角色保存报错）
			changed := false
			var valid []string
			for _, p := range existing.Permissions {
				if domain.IsValidPermission(p) {
					valid = append(valid, p)
				} else {
					changed = true
				}
			}
			if changed {
				existing.Permissions = valid
				if err := s.SaveRole(ctx, *existing); err != nil {
					return err
				}
			}
			continue
		}
		now := time.Now()
		r.CreatedAt = now
		r.UpdatedAt = now
		if err := s.SaveRole(ctx, r); err != nil {
			return err
		}
	}
	return nil
}

// ===== 账号 =====

// InitAdmin 初始化默认管理员账号。
// 仅在用户表完全为空（首次启动）时创建；已有任何用户（含自定义账号）时跳过，
// 防止每次启动用默认密码重建/顶替管理员账号。
// 传入密码不足 8 位时自动生成随机强密码（返回给调用方打印一次），避免弱口令。
func (s *Service) InitAdmin(username, password, displayName string) (bool, string, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count)
	if err != nil {
		return false, "", err
	}
	if count > 0 {
		return false, "", nil // 已有用户，不创建默认账号
	}

	if len(password) < 8 {
		buf := make([]byte, 12)
		if _, err := rand.Read(buf); err != nil {
			return false, "", err
		}
		password = base64.RawURLEncoding.EncodeToString(buf)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return false, "", err
	}

	id := fmt.Sprintf("user-%d", time.Now().UnixNano())
	_, err = s.db.Exec(`
		INSERT INTO users (id, username, password_hash, display_name, role, permissions, bank_ids, enabled)
		VALUES ($1, $2, $3, $4, 'admin', '{}', '{}', true)
	`, id, username, string(hash), displayName)
	if err != nil {
		return false, "", err
	}
	return true, password, nil
}

// GetUserByUsername 根据用户名查询用户，不存在时返回 nil。
func (s *Service) GetUserByUsername(username string) (*User, error) {
	var user User
	var passwordHash string
	err := s.db.QueryRow(`
		SELECT id, username, password_hash, display_name, role, enabled, created_at, updated_at
		FROM users WHERE username=$1
	`, username).Scan(&user.ID, &user.Username, &passwordHash, &user.DisplayName, &user.Role, &user.Enabled, &user.CreatedAt, &user.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	user.Permissions, user.DirectPermissions, user.BankIDs, err = s.effectivePermissions(user.ID)
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// Login 用户登录：验证密码 → 生成 JWT token。
func (s *Service) Login(username, password string) (string, *User, error) {
	var user User
	var passwordHash string

	err := s.db.QueryRow(`
		SELECT id, username, password_hash, display_name, role, enabled, created_at, updated_at
		FROM users WHERE username=$1
	`, username).Scan(&user.ID, &user.Username, &passwordHash, &user.DisplayName, &user.Role, &user.Enabled, &user.CreatedAt, &user.UpdatedAt)
	if err == sql.ErrNoRows {
		return "", nil, ErrInvalidCredentials
	}
	if err != nil {
		return "", nil, err
	}

	if !user.Enabled {
		return "", nil, ErrUserDisabled
	}

	if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(password)); err != nil {
		return "", nil, ErrInvalidCredentials
	}

	user.Permissions, user.DirectPermissions, user.BankIDs, err = s.effectivePermissions(user.ID)
	if err != nil {
		return "", nil, err
	}

	token, err := s.generateToken(&user)
	if err != nil {
		return "", nil, err
	}

	return token, &user, nil
}

// Register 用户自主注册。注册后默认无任何权限，由管理员在用户管理中分配。
func (s *Service) Register(username, password, displayName string) (*User, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return nil, fmt.Errorf("用户名不能为空")
	}
	if len(password) < 8 {
		return nil, fmt.Errorf("密码至少8位")
	}
	if displayName == "" {
		displayName = username
	}
	existing, err := s.GetUserByUsername(username)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, fmt.Errorf("用户名 %s 已存在", username)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	id := fmt.Sprintf("user-%d", time.Now().UnixNano())
	_, err = s.db.Exec(`INSERT INTO users (id, username, password_hash, display_name, role, permissions, bank_ids, enabled) VALUES ($1,$2,$3,$4,'','{}','{}',true)`,
		id, username, string(hash), displayName)
	if err != nil {
		return nil, err
	}
	return s.GetUserByID(id)
}

// ValidateToken 验证 JWT 并返回声明内容。
func (s *Service) ValidateToken(token string) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, ErrInvalidToken
	}

	signingInput := parts[0] + "." + parts[1]
	expectedSig := s.sign(signingInput)
	actualSig := parts[2]
	if !hmac.Equal([]byte(expectedSig), []byte(actualSig)) {
		return nil, ErrInvalidToken
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrInvalidToken
	}

	var claims Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, ErrInvalidToken
	}

	if time.Now().Unix() > claims.Exp {
		return nil, ErrTokenExpired
	}

	return &claims, nil
}

// GetUserByID 根据 ID 获取用户信息（含有效权限）。
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
	user.Permissions, user.DirectPermissions, user.BankIDs, err = s.effectivePermissions(user.ID)
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// ListUsers 列出所有用户（含有效权限）。
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
		u.Permissions, u.DirectPermissions, u.BankIDs, err = s.effectivePermissions(u.ID)
		if err != nil {
			return nil, err
		}
		result = append(result, u)
	}
	return result, nil
}

// CreateUser 管理员创建新用户，可指定角色模板、权限点和题库范围。
func (s *Service) CreateUser(username, password, displayName, role string, permissions, bankIDs []string) (*User, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return nil, fmt.Errorf("用户名不能为空")
	}
	if len(password) < 8 {
		return nil, fmt.Errorf("密码至少8位")
	}
	// 校验角色模板存在
	if role != "" {
		r, err := s.GetRole(context.Background(), role)
		if err != nil {
			return nil, err
		}
		if r == nil {
			return nil, fmt.Errorf("角色模板 %s 不存在", role)
		}
	}
	// 校验权限点合法
	for _, p := range permissions {
		if !domain.IsValidPermission(p) {
			return nil, fmt.Errorf("无效的权限点: %s", p)
		}
	}
	if displayName == "" {
		displayName = username
	}
	existing, err := s.GetUserByUsername(username)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, fmt.Errorf("用户名 %s 已存在", username)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	id := fmt.Sprintf("user-%d", time.Now().UnixNano())
	_, err = s.db.Exec(`INSERT INTO users (id, username, password_hash, display_name, role, permissions, bank_ids, enabled) VALUES ($1,$2,$3,$4,$5,$6,$7,true)`,
		id, username, string(hash), displayName, role, pqArray(permissions), pqArray(bankIDs))
	if err != nil {
		return nil, err
	}
	return s.GetUserByID(id)
}

// UpdateUser 更新用户的角色模板、直接权限、题库范围和启用状态。
func (s *Service) UpdateUser(userID, displayName, role string, permissions, bankIDs []string, enabled *bool) (*User, error) {
	if role != "" {
		r, err := s.GetRole(context.Background(), role)
		if err != nil {
			return nil, err
		}
		if r == nil {
			return nil, fmt.Errorf("角色模板 %s 不存在", role)
		}
	}
	for _, p := range permissions {
		if !domain.IsValidPermission(p) {
			return nil, fmt.Errorf("无效的权限点: %s", p)
		}
	}
	for _, b := range bankIDs {
		if b == "" {
			return nil, fmt.Errorf("题库范围不能包含空值")
		}
	}
	if displayName == "" {
		return nil, fmt.Errorf("显示名不能为空")
	}
	_, err := s.db.Exec(`
		UPDATE users SET display_name=$1, role=$2, permissions=$3, bank_ids=$4, updated_at=NOW()
		WHERE id=$5
	`, displayName, role, pqArray(permissions), pqArray(bankIDs), userID)
	if err != nil {
		return nil, err
	}
	if enabled != nil {
		_, err = s.db.Exec(`UPDATE users SET enabled=$1, updated_at=NOW() WHERE id=$2`, *enabled, userID)
		if err != nil {
			return nil, err
		}
	}
	return s.GetUserByID(userID)
}

// ChangePassword 修改密码（需要验证旧密码）。
func (s *Service) ChangePassword(userID, oldPassword, newPassword string) error {
	var hash string
	err := s.db.QueryRow("SELECT password_hash FROM users WHERE id=$1", userID).Scan(&hash)
	if err != nil {
		return err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(oldPassword)); err != nil {
		return fmt.Errorf("旧密码错误")
	}
	if len(newPassword) < 8 {
		return fmt.Errorf("新密码至少8位")
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("UPDATE users SET password_hash=$1, updated_at=NOW() WHERE id=$2", string(newHash), userID)
	return err
}

// UpdateDisplayName 修改用户昵称。
func (s *Service) UpdateDisplayName(userID, displayName string) error {
	_, err := s.db.Exec("UPDATE users SET display_name=$1, updated_at=NOW() WHERE id=$2", displayName, userID)
	return err
}

// ===== 权限 =====

// effectivePermissions 计算用户有效权限：角色模板权限 ∪ 直接分配权限。
// 同时返回直接分配的权限点和题库范围。
func (s *Service) effectivePermissions(userID string) (perms, directPerms, bankIDs []string, err error) {
	permSet := make(map[string]bool)
	var roleID string
	err = s.db.QueryRow(`SELECT role, permissions, bank_ids FROM users WHERE id=$1`, userID).Scan(&roleID, (*pqArrayScanner)(&directPerms), (*pqArrayScanner)(&bankIDs))
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil, nil, nil
		}
		return nil, nil, nil, err
	}
	if roleID != "" {
		var rolePerms []string
		err := s.db.QueryRow(`SELECT permissions FROM roles WHERE id=$1`, roleID).Scan((*pqArrayScanner)(&rolePerms))
		if err != nil && err != sql.ErrNoRows {
			return nil, nil, nil, err
		}
		for _, p := range rolePerms {
			permSet[p] = true
		}
	}
	for _, p := range directPerms {
		permSet[p] = true
	}
	perms = make([]string, 0, len(permSet))
	for p := range permSet {
		perms = append(perms, p)
	}
	return perms, directPerms, bankIDs, nil
}

// HasPermission 检查用户是否拥有指定权限（不区分题库范围）。
func (s *Service) HasPermission(ctx context.Context, userID, perm string) (bool, error) {
	perms, _, _, err := s.effectivePermissions(userID)
	if err != nil {
		return false, err
	}
	for _, p := range perms {
		if p == perm {
			return true, nil
		}
	}
	return false, nil
}

// HasPermissionInBank 检查用户是否拥有指定权限，且权限覆盖指定题库。
// 题库范围规则：用户 bank_ids 为空 = 全部题库；否则 bankID 必须在 bank_ids 中。
// 注意：角色模板带来的权限是全范围的，直接分配的权限受 bank_ids 限制。
func (s *Service) HasPermissionInBank(ctx context.Context, userID, perm, bankID string) (bool, error) {
	var directPerms, bankIDs []string
	var roleID string
	if err := s.db.QueryRow(`SELECT role, permissions, bank_ids FROM users WHERE id=$1`, userID).Scan(&roleID, (*pqArrayScanner)(&directPerms), (*pqArrayScanner)(&bankIDs)); err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	// 角色模板权限：全范围
	if roleID != "" {
		var rolePerms []string
		if err := s.db.QueryRow(`SELECT permissions FROM roles WHERE id=$1`, roleID).Scan((*pqArrayScanner)(&rolePerms)); err == nil {
			for _, p := range rolePerms {
				if p == perm {
					return true, nil
				}
			}
		}
	}
	// 直接权限：受题库范围限制
	for _, p := range directPerms {
		if p != perm {
			continue
		}
		if len(bankIDs) == 0 {
			return true, nil
		}
		for _, b := range bankIDs {
			if b == bankID {
				return true, nil
			}
		}
	}
	return false, nil
}

// GetBankScope 获取用户对某权限的题库范围。
// 返回：scope=题库ID列表（fullScope=true 表示全部题库）；hasPerm 表示是否拥有该权限。
func (s *Service) GetBankScope(ctx context.Context, userID, perm string) (scope []string, fullScope bool, hasPerm bool, err error) {
	var directPerms, bankIDs []string
	var roleID string
	if err := s.db.QueryRow(`SELECT role, permissions, bank_ids FROM users WHERE id=$1`, userID).Scan(&roleID, (*pqArrayScanner)(&directPerms), (*pqArrayScanner)(&bankIDs)); err != nil {
		if err == sql.ErrNoRows {
			return nil, false, false, nil
		}
		return nil, false, false, err
	}
	// 角色模板权限：全范围
	if roleID != "" {
		var rolePerms []string
		if err := s.db.QueryRow(`SELECT permissions FROM roles WHERE id=$1`, roleID).Scan((*pqArrayScanner)(&rolePerms)); err == nil {
			for _, p := range rolePerms {
				if p == perm {
					return nil, true, true, nil
				}
			}
		}
	}
	// 直接权限：受题库范围限制
	for _, p := range directPerms {
		if p == perm {
			if len(bankIDs) == 0 {
				return nil, true, true, nil
			}
			return bankIDs, false, true, nil
		}
	}
	return nil, false, false, nil
}

// ListReviewers 列出可审核指定题库的用户（自动匹配审题人）：
// 只匹配管理员在用户管理中"直接勾选审题权限 + 设置题库范围"的用户
// （bank_ids 空=全部），保证"内科分配3位老师"这类按库分配语义精确。
// 角色模板带来的审题权限不参与自动匹配（避免不活跃专家阻塞全员投票），
// 仅在完全匹配不到审题人时由 ListFallbackReviewers 兜底。
func (s *Service) ListReviewers(ctx context.Context, bankID string) ([]string, error) {
	rows, err := s.db.Query(`
		SELECT u.id, u.permissions, u.bank_ids
		FROM users u WHERE u.enabled = true ORDER BY u.created_at
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var id string
		var directPerms, bankIDs []string
		if err := rows.Scan(&id, (*pqArrayScanner)(&directPerms), (*pqArrayScanner)(&bankIDs)); err != nil {
			return nil, err
		}
		direct := false
		for _, p := range directPerms {
			if p == domain.PermReviewDo {
				direct = true
				break
			}
		}
		if !direct {
			continue
		}
		if len(bankIDs) == 0 {
			// 无范围限制 = 全部题库
			result = append(result, id)
			continue
		}
		for _, b := range bankIDs {
			if b == bankID {
				result = append(result, id)
				break
			}
		}
	}
	return result, rows.Err()
}

// ListFallbackReviewers 列出角色模板含审题权限且非系统管理员的启用用户（全范围），
// 用于"一个直接权限审题人都没有"时的兜底匹配（避免用户因未配置权限而无法提交）。
func (s *Service) ListFallbackReviewers(ctx context.Context) ([]string, error) {
	rows, err := s.db.Query(`
		SELECT u.id, u.role FROM users u WHERE u.enabled = true ORDER BY u.created_at
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var id, role string
		if err := rows.Scan(&id, &role); err != nil {
			return nil, err
		}
		if role == "" {
			continue
		}
		var rolePerms []string
		if err := s.db.QueryRow(`SELECT permissions FROM roles WHERE id=$1`, role).Scan((*pqArrayScanner)(&rolePerms)); err != nil {
			continue
		}
		hasReview, isAdmin := false, false
		for _, p := range rolePerms {
			if p == domain.PermReviewDo {
				hasReview = true
			}
			if p == domain.PermUserManage {
				isAdmin = true
			}
		}
		if hasReview && !isAdmin {
			result = append(result, id)
		}
	}
	return result, nil
}

// HasFinalRight 检查用户是否拥有最终把关权限。
func (s *Service) HasFinalRight(ctx context.Context, userID string) (bool, error) {
	return s.HasPermission(ctx, userID, domain.PermReviewFinal)
}

// ListReviewCandidates 列出全部有审题权限且启用的用户（流程配置选审核人用）。
func (s *Service) ListReviewCandidates(ctx context.Context) ([]review.Candidate, error) {
	users, err := s.ListUsers()
	if err != nil {
		return nil, err
	}
	var result []review.Candidate
	for _, u := range users {
		if !u.Enabled {
			continue
		}
		hasReview := false
		for _, p := range u.Permissions {
			if p == domain.PermReviewDo {
				hasReview = true
				break
			}
		}
		if !hasReview {
			continue
		}
		result = append(result, review.Candidate{
			ID:          u.ID,
			Username:    u.Username,
			DisplayName: u.DisplayName,
			BankIDs:     u.BankIDs,
		})
	}
	return result, nil
}

// ===== 角色模板 =====

// SaveRole 创建或更新角色模板。
func (s *Service) SaveRole(ctx context.Context, r domain.Role) error {
	if r.ID == "" {
		return fmt.Errorf("角色ID不能为空")
	}
	if r.Name == "" {
		return fmt.Errorf("角色名称不能为空")
	}
	for _, p := range r.Permissions {
		if !domain.IsValidPermission(p) {
			return fmt.Errorf("无效的权限点: %s", p)
		}
	}
	now := time.Now()
	if r.CreatedAt.IsZero() {
		r.CreatedAt = now
	}
	r.UpdatedAt = now
	_, err := s.db.Exec(`
		INSERT INTO roles (id, name, description, permissions, is_builtin, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (id) DO UPDATE SET
			name=EXCLUDED.name, description=EXCLUDED.description,
			permissions=EXCLUDED.permissions, updated_at=EXCLUDED.updated_at
	`, r.ID, r.Name, r.Description, pqArray(r.Permissions), r.IsBuiltin, r.CreatedAt, r.UpdatedAt)
	return err
}

// GetRole 获取角色模板。
func (s *Service) GetRole(ctx context.Context, id string) (*domain.Role, error) {
	row := s.db.QueryRow(`SELECT id, name, description, permissions, is_builtin, created_at, updated_at FROM roles WHERE id=$1`, id)
	var r domain.Role
	var perms []string
	err := row.Scan(&r.ID, &r.Name, &r.Description, (*pqArrayScanner)(&perms), &r.IsBuiltin, &r.CreatedAt, &r.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.Permissions = perms
	return &r, nil
}

// ListRoles 列出所有角色模板。
func (s *Service) ListRoles(ctx context.Context) ([]domain.Role, error) {
	rows, err := s.db.Query(`SELECT id, name, description, permissions, is_builtin, created_at, updated_at FROM roles ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.Role
	for rows.Next() {
		var r domain.Role
		var perms []string
		if err := rows.Scan(&r.ID, &r.Name, &r.Description, (*pqArrayScanner)(&perms), &r.IsBuiltin, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		r.Permissions = perms
		result = append(result, r)
	}
	return result, rows.Err()
}

// DeleteRole 删除角色模板。有用户引用时拒绝删除；内置角色可删除但需谨慎。
func (s *Service) DeleteRole(ctx context.Context, id string) error {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE role=$1`, id).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("角色 %s 仍被 %d 个用户引用，请先调整这些用户的角色", id, count)
	}
	_, err := s.db.Exec(`DELETE FROM roles WHERE id=$1`, id)
	return err
}

// ===== 专家库同步 =====

// ExpertSyncStore 同步所需的最小专家存储接口。
type ExpertSyncStore interface {
	GetExpert(ctx context.Context, id string) (*domain.Expert, error)
	SaveExpert(ctx context.Context, expert domain.Expert) error
}

// SyncReviewUsers 同步拥有审题权限的用户到专家库。
// 有审题权限的用户没有专家库记录时自动创建（ID 一致，便于流程配置引用）。
func (s *Service) SyncReviewUsers(ctx context.Context, expertStore ExpertSyncStore) error {
	users, err := s.ListUsers()
	if err != nil {
		return err
	}
	for _, u := range users {
		if !u.Enabled {
			continue
		}
		hasReview := false
		for _, p := range u.Permissions {
			if p == domain.PermReviewDo {
				hasReview = true
				break
			}
		}
		if !hasReview {
			continue
		}
		existing, _ := expertStore.GetExpert(ctx, u.ID)
		if existing == nil {
			expertStore.SaveExpert(ctx, domain.Expert{
				ID:      u.ID,
				Name:    u.DisplayName,
				Enabled: true,
			})
		}
	}
	return nil
}

// ===== JWT =====

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

// ===== 工具 =====

// pqArray 把字符串切片转为 PostgreSQL 数组字面量（带转义）。
func pqArray(items []string) string {
	if items == nil {
		return "{}"
	}
	parts := make([]string, len(items))
	for i, s := range items {
		parts[i] = `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// pqArrayScanner 扫描 PostgreSQL 数组到字符串切片。
type pqArrayScanner []string

func (s *pqArrayScanner) Scan(src interface{}) error {
	if src == nil {
		*s = []string{}
		return nil
	}
	var raw string
	switch v := src.(type) {
	case string:
		raw = v
	case []byte:
		raw = string(v)
	default:
		return fmt.Errorf("无法解析数组类型 %T", src)
	}
	if raw == "{}" || raw == "" {
		*s = []string{}
		return nil
	}
	inner := strings.TrimPrefix(strings.TrimSuffix(raw, "}"), "{")
	parts := strings.Split(inner, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		p = strings.Trim(p, `"`)
		p = strings.ReplaceAll(p, `\"`, `"`)
		p = strings.ReplaceAll(p, `\\`, `\`)
		if p != "" {
			out = append(out, p)
		}
	}
	*s = out
	return nil
}
