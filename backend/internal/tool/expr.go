package tool

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// 递归下降的四则运算求值器。
//
// 为什么自己写而不引第三方表达式库：模型能碰的东西越少越好。一个 100 行的
// 解析器没有反射、没有变量作用域、没有函数注册表，攻击面就是一个字符一个字符
// 数得清的；换成通用求值库，等于把「任意表达式」都塞进模型手里。
//
// 语法（优先级从低到高）：
//
//	expr   := term (('+' | '-') term)*
//	term   := unary (('*' | '/' | '%') unary)*
//	unary  := ('+' | '-') unary | primary
//	primary:= number | '(' expr ')'
func evalExpr(input string) (float64, error) {
	p := &parser{src: input}
	v, err := p.parseExpr()
	if err != nil {
		return 0, err
	}
	p.skipSpaces()
	if !p.eof() {
		return 0, fmt.Errorf("第 %d 个字符起有多余内容：%q", p.pos+1, p.rest())
	}
	if math.IsNaN(v) {
		return 0, fmt.Errorf("结果不是有效数字（NaN）")
	}
	if math.IsInf(v, 0) {
		return 0, fmt.Errorf("结果超出可表示范围")
	}
	return v, nil
}

const maxExprDepth = 32

type parser struct {
	src   string
	pos   int
	depth int
}

func (p *parser) eof() bool { return p.pos >= len(p.src) }
func (p *parser) rest() string {
	if p.eof() {
		return ""
	}
	tail := p.src[p.pos:]
	if len(tail) > 20 {
		tail = tail[:20] + "..."
	}
	return tail
}

func (p *parser) skipSpaces() {
	for !p.eof() && (p.src[p.pos] == ' ' || p.src[p.pos] == '\t') {
		p.pos++
	}
}

func (p *parser) peek() byte {
	if p.eof() {
		return 0
	}
	return p.src[p.pos]
}

func (p *parser) parseExpr() (float64, error) {
	left, err := p.parseTerm()
	if err != nil {
		return 0, err
	}
	for {
		p.skipSpaces()
		op := p.peek()
		if op != '+' && op != '-' {
			return left, nil
		}
		p.pos++
		right, err := p.parseTerm()
		if err != nil {
			return 0, err
		}
		if op == '+' {
			left += right
		} else {
			left -= right
		}
	}
}

func (p *parser) parseTerm() (float64, error) {
	left, err := p.parseUnary()
	if err != nil {
		return 0, err
	}
	for {
		p.skipSpaces()
		op := p.peek()
		if op != '*' && op != '/' && op != '%' {
			return left, nil
		}
		p.pos++
		right, err := p.parseUnary()
		if err != nil {
			return 0, err
		}
		switch op {
		case '*':
			left *= right
		case '/':
			if right == 0 {
				// 除零要明确报错：静默返回 0 或 +Inf 会让模型把错数当真。
				return 0, fmt.Errorf("除数不能为 0")
			}
			left /= right
		case '%':
			if right == 0 {
				return 0, fmt.Errorf("取模的除数不能为 0")
			}
			left = math.Mod(left, right)
		}
	}
}

func (p *parser) parseUnary() (float64, error) {
	p.skipSpaces()
	switch p.peek() {
	case '+':
		p.pos++
		return p.parseUnary()
	case '-':
		p.pos++
		v, err := p.parseUnary()
		return -v, err
	}
	return p.parsePrimary()
}

func (p *parser) parsePrimary() (float64, error) {
	p.skipSpaces()
	if p.eof() {
		return 0, fmt.Errorf("表达式在第 %d 个字符处意外结束", p.pos+1)
	}

	if p.peek() == '(' {
		p.depth++
		if p.depth > maxExprDepth {
			return 0, fmt.Errorf("括号嵌套过深（上限 %d 层）", maxExprDepth)
		}
		p.pos++
		v, err := p.parseExpr()
		if err != nil {
			return 0, err
		}
		p.skipSpaces()
		if p.peek() != ')' {
			return 0, fmt.Errorf("第 %d 个字符处缺少右括号", p.pos+1)
		}
		p.pos++
		p.depth--
		return v, nil
	}

	start := p.pos
	dotSeen := false
	for !p.eof() {
		c := p.src[p.pos]
		if c >= '0' && c <= '9' {
			p.pos++
			continue
		}
		if c == '.' && !dotSeen {
			dotSeen = true
			p.pos++
			continue
		}
		break
	}
	if start == p.pos {
		return 0, fmt.Errorf("第 %d 个字符 %q 无法识别（只支持数字、括号与 + - * / %%）", p.pos+1, string(p.peek()))
	}
	text := p.src[start:p.pos]
	v, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return 0, fmt.Errorf("数字 %q 不合法", text)
	}
	return v, nil
}

// formatNumber 去掉浮点尾巴（0.30000000000000004 → 0.3 不必强行修，-0 要修）。
func formatNumber(v float64) string {
	if v == 0 {
		return "0" // 避免输出 -0
	}
	out := strconv.FormatFloat(v, 'f', -1, 64)
	// 超大/超小值用科学计数法，否则会打出一长串 0。
	if math.Abs(v) >= 1e15 || (math.Abs(v) < 1e-9 && v != 0) {
		out = strconv.FormatFloat(v, 'g', -1, 64)
	}
	return strings.TrimSuffix(out, ".0")
}
