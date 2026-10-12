package handler

// 请求 DTO。

type loginRequest struct {
	Email    string `json:"email" binding:"required,max=320"`
	Password string `json:"password" binding:"required,max=1024"`
}

type quickLoginRequest struct {
	Token string `json:"token" binding:"required,max=128"`
}

type reauthRequest struct {
	Password string `json:"password" binding:"required,max=1024"`
}

// 响应 DTO：字段名照前端 schema（core/api.ts、admin/LoginPage.tsx、admin/me.ts、portal/screens/account/api.ts）。

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

// loginResponse 门户与后台登录共用。
type loginResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
	UserID      string `json:"user_id"`
}

type sessionDTO struct {
	ID         string `json:"id"`
	Current    bool   `json:"current"`
	UserAgent  string `json:"user_agent"`
	CreatedAt  string `json:"created_at"`
	LastSeenAt string `json:"last_seen_at"`
	ExpiresAt  string `json:"expires_at"`
}

type sessionsResponse struct {
	Sessions []sessionDTO `json:"sessions"`
}

type revokeSessionResponse struct {
	Revoked bool `json:"revoked"`
}

type quickLoginResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
	ExpiresIn int    `json:"expires_in"`
}

type adminMeResponse struct {
	UserID      string  `json:"user_id"`
	Email       string  `json:"email"`
	DisplayName *string `json:"display_name"`
	Reauthed    bool    `json:"reauthed"`
}
