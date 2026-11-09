package auth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

const (
	loginObservationWindow = 15 * time.Minute
	accountFailureLimit    = 8
	ipFailureLimit         = 30
	loginBaseBlock         = 5 * time.Second
	loginMaxBlock          = 5 * time.Minute

	registrationWindow   = time.Hour
	registrationIPLimit  = 6 // 允许同一来源在窗口内完成 5 次注册尝试，第 6 次开始阻断
	registrationBaseWait = 5 * time.Minute
	registrationMaxWait  = time.Hour
)

type loginLimitKey struct {
	scope     string
	hash      string
	threshold int
	window    time.Duration
	baseBlock time.Duration
	maxBlock  time.Duration
}

// CheckLoginAllowed 同时检查账号和来源 IP 两个独立桶。
func (s *Service) CheckLoginAllowed(ctx context.Context, username, clientIP string) (time.Duration, error) {
	return s.checkLimits(ctx, loginKeys(username, clientIP))
}

// RecordLoginFailure 原子记录失败并返回本次失败触发的等待时间。
func (s *Service) RecordLoginFailure(ctx context.Context, username, clientIP string) (time.Duration, error) {
	return s.recordLimitEvents(ctx, loginKeys(username, clientIP))
}

// RecordLoginSuccess 清除账号失败桶；IP 桶保留到观察窗口自然过期，避免一次成功登录重置同源攻击计数。
func (s *Service) RecordLoginSuccess(ctx context.Context, username string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM auth_login_limits WHERE scope='account' AND key_hash=$1`, loginKeyHash("account", normalizeLoginName(username)))
	return err
}

// CheckRegistrationAllowed 检查公开注册的来源 IP 桶。
func (s *Service) CheckRegistrationAllowed(ctx context.Context, clientIP string) (time.Duration, error) {
	return s.checkLimits(ctx, []loginLimitKey{registrationKey(clientIP)})
}

// RecordRegistrationAttempt 每次到达业务注册逻辑的尝试都计数，成功与失败都不能绕过注册频率限制。
func (s *Service) RecordRegistrationAttempt(ctx context.Context, clientIP string) (time.Duration, error) {
	return s.recordLimitEvents(ctx, []loginLimitKey{registrationKey(clientIP)})
}

// AuditSubject 返回不可逆的短标识，用于安全审计关联，不保存完整用户名或 IP。
func AuditSubject(scope, value string) string {
	return loginKeyHash(scope, strings.TrimSpace(value))[:12]
}

func loginKeys(username, clientIP string) []loginLimitKey {
	return []loginLimitKey{
		{
			scope: "account", hash: loginKeyHash("account", normalizeLoginName(username)),
			threshold: accountFailureLimit, window: loginObservationWindow,
			baseBlock: loginBaseBlock, maxBlock: loginMaxBlock,
		},
		{
			scope: "ip", hash: loginKeyHash("ip", normalizeClientIP(clientIP)),
			threshold: ipFailureLimit, window: loginObservationWindow,
			baseBlock: loginBaseBlock, maxBlock: loginMaxBlock,
		},
	}
}

func registrationKey(clientIP string) loginLimitKey {
	return loginLimitKey{
		scope: "register_ip", hash: loginKeyHash("register_ip", normalizeClientIP(clientIP)),
		threshold: registrationIPLimit, window: registrationWindow,
		baseBlock: registrationBaseWait, maxBlock: registrationMaxWait,
	}
}

func normalizeLoginName(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
}

func normalizeClientIP(clientIP string) string {
	clientIP = strings.TrimSpace(clientIP)
	if clientIP == "" {
		return "unknown"
	}
	return clientIP
}

func loginKeyHash(scope, value string) string {
	sum := sha256.Sum256([]byte(scope + "\x00" + value))
	return hex.EncodeToString(sum[:])
}

func (s *Service) checkLimits(ctx context.Context, keys []loginLimitKey) (time.Duration, error) {
	now := time.Now()
	var longest time.Duration
	for _, key := range keys {
		var blockedUntil sql.NullTime
		err := s.db.QueryRowContext(ctx, `
			SELECT blocked_until FROM auth_login_limits WHERE scope=$1 AND key_hash=$2
		`, key.scope, key.hash).Scan(&blockedUntil)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return 0, fmt.Errorf("读取登录频率限制失败: %w", err)
		}
		if blockedUntil.Valid && blockedUntil.Time.After(now) {
			if remaining := time.Until(blockedUntil.Time); remaining > longest {
				longest = remaining
			}
		}
	}
	return longest, nil
}

func (s *Service) recordLimitEvents(ctx context.Context, keys []loginLimitKey) (time.Duration, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("开始登录限速事务失败: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	now := time.Now()
	var longest time.Duration
	for _, key := range keys {
		delay, err := updateLoginLimit(ctx, tx, key, now)
		if err != nil {
			return 0, err
		}
		if delay > longest {
			longest = delay
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM auth_login_limits WHERE updated_at < $1`, now.Add(-30*24*time.Hour)); err != nil {
		return 0, fmt.Errorf("清理登录限速记录失败: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("提交登录限速事务失败: %w", err)
	}
	committed = true
	return longest, nil
}

func updateLoginLimit(ctx context.Context, tx *sql.Tx, key loginLimitKey, now time.Time) (time.Duration, error) {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO auth_login_limits (scope, key_hash, failure_count, window_started_at, updated_at)
		VALUES ($1,$2,0,$3,$3) ON CONFLICT (scope,key_hash) DO NOTHING
	`, key.scope, key.hash, now)
	if err != nil {
		return 0, fmt.Errorf("初始化登录限速记录失败: %w", err)
	}

	var count int
	var windowStarted time.Time
	if err := tx.QueryRowContext(ctx, `
		SELECT failure_count, window_started_at FROM auth_login_limits
		WHERE scope=$1 AND key_hash=$2 FOR UPDATE
	`, key.scope, key.hash).Scan(&count, &windowStarted); err != nil {
		return 0, fmt.Errorf("锁定登录限速记录失败: %w", err)
	}
	if now.Sub(windowStarted) >= key.window {
		count = 0
		windowStarted = now
	}
	count++

	var blockedUntil any
	var delay time.Duration
	if count >= key.threshold {
		delay = exponentialBlock(count-key.threshold, key.baseBlock, key.maxBlock)
		blockedUntil = now.Add(delay)
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE auth_login_limits SET failure_count=$3, window_started_at=$4,
			blocked_until=$5, updated_at=$6 WHERE scope=$1 AND key_hash=$2
	`, key.scope, key.hash, count, windowStarted, blockedUntil, now)
	if err != nil {
		return 0, fmt.Errorf("更新登录限速记录失败: %w", err)
	}
	return delay, nil
}

func exponentialBlock(steps int, base, maximum time.Duration) time.Duration {
	delay := base
	for i := 0; i < steps && delay < maximum; i++ {
		if delay > maximum/2 {
			return maximum
		}
		delay *= 2
	}
	if delay > maximum {
		return maximum
	}
	return delay
}
