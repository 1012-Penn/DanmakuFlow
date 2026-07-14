// Package model 提供弹幕系统的数据模型和共享错误定义。
package model

import "errors"

// 治理相关 sentinel errors。
// 定义在 model 包中，供 store/service/handler 共用，避免循环依赖。
var (
	ErrReportNotFound      = errors.New("report not found")
	ErrDanmakuNotFound     = errors.New("danmaku not found")
	ErrDuplicateReport     = errors.New("duplicate report")
	ErrForbiddenTransition = errors.New("forbidden status transition")
	ErrInsufficientRole    = errors.New("insufficient permissions")
	ErrCannotBanAdmin      = errors.New("cannot ban an admin user")
	ErrCannotChangeOwnRole = errors.New("cannot change your own role")
	ErrLastAdmin           = errors.New("cannot demote the last admin")
	ErrCannotBanSelf       = errors.New("cannot ban yourself")
	ErrTargetUserNotFound  = errors.New("target user not found")
	ErrInvalidRole         = errors.New("invalid role")
	ErrReportClosed        = errors.New("report already resolved or dismissed")
	ErrBannedUserReport    = errors.New("banned users cannot report")
	ErrDanmakuRoomMismatch = errors.New("danmaku does not belong to this room")
)
