package service

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

type MetaProgram struct {
	Options []MetaOption `json:"options"`
	Root    MetaAction   `json:"root"`
}

type MetaOption struct {
	Name  string    `json:"name"`
	Value MetaValue `json:"value"`
}

type MetaActionKind string

const (
	MetaActionCall     MetaActionKind = "call"
	MetaActionRoute    MetaActionKind = "route"
	MetaActionSwitch   MetaActionKind = "switch"
	MetaActionParallel MetaActionKind = "parallel"
	MetaActionJudge    MetaActionKind = "judge"
)

type MetaAction struct {
	Kind            MetaActionKind `json:"kind"`
	Model           string         `json:"model,omitempty"`
	Prompt          string         `json:"prompt,omitempty"`
	Routes          []MetaRoute    `json:"routes,omitempty"`
	Switches        []MetaSwitch   `json:"switches,omitempty"`
	Calls           []MetaAction   `json:"calls,omitempty"`
	SynthesizeModel string         `json:"synthesize_model,omitempty"`
}

type MetaRoute struct {
	Condition *MetaExpression `json:"condition,omitempty"`
	Otherwise bool            `json:"otherwise,omitempty"`
	Action    MetaAction      `json:"action"`
}

type MetaSwitch struct {
	Weight    float64    `json:"weight,omitempty"`
	Otherwise bool       `json:"otherwise,omitempty"`
	Action    MetaAction `json:"action"`
}

type MetaExpression struct {
	Left     string    `json:"left"`
	Operator string    `json:"operator"`
	Right    MetaValue `json:"right"`
}

type MetaValueKind string

const (
	MetaValueString MetaValueKind = "string"
	MetaValueNumber MetaValueKind = "number"
	MetaValueBool   MetaValueKind = "bool"
)

type MetaValue struct {
	Kind   MetaValueKind `json:"kind"`
	String string        `json:"string,omitempty"`
	Number float64       `json:"number,omitempty"`
	Bool   bool          `json:"bool,omitempty"`
}

func ParseMetaModuleDSL(source string) (MetaProgram, error) {
	tokens, err := lexMetaDSL(source)
	if err != nil {
		return MetaProgram{}, err
	}
	parser := metaDSLParser{tokens: tokens}
	program, err := parser.parseProgram()
	if err != nil {
		return MetaProgram{}, err
	}
	if !parser.match(metaTokenEOF, "") {
		return MetaProgram{}, parser.errorf("unexpected token %q", parser.peek().value)
	}
	return program, nil
}

type metaTokenKind int

const (
	metaTokenEOF metaTokenKind = iota
	metaTokenIdent
	metaTokenString
	metaTokenNumber
	metaTokenBool
	metaTokenSymbol
	metaTokenOperator
	metaTokenArrow
)

type metaToken struct {
	kind  metaTokenKind
	value string
	line  int
	col   int
}

func lexMetaDSL(source string) ([]metaToken, error) {
	lexer := metaDSLLexer{input: []rune(source), line: 1, col: 1}
	tokens := []metaToken{}
	for {
		token, err := lexer.next()
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, token)
		if token.kind == metaTokenEOF {
			return tokens, nil
		}
	}
}

type metaDSLLexer struct {
	input []rune
	pos   int
	line  int
	col   int
}

