// Package auth 提供用户认证和权限管理功能。
// 包括 JWT 签发/验证、bcrypt 密码哈希、RBAC 角色权限控制。
//
// 权限模型：
//   - 用户可属于一个角色模板（roles 表，管理员可自定义 n 种角色）
//   - 用户可直接分配权限点（users.permissions）和题库范围（users.bank_ids）
//   - 有效动作权限 = 角色模板权限 ∪ 直接分配权限
//   - bank_ids 是用户级资源边界，对两种来源的题库范围权限一并生效
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
	ErrInvalidCredentials          = errors.New("用户名或密码错误")
	ErrUserDisabled                = errors.New("账号已被禁用")
	ErrInvalidToken                = errors.New("无效的登录凭证")
	ErrTokenExpired                = errors.New("登录已过期")
	ErrPermissionDenied            = errors.New("权限不足")
	ErrSuperAdminOnly              = errors.New("仅超级管理员可执行此操作")
	ErrProtectedAccount            = errors.New("超级管理员账号受保护，不能修改、禁用或删除")
	ErrSuperAdminExists            = errors.New("系统只能有一个超级管理员")
	ErrSuperAdminRoleNotAssignable = errors.New("超级管理员角色不能通过用户管理分配")
	ErrProtectedRole               = errors.New("超级管理员角色受保护，不能修改或删除")
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
	BankIDs           []string  `json:"bank_ids"`           // 用户级题库范围（空=全部）
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
	db                *sql.DB
	jwtSecret         []byte
	jwtExpiry         time.Duration
	dummyPasswordHash []byte
}

// NewService 创建认证服务实例。
func NewService(db *sql.DB, jwtSecret string, expiry time.Duration) *Service {
	dummyHash, _ := bcrypt.GenerateFromPassword([]byte("aigo-dummy-password-comparison"), bcrypt.DefaultCost)
	return &Service{
		db:                db,
		jwtSecret:         []byte(jwtSecret),
		jwtExpiry:         expiry,
		dummyPasswordHash: dummyHash,
	}
}

// ===== 内置角色 =====

// builtinRoles 内置角色模板。
// super_admin 是系统唯一的最高权限角色；admin 是超级管理员可下放的业务管理员角色。
func builtinRoles() []domain.Role {
	return []domain.Role{
		{
			ID:          domain.RoleSuperAdmin,
			Name:        "超级管理员",
			Description: "系统唯一的最高权限账号，负责分配管理员和维护角色模板",
			IsBuiltin:   true,
			Permissions: allPermissions(),
		},
		{
			ID:          domain.RoleAdmin,
			Name:        "管理员",
			Description: "由超级管理员分配的业务管理员，可管理人员、题库、流程并参与审核决断",
			IsBuiltin:   true,
			Permissions: delegatedAdminPermissions(),
		},
		{
			ID:          domain.RoleExpert,
			Name:        "审题专家",
			Description: "可使用个人题库命题、处理退回修改，并审核分配给自己的任务",
			IsBuiltin:   true,
			Permissions: []string{
				domain.PermQuestionView, domain.PermQuestionEdit,
				domain.PermQuestionGenerate,
				domain.PermQuestionShare,
				domain.PermReviewDo,
				// 出题人需要跟踪本人题目的审核结论与评语（个人范围审核记录页）。
				domain.PermReviewResults,
			},
		},
		{
			ID:        domain.RoleTeacher,
			Name:      "命题教师",
			IsBuiltin: true,
			Permissions: []string{
				domain.PermQuestionView, domain.PermQuestionEdit,
				domain.PermQuestionGenerate,
				domain.PermQuestionShare,
				domain.PermStatsView,
			},
		},
	}
}

// allPermissions 返回系统当前定义的完整权限集，避免新增权限时超级管理员漏配。
func allPermissions() []string {
	permissions := domain.AllPermissions()
	result := make([]string, 0, len(permissions))
	for _, permission := range permissions {
		result = append(result, permission.Code)
	}
	return result
}

