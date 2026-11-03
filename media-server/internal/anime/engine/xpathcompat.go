package engine

import "strings"

// Kazumi 用的 Dart 库 xpath_selector 在谓词里认 CSS 式的属性匹配（如 TvTFun 的
// //ol[@class*='tvt-watch-episodes']），我们用的 antchfx/xpath 不认：它把 * 当乘号，
// 接着遇到 = 就报"expression must evaluate to a node-set"。
// compatXPath 在表达式交给 XPath 库之前把这几种写法改成等价的标准 XPath：
//
//	[@a*='v'] → contains(@a, 'v')                                   属性值包含 v
//	[@a^='v'] → starts-with(@a, 'v')                                以 v 开头
//	[@a$='v'] → ends-with(@a, 'v')                                  以 v 结尾
//	[@a~='v'] → contains(concat(' ', normalize-space(@a), ' '), ' v ') 按空白拆开后有 v 这一项
//	[@a|='v'] → (@a = 'v' or starts-with(@a, 'v-'))                 等于 v，或以 "v-" 开头
//
// 只改运行时用的表达式，规则原文不动。这几种写法在标准 XPath 里都是语法错误，改写不会改变任何
// 本来就合法的表达式的意思；为了不误伤，只认"@属性名 + 运算符 + 带引号的值"、并且 @ 前面紧挨着
// [ ( , 或 and/or 的情况，引号里的字原样跳过。值为空时按 CSS 的规定什么都不匹配。
func compatXPath(expr string) string {
	if !strings.ContainsAny(expr, "*^$~|") || !strings.Contains(expr, "@") {
		return expr
	}
	var b strings.Builder
	for i := 0; i < len(expr); {
		c := expr[i]
		if c == '\'' || c == '"' {
			end := strings.IndexByte(expr[i+1:], c)
			if end < 0 { // 引号没收尾：原样留着，让 XPath 库自己报错
				b.WriteString(expr[i:])
				break
			}
			b.WriteString(expr[i : i+end+2])
			i += end + 2
			continue
		}
		if c == '@' && operandStart(b.String()) {
			if out, n, ok := rewriteAttrMatch(expr[i:]); ok {
				b.WriteString(out)
				i += n
				continue
			}
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

// rewriteAttrMatch s 以 @ 开头：是"@属性 运算符 '值'"就返回改写结果和吃掉的长度
func rewriteAttrMatch(s string) (string, int, bool) {
	i := 1
	if i >= len(s) || !isXPathNameStart(s[i]) {
		return "", 0, false
	}
	for i < len(s) && isXPathNameChar(s[i]) {
		i++
	}
	attr := s[:i]
	i = skipXPathSpace(s, i)
	if i+1 >= len(s) || s[i+1] != '=' || !strings.ContainsRune("*^$~|", rune(s[i])) {
		return "", 0, false
	}
	op := s[i]
	i = skipXPathSpace(s, i+2)
	if i >= len(s) || (s[i] != '\'' && s[i] != '"') {
		return "", 0, false
	}
	q := s[i]
	end := strings.IndexByte(s[i+1:], q)
	if end < 0 {
		return "", 0, false
	}
	val := s[i+1 : i+1+end]
	n := i + end + 2
	lit := func(v string) string { return string(q) + v + string(q) } // 值里不会有同一种引号，原样套回去
	var out string
	switch op {
	case '*':
		out = "contains(" + attr + ", " + lit(val) + ")"
	case '^':
		out = "starts-with(" + attr + ", " + lit(val) + ")"
	case '$':
		out = "ends-with(" + attr + ", " + lit(val) + ")"
	case '~':
		if strings.ContainsAny(val, " \t\r\n\f") {
			return "false()", n, true
		}
		out = "contains(concat(' ', normalize-space(" + attr + "), ' '), " + lit(" "+val+" ") + ")"
	case '|':
		return "(" + attr + " = " + lit(val) + " or starts-with(" + attr + ", " + lit(val+"-") + "))", n, true
	}
	if val == "" {
		return "false()", n, true
	}
	return out, n, true
}

// operandStart 已经写出的部分末尾是不是一个"可以开始新条件"的位置：[ ( , 或者 and / or 这两个词
func operandStart(prev string) bool {
	p := strings.TrimRight(prev, " \t\r\n")
	if p == "" {
		return false
	}
	switch p[len(p)-1] {
	case '[', '(', ',':
		return true
	}
	if len(p) == len(prev) { // and/or 后面必须隔着空白（"and@x" 不是这个意思）
		return false
	}
	for _, w := range []string{"and", "or"} {
		if strings.HasSuffix(p, w) {
			rest := p[:len(p)-len(w)]
			if rest == "" || !isXPathNameChar(rest[len(rest)-1]) {
				return rest != ""
			}
		}
	}
	return false
}

func skipXPathSpace(s string, i int) int {
	for i < len(s) && strings.IndexByte(" \t\r\n", s[i]) >= 0 {
		i++
	}
	return i
}

// 属性名：字母、下划线或非 ASCII 开头，后面还可以有数字、- . 和命名空间的 :
func isXPathNameStart(c byte) bool {
	return c == '_' || c >= 0x80 || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isXPathNameChar(c byte) bool {
	return isXPathNameStart(c) || (c >= '0' && c <= '9') || c == '-' || c == '.' || c == ':'
}