func (lexer *metaDSLLexer) next() (metaToken, error) {
	lexer.skipWhitespaceAndComments()
	startLine, startCol := lexer.line, lexer.col
	r := lexer.peek()
	if r == 0 {
		return metaToken{kind: metaTokenEOF, line: startLine, col: startCol}, nil
	}
	if isMetaIdentStart(r) {
		value := lexer.readIdent()
		switch value {
		case "true", "false":
			return metaToken{kind: metaTokenBool, value: value, line: startLine, col: startCol}, nil
		default:
			return metaToken{kind: metaTokenIdent, value: value, line: startLine, col: startCol}, nil
		}
	}
	if unicode.IsDigit(r) {
		return metaToken{kind: metaTokenNumber, value: lexer.readNumber(), line: startLine, col: startCol}, nil
	}
	if r == '"' {
		value, err := lexer.readString()
		if err != nil {
			return metaToken{}, err
		}
		return metaToken{kind: metaTokenString, value: value, line: startLine, col: startCol}, nil
	}
	if r == '=' && lexer.peekN(1) == '>' {
		lexer.advance()
		lexer.advance()
		return metaToken{kind: metaTokenArrow, value: "=>", line: startLine, col: startCol}, nil
	}
	if r == '=' {
		lexer.advance()
		if lexer.peek() == '=' {
			lexer.advance()
			return metaToken{kind: metaTokenOperator, value: "==", line: startLine, col: startCol}, nil
		}
		return metaToken{kind: metaTokenSymbol, value: "=", line: startLine, col: startCol}, nil
	}
	if strings.ContainsRune("{}();,", r) {
		lexer.advance()
		return metaToken{kind: metaTokenSymbol, value: string(r), line: startLine, col: startCol}, nil
	}
	if strings.ContainsRune("!<>", r) {
		value := string(r)
		lexer.advance()
		if lexer.peek() == '=' {
			value += "="
			lexer.advance()
		}
		return metaToken{kind: metaTokenOperator, value: value, line: startLine, col: startCol}, nil
	}
	return metaToken{}, fmt.Errorf("line %d:%d: unexpected character %q", startLine, startCol, r)
}

func (lexer *metaDSLLexer) skipWhitespaceAndComments() {
	for {
		for unicode.IsSpace(lexer.peek()) {
			lexer.advance()
		}
		if lexer.peek() != '#' {
			return
		}
		for lexer.peek() != 0 && lexer.peek() != '\n' {
			lexer.advance()
		}
	}
}

func (lexer *metaDSLLexer) readIdent() string {
	start := lexer.pos
	for isMetaIdentPart(lexer.peek()) {
		lexer.advance()
	}
	return string(lexer.input[start:lexer.pos])
}

func (lexer *metaDSLLexer) readNumber() string {
	start := lexer.pos
	for unicode.IsDigit(lexer.peek()) {
		lexer.advance()
	}
	if lexer.peek() == '.' && unicode.IsDigit(lexer.peekN(1)) {
		lexer.advance()
		for unicode.IsDigit(lexer.peek()) {
			lexer.advance()
		}
	}
	return string(lexer.input[start:lexer.pos])
}

func (lexer *metaDSLLexer) readString() (string, error) {
	startLine, startCol := lexer.line, lexer.col
	lexer.advance()
	var builder strings.Builder
	for {
		r := lexer.peek()
		if r == 0 || r == '\n' {
			return "", fmt.Errorf("line %d:%d: unterminated string", startLine, startCol)
		}
		lexer.advance()
		if r == '"' {
			return builder.String(), nil
		}
		if r != '\\' {
			builder.WriteRune(r)
			continue
		}
		escaped := lexer.peek()
		if escaped == 0 {
			return "", fmt.Errorf("line %d:%d: unterminated string escape", startLine, startCol)
		}
		lexer.advance()
		switch escaped {
		case '"', '\\':
			builder.WriteRune(escaped)
		case 'n':
			builder.WriteRune('\n')
		case 'r':
			builder.WriteRune('\r')
		case 't':
			builder.WriteRune('\t')
		default:
			return "", fmt.Errorf("line %d:%d: unsupported string escape \\%c", lexer.line, lexer.col, escaped)
		}
	}
}

func (lexer *metaDSLLexer) peek() rune {
	return lexer.peekN(0)
}

func (lexer *metaDSLLexer) peekN(offset int) rune {
	if lexer.pos+offset >= len(lexer.input) {
		return 0
	}
	return lexer.input[lexer.pos+offset]
}

func (lexer *metaDSLLexer) advance() {
	if lexer.pos >= len(lexer.input) {
		return
	}
	if lexer.input[lexer.pos] == '\n' {
		lexer.line++
		lexer.col = 1
	} else {
		lexer.col++
	}
	lexer.pos++
}

