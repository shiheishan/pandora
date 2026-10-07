package identity

// 按邮箱找人的 SQL。
//
// 条件写成 email_lower = lower($2)，不写 email = $2：users.email 是 citext，而 citext
// 的等号不是 LEAKPROOF，在 RLS 下不能被下推成索引条件，租户里的每个用户都会被取出来
// 逐行比较（5k-r3 登录查口令平均 94.6ms）。email_lower 是 lower(email::text) 的存储型
// 生成列（迁移 00119，索引 (tenant_id, email_lower)），texteq 是 LEAKPROOF，lower()
// 只作用在参数上，整条条件可以走索引。
//
// 保留 email = $2::citext 作附加过滤：它只在索引取出的那一两行上算，结果与改前逐行
// 相同。必须显式转成 citext：$2 已被 lower($2::text) 定成 text，而 text→citext 只是
// 赋值转换、citext→text 却是隐式转换，不写的话 citext = text 会被解析成区分大小写的
// text 等号。
const (
	loginCredentialSQL = `
			SELECT u.id, u.status, p.phc
			  FROM users u
			  JOIN user_passwords p ON p.user_id = u.id
			 WHERE u.tenant_id = $1
			   AND u.email_lower = lower($2::text)
			   AND u.email = $2::citext`

	registrationEmailTakenSQL = `SELECT EXISTS(
			SELECT 1 FROM users
			 WHERE tenant_id = $1 AND email_lower = lower($2::text) AND email = $2::citext)`
)
