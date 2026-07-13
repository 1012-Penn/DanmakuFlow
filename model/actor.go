package model

// Actor 表示一个经过认证或匿名的请求发起者。
//
// Authenticated == false 表示匿名用户（只允许观看，不允许发送弹幕）。
// 匿名用户的 UserID/Username/Nickname 均为空字符串。
//
// Actor 定义在 model 包中，供 websocket 和 service 共同引用，避免循环依赖。
type Actor struct {
	UserID        string
	Username      string
	Nickname      string
	Authenticated bool
}