func isMetaIdentStart(r rune) bool {
	return unicode.IsLetter(r) || r == '_'
}

func isMetaIdentPart(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '.'
}

type metaDSLParser struct {
	tokens []metaToken
	pos    int
}

func (parser *metaDSLParser) parseProgram() (MetaProgram, error) {
	program := MetaProgram{}
	for parser.match(metaTokenIdent, "option") {
		option, err := parser.parseOption()
		if err != nil {
			return MetaProgram{}, err
		}
		program.Options = append(program.Options, option)
		parser.consumeSeparators()
	}
	action, err := parser.parseAction()
	if err != nil {
		return MetaProgram{}, err
	}
	program.Root = action
	parser.consumeSeparators()
	return program, nil
}

func (parser *metaDSLParser) parseOption() (MetaOption, error) {
	name, err := parser.expect(metaTokenIdent, "")
	if err != nil {
		return MetaOption{}, err
	}
	if _, err := parser.expectSymbol("="); err != nil {
		return MetaOption{}, err
	}
	value, err := parser.parseLiteral()
	if err != nil {
		return MetaOption{}, err
	}
	return MetaOption{Name: name.value, Value: value}, nil
}

func (parser *metaDSLParser) parseAction() (MetaAction, error) {
	token, err := parser.expect(metaTokenIdent, "")
	if err != nil {
		return MetaAction{}, err
	}
	switch token.value {
	case "call":
		model, err := parser.expect(metaTokenString, "")
		if err != nil {
			return MetaAction{}, err
		}
		return MetaAction{Kind: MetaActionCall, Model: model.value}, nil
	case "route":
		return parser.parseRoute()
	case "switch":
		return parser.parseSwitch()
	case "parallel":
		return parser.parseParallel()
	case "judge":
		return parser.parseJudge()
	default:
		return MetaAction{}, parser.errorAt(token, "expected action: call, route, switch, parallel, or judge")
	}
}

func (parser *metaDSLParser) parseRoute() (MetaAction, error) {
	if _, err := parser.expectSymbol("{"); err != nil {
		return MetaAction{}, err
	}
	action := MetaAction{Kind: MetaActionRoute}
	seenOtherwise := false
	for !parser.match(metaTokenSymbol, "}") {
		if parser.match(metaTokenEOF, "") {
			return MetaAction{}, parser.errorf("unterminated route block")
		}
		branchStart, err := parser.expect(metaTokenIdent, "")
		if err != nil {
			return MetaAction{}, err
		}
		switch branchStart.value {
		case "when":
			if seenOtherwise {
				return MetaAction{}, parser.errorAt(branchStart, "when branch cannot follow otherwise")
			}
			expr, err := parser.parseExpression()
			if err != nil {
				return MetaAction{}, err
			}
			if _, err := parser.expect(metaTokenArrow, "=>"); err != nil {
				return MetaAction{}, err
			}
			branchAction, err := parser.parseAction()
			if err != nil {
				return MetaAction{}, err
			}
			action.Routes = append(action.Routes, MetaRoute{Condition: &expr, Action: branchAction})
		case "otherwise":
			if seenOtherwise {
				return MetaAction{}, parser.errorAt(branchStart, "duplicate otherwise branch")
			}
			seenOtherwise = true
			if _, err := parser.expect(metaTokenArrow, "=>"); err != nil {
				return MetaAction{}, err
			}
			branchAction, err := parser.parseAction()
			if err != nil {
				return MetaAction{}, err
			}
			action.Routes = append(action.Routes, MetaRoute{Otherwise: true, Action: branchAction})
		default:
			return MetaAction{}, parser.errorAt(branchStart, "expected when or otherwise")
		}
		parser.consumeSeparators()
	}
	if !seenOtherwise {
		return MetaAction{}, parser.errorf("route requires an otherwise branch")
	}
	return action, nil
}

