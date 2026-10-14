package service

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"text/template"
	"text/template/parse"
	"unicode/utf8"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

var errTemplateSyntax = errors.New("只能使用 {{变量名}} 形式的变量")

var tplVarName = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// templateVars 从模板文本里提取用到的变量名（升序去重）。
func templateVars(text string) ([]string, error) {
	t := parse.New("tpl")
	t.Mode = parse.SkipFuncCheck
	trees := map[string]*parse.Tree{}
	_, err := t.Parse(text, "", "", trees)
	if err != nil {
		return nil, errTemplateSyntax
	}
	for name := range trees {
		if name != "tpl" {
			return nil, errTemplateSyntax
		}
	}
	var names []string
	if t.Root != nil {
		for _, n := range t.Root.Nodes {
			var id string
			switch node := n.(type) {
			case *parse.TextNode:
				continue
			case *parse.ActionNode:
				id, err = actionVarName(node)
				if err != nil {
					return nil, errTemplateSyntax
				}
			default:
				return nil, errTemplateSyntax
			}
			if id != "" {
				names = append(names, id)
			}
		}
	}
	sort.Strings(names)
	return dedupeSorted(names), nil
}

func actionVarName(a *parse.ActionNode) (string, error) {
	p := a.Pipe
	if p.IsAssign || len(p.Decl) != 0 || len(p.Cmds) != 1 || len(p.Cmds[0].Args) != 1 {
		return "", errTemplateSyntax
	}
	arg := p.Cmds[0].Args[0]
	id, ok := arg.(*parse.IdentifierNode)
	if !ok || !tplVarName.MatchString(id.Ident) {
		return "", errTemplateSyntax
	}
	return id.Ident, nil
}

func dedupeSorted(sorted []string) []string {
	if len(sorted) == 0 {
		return []string{}
	}
	out := []string{sorted[0]}
	for i := 1; i < len(sorted); i++ {
		if sorted[i] != sorted[i-1] {
			out = append(out, sorted[i])
		}
	}
	return out
}

// unknownVariables 返回模板里不在白名单中的变量名。
func unknownVariables(text string, allowed []string) ([]string, error) {
	used, err := templateVars(text)
	if err != nil {
		return nil, err
	}
	allow := make(map[string]struct{}, len(allowed))
	for _, a := range allowed {
		allow[a] = struct{}{}
	}
	bad := []string{}
	for _, u := range used {
		if _, ok := allow[u]; !ok {
			bad = append(bad, u)
		}
	}
	return bad, nil
}

// renderText 按白名单与变量值渲染模板文本。
func renderText(text string, allowed []string, vars map[string]string) (string, error) {
	bad, err := unknownVariables(text, allowed)
	if err != nil {
		return "", err
	}
	if len(bad) > 0 {
		return "", fmt.Errorf("模板用到了未登记的变量：%s", strings.Join(bad, "、"))
	}
	fm := template.FuncMap{}
	for _, name := range allowed {
		name := name
		fm[name] = func() string { return vars[name] }
	}
	tpl, err := template.New("tpl").Funcs(fm).Parse(text)
	if err != nil {
		return "", errTemplateSyntax
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, nil); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// sampleVars 是预览渲染用的示例变量值。
var sampleVars = map[string]string{
	"site":       "潘多拉面板",
	"code":       "246810",
	"minutes":    "10",
	"plan":       "旗舰套餐",
	"days":       "3",
	"expires_at": "2026-08-31 23:59",
	"expired_at": "2026-08-31 23:59",
	"percent":    "85",
	"remaining":  "3.2 GB",
	"order_no":   "AO20260804-XXXXXX",
	"subject":    "无法连接节点",
	"amount":     "29.90 CNY",
	"email":      "us***@example.com",
	"ticket_no":  "TK20260804-ABCDEFGH",
	"body":       "节点连不上了，麻烦看一下。",
	"traffic":    "12.5 GB",
	"balance":    "88.00 CNY",
}

// renderPreview 用示例值渲染主题与正文，预览不因语法或白名单错误而失败。
func renderPreview(subject, body string, allowed []string) (string, string) {
	vars := previewVars(allowed)
	sub, err := renderText(subject, allowed, vars)
	if err != nil {
		sub = subject
	}
	b, err := renderText(body, allowed, vars)
	if err != nil {
		b = body
	}
	return sub, b
}

func previewVars(allowed []string) map[string]string {
	vars := make(map[string]string, len(allowed))
	for _, name := range allowed {
		if v, ok := sampleVars[name]; ok {
			vars[name] = v
		} else {
			vars[name] = "{{" + name + "}}"
		}
	}
	return vars
}

// validateTemplateContent 校验并规范化模板主题与正文。
func validateTemplateContent(subject, body string, allowed []string) (string, string, error) {
	subject = strings.TrimSpace(subject)
	body = strings.TrimSpace(body)
	fields := map[string]string{}

	checkField := func(key, s string, isSubject bool) {
		if _, ok := fields[key]; ok {
			return
		}
		if strings.ContainsRune(s, 0) {
			fields[key] = "不能包含空字符"
			return
		}
		if isSubject {
			if subject == "" || utf8.RuneCountInString(subject) > 200 {
				fields[key] = "主题必填，且不超过 200 字"
				return
			}
			if strings.ContainsAny(subject, "\r\n") {
				fields[key] = "主题不能换行"
				return
			}
		} else {
			if body == "" || utf8.RuneCountInString(body) > 20000 {
				fields[key] = "正文必填，且不超过 20000 字"
				return
			}
		}
		_, err := templateVars(s)
		if err != nil {
			fields[key] = errTemplateSyntax.Error()
			return
		}
		bad, err := unknownVariables(s, allowed)
		if err != nil {
			fields[key] = errTemplateSyntax.Error()
			return
		}
		if len(bad) > 0 {
			fields[key] = "用到了这个模板不提供的变量：" + strings.Join(bad, "、") + "。可用变量：" + strings.Join(allowed, "、")
		}
	}

	checkField("subject", subject, true)
	checkField("body", body, false)

	if len(fields) > 0 {
		return "", "", errs.Invalid(fields)
	}
	return subject, body, nil
}

// headerSafe 去掉主题里的换行，避免污染邮件头。
func headerSafe(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}