// delegatedAdminPermissions 返回管理员默认权限：保留业务管理能力，
// 排除角色模板管理和批量推理；批量推理由超级管理员按需单独授予。
func delegatedAdminPermissions() []string {
	// question:view_all（查看全部题库）按产品口径不随管理员角色默认下发：
	// 未授予时管理员同样只能看本人题目，需要跨出题人送审时由超级管理员显式授予。
	result := make([]string, 0, len(domain.AllPermissions())-3)
	for _, permission := range domain.AllPermissions() {
		if permission.Code == domain.PermRoleManage || permission.Code == domain.PermBatchRun ||
			permission.Code == domain.PermQuestionViewAll {
			continue
		}
		result = append(result, permission.Code)
	}
	return result
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
			// 超级管理员必须始终保持完整权限；否则旧库中被改过的模板可能导致
			// 系统失去唯一的最高权限入口。
			if r.ID == domain.RoleSuperAdmin && !samePermissions(valid, r.Permissions) {
				valid = append([]string(nil), r.Permissions...)
				changed = true
			}
			// 管理员角色补齐新增业务权限，但始终移除角色模板管理权限；
			// 其余自定义角色保持管理员的有效权限修改。
			if r.ID == domain.RoleAdmin {
				filtered := valid[:0]
				for _, p := range valid {
					if p != domain.PermRoleManage {
						filtered = append(filtered, p)
					} else {
						changed = true
					}
				}
				valid = filtered
				have := make(map[string]bool, len(valid))
				for _, p := range valid {
					have[p] = true
				}
				for _, p := range delegatedAdminPermissions() {
					if !have[p] {
						valid = append(valid, p)
						changed = true
					}
				}
			}
			// 医学专家同时是个人题库用户和审核人：可单题命题、查看自己的三层题库、
			// 处理待修改、分享正式题并查看本人审核记录；不包含全局库、汇总统计或管理权限。
			// 专家是岗位型内置角色：启动自愈把权限恢复为模板集，防止旧库残留缺漏；
			// 批量推理是可按需授予的例外：超级管理员若明确勾选，自愈必须保留这项调整。
			if r.ID == domain.RoleExpert {
				keepBatch := containsPermission(valid, domain.PermBatchRun)
				restored := append([]string(nil), r.Permissions...)
				if keepBatch {
					restored = append(restored, domain.PermBatchRun)
				}
				if !samePermissions(valid, restored) {
					valid = restored
					changed = true
				}
			}
			// 命题教师需要能把本人已通过审核的正式题目提交分享申请；
			// 仅补齐这一新增能力，不覆盖管理员对教师其他权限的调整。
			if r.ID == domain.RoleTeacher {
				have := make(map[string]bool, len(valid))
				for _, p := range valid {
					have[p] = true
				}
				for _, p := range []string{domain.PermQuestionShare} {
					if !have[p] {
						valid = append(valid, p)
						changed = true
					}
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

func samePermissions(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
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
		// 现有版本的默认 admin 账号需要升级为唯一超级管理员。只在数据库
		// 尚无超级管理员时执行，避免覆盖机构已经明确配置好的账号。
		if username == "admin" {
			var role string
			lookupErr := s.db.QueryRow(`SELECT role FROM users WHERE username=$1`, username).Scan(&role)
			if lookupErr != nil && lookupErr != sql.ErrNoRows {
				return false, "", lookupErr
			}
			if lookupErr == nil && role == domain.RoleAdmin {
				var superCount int
				if err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE role=$1`, domain.RoleSuperAdmin).Scan(&superCount); err != nil {
					return false, "", err
				}
				if superCount == 0 {
					if _, err := s.db.Exec(`UPDATE users SET role=$1, permissions='{}', bank_ids='{}', updated_at=NOW() WHERE username=$2`, domain.RoleSuperAdmin, username); err != nil {
						return false, "", err
					}
				}
			}
		}
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
		VALUES ($1, $2, $3, $4, $5, '{}', '{}', true)
	`, id, username, string(hash), displayName, domain.RoleSuperAdmin)
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
		_ = bcrypt.CompareHashAndPassword(s.dummyPasswordHash, []byte(password))
		return "", nil, ErrInvalidCredentials
	}
	if err != nil {
		return "", nil, err
	}

	if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(password)); err != nil {
		return "", nil, ErrInvalidCredentials
	}
	if !user.Enabled {
		return "", nil, ErrUserDisabled
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

// CreateUserAs 在调用者权限范围内创建用户。
// 普通管理员只能创建/授权不超过自身能力的业务账号，且不能分配 admin 或 super_admin。
func (s *Service) CreateUserAs(ctx context.Context, actorID, username, password, displayName, role string, permissions, bankIDs []string) (*User, error) {
	if err := s.validateUserMutation(ctx, actorID, "", role, permissions, bankIDs); err != nil {
		return nil, err
	}
	user, err := s.CreateUser(username, password, displayName, role, permissions, bankIDs)
	if err != nil && role == domain.RoleSuperAdmin {
		// 数据库唯一索引负责处理并发创建，这里转换为稳定的业务错误。
		var count int
		if countErr := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE role=$1`, domain.RoleSuperAdmin).Scan(&count); countErr == nil && count > 0 {
			return nil, ErrSuperAdminExists
		}
	}
	return user, err
}

// UpdateUserAs 在调用者权限范围内更新用户。
func (s *Service) UpdateUserAs(ctx context.Context, actorID, userID, displayName, role string, permissions, bankIDs []string, enabled *bool) (*User, error) {
	if err := s.validateUserMutation(ctx, actorID, userID, role, permissions, bankIDs); err != nil {
		return nil, err
	}
	return s.UpdateUser(userID, displayName, role, permissions, bankIDs, enabled)
}

// DeleteUserAs 删除普通用户。超级管理员账号和当前登录账号都不能被删除。
func (s *Service) DeleteUserAs(ctx context.Context, actorID, userID string) (*User, error) {
	actor, err := s.GetUserByID(actorID)
	if err != nil || actor == nil || !actor.Enabled || !containsPermission(actor.Permissions, domain.PermUserManage) {
		return nil, ErrPermissionDenied
	}
	target, err := s.GetUserByID(userID)
	if err != nil {
		return nil, err
	}
	if target == nil {
		return nil, fmt.Errorf("用户不存在")
	}
	if target.ID == actorID || target.Role == domain.RoleSuperAdmin {
		return nil, ErrProtectedAccount
	}
	isSuper, err := s.IsSuperAdmin(ctx, actorID)
	if err != nil {
		return nil, err
	}
	if target.Role == domain.RoleAdmin && !isSuper {
		return nil, ErrSuperAdminOnly
	}
	if _, err := s.db.Exec(`DELETE FROM users WHERE id=$1`, userID); err != nil {
		return nil, err
	}
	return target, nil
}

// validateUserMutation 校验“谁可以给谁授予什么”。权限管理不能只依赖前端
// 隐藏选项，否则普通管理员可构造请求把最高权限授予新账号。
func (s *Service) validateUserMutation(ctx context.Context, actorID, targetID, role string, permissions, bankIDs []string) error {
	actor, err := s.GetUserByID(actorID)
	if err != nil || actor == nil || !actor.Enabled || !containsPermission(actor.Permissions, domain.PermUserManage) {
		return ErrPermissionDenied
	}
	targetRole := ""
	if targetID != "" {
		target, err := s.GetUserByID(targetID)
		if err != nil {
			return err
		}
		if target == nil {
			return fmt.Errorf("用户不存在")
		}
		targetRole = target.Role
		if target.ID == actorID || target.Role == domain.RoleSuperAdmin {
			return ErrProtectedAccount
		}
	}

	isSuper, err := s.IsSuperAdmin(ctx, actorID)
	if err != nil {
		return err
	}
	if targetRole == domain.RoleAdmin && !isSuper {
		return ErrSuperAdminOnly
	}
	if role == domain.RoleSuperAdmin {
		if !isSuper {
			return ErrSuperAdminOnly
		}
		return ErrSuperAdminRoleNotAssignable
	}
	if role == domain.RoleAdmin && !isSuper {
		return ErrSuperAdminOnly
	}

	for _, p := range permissions {
		if !domain.IsValidPermission(p) {
			return fmt.Errorf("无效的权限点: %s", p)
		}
		if p == domain.PermRoleManage && role != domain.RoleSuperAdmin {
			return ErrSuperAdminOnly
		}
	}
	rolePerms, err := s.permissionsForRole(role)
	if err != nil {
		return err
	}
	if role != "" && role != domain.RoleSuperAdmin && containsPermission(rolePerms, domain.PermRoleManage) {
		return ErrSuperAdminOnly
	}
	if !isSuper {
		allowed := make(map[string]bool, len(actor.Permissions))
		for _, p := range actor.Permissions {
			allowed[p] = true
		}
		for _, p := range append(append([]string(nil), rolePerms...), permissions...) {
			if !allowed[p] {
				return fmt.Errorf("管理员不能授予自身未拥有的权限: %s", p)
			}
		}
		if len(actor.BankIDs) > 0 {
			allowedBanks := make(map[string]bool, len(actor.BankIDs))
			for _, bankID := range actor.BankIDs {
				allowedBanks[bankID] = true
			}
			if len(bankIDs) == 0 {
				return fmt.Errorf("受限管理员不能把用户范围扩大到全部题库")
			}
			for _, bankID := range bankIDs {
				if !allowedBanks[bankID] {
					return fmt.Errorf("不能分配权限范围外的题库: %s", bankID)
				}
			}
		}
	}
	return nil
}

func (s *Service) permissionsForRole(roleID string) ([]string, error) {
	if roleID == "" {
		return nil, nil
	}
	role, err := s.GetRole(context.Background(), roleID)
	if err != nil {
		return nil, err
	}
	if role == nil {
		return nil, fmt.Errorf("角色模板 %s 不存在", roleID)
	}
	return role.Permissions, nil
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
	// 角色模板管理是超级管理员专属能力。即使旧库或历史直接授权中残留
	// role:manage，非 super_admin 也不能通过有效权限或前端菜单获得它。
	if roleID != domain.RoleSuperAdmin {
		delete(permSet, domain.PermRoleManage)
	}
	perms = make([]string, 0, len(permSet))
	for p := range permSet {
		perms = append(perms, p)
	}
	return perms, directPerms, bankIDs, nil
}

// IsSuperAdmin 判断用户当前是否挂载唯一的超级管理员角色。
// 不依据权限并集判断，防止普通角色或直接权限伪造最高身份。
func (s *Service) IsSuperAdmin(ctx context.Context, userID string) (bool, error) {
	var role string
	if err := s.db.QueryRowContext(ctx, `SELECT role FROM users WHERE id=$1`, userID).Scan(&role); err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	return domain.IsSuperAdminRole(role), nil
}

// HasPermission 检查用户是否拥有指定权限（不区分题库范围）。
func (s *Service) HasPermission(ctx context.Context, userID, perm string) (bool, error) {
	if perm == domain.PermRoleManage {
		return s.IsSuperAdmin(ctx, userID)
	}
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
// bank_ids 是用户级安全边界，对角色继承权限和直接权限一并生效，避免“角色有权限”
// 绕过管理员给该用户设置的专业题库范围。
func (s *Service) HasPermissionInBank(ctx context.Context, userID, perm, bankID string) (bool, error) {
	var directPerms, bankIDs []string
	var roleID string
	if err := s.db.QueryRow(`SELECT role, permissions, bank_ids FROM users WHERE id=$1`, userID).Scan(&roleID, (*pqArrayScanner)(&directPerms), (*pqArrayScanner)(&bankIDs)); err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	if perm == domain.PermRoleManage && roleID != domain.RoleSuperAdmin {
		return false, nil
	}
	hasPerm := false
	// 角色模板与直接权限先合并判断动作权限。
	if roleID != "" {
		var rolePerms []string
		if err := s.db.QueryRow(`SELECT permissions FROM roles WHERE id=$1`, roleID).Scan((*pqArrayScanner)(&rolePerms)); err == nil {
			for _, p := range rolePerms {
				if p == perm {
					hasPerm = true
					break
				}
			}
		}
	}
	for _, p := range directPerms {
		if p == perm {
			hasPerm = true
			break
		}
	}
	if !hasPerm {
		return false, nil
	}
	if len(bankIDs) == 0 {
		return true, nil
	}
	for _, b := range bankIDs {
		if b == bankID {
			return true, nil
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
	if perm == domain.PermRoleManage && roleID != domain.RoleSuperAdmin {
		return nil, false, false, nil
	}
	hasPerm = false
	// 角色模板与直接权限先合并；用户级 bank_ids 随后作为统一资源边界。
	if roleID != "" {
		var rolePerms []string
		if err := s.db.QueryRow(`SELECT permissions FROM roles WHERE id=$1`, roleID).Scan((*pqArrayScanner)(&rolePerms)); err == nil {
			for _, p := range rolePerms {
				if p == perm {
					hasPerm = true
					break
				}
			}
		}
	}
	for _, p := range directPerms {
		if p == perm {
			hasPerm = true
			break
		}
	}
	if !hasPerm {
		return nil, false, false, nil
	}
	if len(bankIDs) == 0 {
		return nil, true, true, nil
	}
	return bankIDs, false, true, nil
}

// ListReviewers 列出可审核指定题库的用户（自动匹配审题人）。
// 审题权限可来自角色或直接授权；有直接分配人时优先使用该精确名单，否则回退到角色审题人。
// bank_ids 仅用于决定任务可分配范围，并不授予题库浏览权。
// 系统管理员默认拥有全部权限，但不会被自动塞进专家投票名单。
func (s *Service) ListReviewers(ctx context.Context, bankID string) ([]string, error) {
	users, err := s.ListUsers()
	if err != nil {
		return nil, err
	}
	var direct, inherited []string
	for _, user := range users {
		if !user.Enabled || containsPermission(user.Permissions, domain.PermUserManage) ||
			!containsPermission(user.Permissions, domain.PermReviewDo) {
			continue
		}
		inScope := len(user.BankIDs) == 0
		if !inScope {
			for _, b := range user.BankIDs {
				if b == bankID {
					inScope = true
					break
				}
			}
		}
		if !inScope {
			continue
		}
		target := &inherited
		if containsPermission(user.DirectPermissions, domain.PermReviewDo) {
			target = &direct
		}
		*target = append(*target, user.ID)
	}
	if len(direct) > 0 {
		return direct, nil
	}
	return inherited, nil
}

func containsPermission(permissions []string, permission string) bool {
	for _, candidate := range permissions {
		if candidate == permission {
			return true
		}
	}
	return false
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

// SaveRoleAs 仅允许超级管理员维护角色模板。超级管理员角色本身不可编辑，
// 其他角色不能包含 role:manage，保证“超级管理员只有一个”不是前端约定。
func (s *Service) SaveRoleAs(ctx context.Context, actorID string, r domain.Role) error {
	if err := s.requireActiveSuperAdmin(ctx, actorID); err != nil {
		return err
	}
	if r.ID == domain.RoleSuperAdmin {
		return ErrProtectedRole
	}
	if containsPermission(r.Permissions, domain.PermRoleManage) {
		return ErrSuperAdminOnly
	}
	return s.SaveRole(ctx, r)
}

// SaveRole 创建或更新角色模板。
// 该方法供启动初始化和兼容调用使用；对外 HTTP 入口使用 SaveRoleAs。
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
	if r.ID != domain.RoleSuperAdmin && containsPermission(r.Permissions, domain.PermRoleManage) {
		return ErrSuperAdminOnly
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

// DeleteRoleAs 删除非系统保护角色模板。
func (s *Service) DeleteRoleAs(ctx context.Context, actorID, id string) error {
	if err := s.requireActiveSuperAdmin(ctx, actorID); err != nil {
		return err
	}
	if id == domain.RoleSuperAdmin {
		return ErrProtectedRole
	}
	return s.DeleteRole(ctx, id)
}

func (s *Service) requireActiveSuperAdmin(ctx context.Context, actorID string) error {
	user, err := s.GetUserByID(actorID)
	if err != nil {
		return err
	}
	if user == nil || !user.Enabled || !domain.IsSuperAdminRole(user.Role) {
		return ErrSuperAdminOnly
	}
	return nil
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
	if id == domain.RoleSuperAdmin {
		return ErrProtectedRole
	}
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