func (parser *metaDSLParser) parseSwitch() (MetaAction, error) {
	if _, err := parser.expectSymbol("{"); err != nil {
		return MetaAction{}, err
	}
	action := MetaAction{Kind: MetaActionSwitch}
	seenOtherwise := false
	weightedBranches := 0
	for !parser.match(metaTokenSymbol, "}") {
		if parser.match(metaTokenEOF, "") {
			return MetaAction{}, parser.errorf("unterminated switch block")
		}
		branchStart, err := parser.expect(metaTokenIdent, "")
		if err != nil {
			return MetaAction{}, err
		}
		switch branchStart.value {
		case "weight", "chance", "probability":
			if seenOtherwise {
				return MetaAction{}, parser.errorAt(branchStart, "weighted branch cannot follow otherwise")
			}
			weightToken, err := parser.expect(metaTokenNumber, "")
			if err != nil {
				return MetaAction{}, err
			}
			weight, err := strconv.ParseFloat(weightToken.value, 64)
			if err != nil || weight <= 0 {
				return MetaAction{}, parser.errorAt(weightToken, "switch weight must be a positive number")
			}
			if _, err := parser.expect(metaTokenArrow, "=>"); err != nil {
				return MetaAction{}, err
			}
			branchAction, err := parser.parseAction()
			if err != nil {
				return MetaAction{}, err
			}
			action.Switches = append(action.Switches, MetaSwitch{Weight: weight, Action: branchAction})
			weightedBranches++
		case "otherwise":
			if seenOtherwise {
				return MetaAction{}, parser.errorAt(branchStart, "duplicate otherwise branch")
			}
			seenOtherwise = true
			if _, err := parser.expect(metaTokenArrow, "=>"); err != nil {
				return MetaAction{}, err
			}
			branchAction, err := parser.parseAction()
			if err != nil {
				return MetaAction{}, err
			}
			action.Switches = append(action.Switches, MetaSwitch{Otherwise: true, Action: branchAction})
		default:
			return MetaAction{}, parser.errorAt(branchStart, "expected weight, chance, probability, or otherwise")
		}
		parser.consumeSeparators()
	}
	if weightedBranches == 0 {
		return MetaAction{}, parser.errorf("switch requires at least one weighted branch")
	}
	return action, nil
}

func (parser *metaDSLParser) parseParallel() (MetaAction, error) {
	if _, err := parser.expectSymbol("{"); err != nil {
		return MetaAction{}, err
	}
	action := MetaAction{Kind: MetaActionParallel}
	for !parser.match(metaTokenSymbol, "}") {
		if parser.match(metaTokenEOF, "") {
			return MetaAction{}, parser.errorf("unterminated parallel block")
		}
		call, err := parser.parseAction()
		if err != nil {
			return MetaAction{}, err
		}
		if call.Kind != MetaActionCall {
			return MetaAction{}, parser.errorf("parallel currently accepts call actions only")
		}
		action.Calls = append(action.Calls, call)
		parser.consumeSeparators()
	}
	if len(action.Calls) == 0 {
		return MetaAction{}, parser.errorf("parallel requires at least one call")
	}
	if parser.match(metaTokenIdent, "synthesize") {
		model, err := parser.expect(metaTokenString, "")
		if err != nil {
			return MetaAction{}, err
		}
		action.SynthesizeModel = model.value
	}
	return action, nil
}

func (parser *metaDSLParser) parseJudge() (MetaAction, error) {
	model, err := parser.expect(metaTokenString, "")
	if err != nil {
		return MetaAction{}, err
	}
	if _, err := parser.expectSymbol("{"); err != nil {
		return MetaAction{}, err
	}
	action := MetaAction{Kind: MetaActionJudge, Model: model.value}
	parser.consumeSeparators()
	if parser.match(metaTokenIdent, "prompt") {
		prompt, err := parser.expect(metaTokenString, "")
		if err != nil {
			return MetaAction{}, err
		}
		action.Prompt = prompt.value
		parser.consumeSeparators()
	}
	routeToken, err := parser.expect(metaTokenIdent, "route")
	if err != nil {
		return MetaAction{}, err
	}
	_ = routeToken
	route, err := parser.parseRoute()
	if err != nil {
		return MetaAction{}, err
	}
	action.Routes = route.Routes
	parser.consumeSeparators()
	if _, err := parser.expectSymbol("}"); err != nil {
		return MetaAction{}, err
	}
	return action, nil
}

func (parser *metaDSLParser) parseExpression() (MetaExpression, error) {
	left, err := parser.expect(metaTokenIdent, "")
	if err != nil {
		return MetaExpression{}, err
	}
	operator, err := parser.parseExpressionOperator()
	if err != nil {
		return MetaExpression{}, err
	}
	switch operator {
	case "==", "!=", "<", "<=", ">", ">=", "contains", "not_contains", "starts_with", "ends_with", "matches":
	default:
		return MetaExpression{}, parser.errorf("unsupported operator %q", operator)
	}
	right, err := parser.parseLiteral()
	if err != nil {
		return MetaExpression{}, err
	}
	return MetaExpression{Left: left.value, Operator: operator, Right: right}, nil
}

func (parser *metaDSLParser) parseExpressionOperator() (string, error) {
	token := parser.peek()
	if token.kind != metaTokenOperator && token.kind != metaTokenIdent {
		return "", parser.errorAt(token, "expected operator")
	}
	parser.pos++
	return token.value, nil
}

func (parser *metaDSLParser) parseLiteral() (MetaValue, error) {
	token := parser.peek()
	switch token.kind {
	case metaTokenString:
		parser.pos++
		return MetaValue{Kind: MetaValueString, String: token.value}, nil
	case metaTokenNumber:
		parser.pos++
		value, err := strconv.ParseFloat(token.value, 64)
		if err != nil {
			return MetaValue{}, parser.errorAt(token, "invalid number")
		}
		return MetaValue{Kind: MetaValueNumber, Number: value}, nil
	case metaTokenBool:
		parser.pos++
		return MetaValue{Kind: MetaValueBool, Bool: token.value == "true"}, nil
	default:
		return MetaValue{}, parser.errorAt(token, "expected string, number, or boolean literal")
	}
}

func (parser *metaDSLParser) consumeSeparators() {
	for parser.match(metaTokenSymbol, ";") || parser.match(metaTokenSymbol, ",") {
	}
}

func (parser *metaDSLParser) expect(kind metaTokenKind, value string) (metaToken, error) {
	token := parser.peek()
	if token.kind != kind || (value != "" && token.value != value) {
		expected := value
		if expected == "" {
			expected = metaTokenKindName(kind)
		}
		return metaToken{}, parser.errorAt(token, "expected %s", expected)
	}
	parser.pos++
	return token, nil
}

func (parser *metaDSLParser) expectSymbol(value string) (metaToken, error) {
	token := parser.peek()
	if token.kind == metaTokenSymbol && token.value == value {
		parser.pos++
		return token, nil
	}
	return metaToken{}, parser.errorAt(token, "expected %s", value)
}

func (parser *metaDSLParser) match(kind metaTokenKind, value string) bool {
	token := parser.peek()
	if token.kind != kind || (value != "" && token.value != value) {
		return false
	}
	parser.pos++
	return true
}

func (parser *metaDSLParser) peek() metaToken {
	if parser.pos >= len(parser.tokens) {
		return metaToken{kind: metaTokenEOF}
	}
	return parser.tokens[parser.pos]
}

func (parser *metaDSLParser) errorf(format string, args ...interface{}) error {
	return parser.errorAt(parser.peek(), format, args...)
}

func (parser *metaDSLParser) errorAt(token metaToken, format string, args ...interface{}) error {
	return fmt.Errorf("line %d:%d: %s", token.line, token.col, fmt.Sprintf(format, args...))
}

func metaTokenKindName(kind metaTokenKind) string {
	switch kind {
	case metaTokenIdent:
		return "identifier"
	case metaTokenString:
		return "string"
	case metaTokenNumber:
		return "number"
	case metaTokenBool:
		return "boolean"
	case metaTokenSymbol:
		return "symbol"
	case metaTokenOperator:
		return "operator"
	case metaTokenArrow:
		return "=>"
	default:
		return "end of input"
	}
}
