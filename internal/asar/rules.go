package asar

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
)

const identifier = `[A-Za-z_$][A-Za-z0-9_$]*`

var (
	savedGuard            = regexp.MustCompile(`\{try\{if\((!await (` + identifier + `)\(\)|!1)`)
	savedDefinition       = regexp.MustCompile(`^async function(?: ` + identifier + `)?\(` + identifier + `,` + identifier + `,` + identifier + `,(` + identifier + `)\)`)
	authDenied            = regexp.MustCompile(`(` + identifier + ")!==`chatgpt`&&(" + identifier + ")!==`personalAccessToken`")
	authDeniedPatched     = regexp.MustCompile(`!/\^\(chatgpt\|personalAccessToken\|apikey\)\$/\.test\((` + identifier + `)\)`)
	authDeniedJSON        = regexp.MustCompile(`"chatgpt"!==(` + identifier + `)&&"personalAccessToken"!==(` + identifier + `)&&"amazonBedrock"!==(` + identifier + `)`)
	authDeniedJSONPatched = regexp.MustCompile(`!/\^\(chatgpt\|personalAccessToken\|apikey\|amazonBedrock\)\$/\.test\((` + identifier + `)\)`)
	accessAuth            = regexp.MustCompile(`(` + identifier + ")\\?\\.authMethod===`chatgpt`\\|\\|(" + identifier + ")\\?\\.authMethod===`personalAccessToken`")
	accessAuthPatched     = regexp.MustCompile(`/\^\(chatgpt\|personalAccessToken\|apikey\)\$/\.test\((` + identifier + `)\?\.authMethod\)`)
	awaitCall             = regexp.MustCompile(`await (` + identifier + `)\(`)
	rspackModule          = regexp.MustCompile(`(?:__webpack_modules__=\{|\},)(` + identifier + `)\((` + identifier + `),(` + identifier + `),(` + identifier + `)\)\{`)
	functionHeader        = regexp.MustCompile(`(?:async )?function(?: ` + identifier + `)?\(([^{}()]*)\)\{`)
	importedAuthCall      = regexp.MustCompile(`let (` + identifier + `)=await\s*\(0,(` + identifier + `)\.(` + identifier + `)\)\(`)
)

type rspackReference struct {
	module, export string
}

type rspackScope struct {
	id, exports, require string
	start, end           int
}

type ruleReport struct {
	ui, readers, policies, tierMenus, tierLookups int
	tierMenusRspack                               bool
	effortQueries                                 int
	policyModule, effortModule                    string
	accountReferences                             []rspackReference
	patched                                       int
	edits                                         []Candidate
}

func (r *ruleReport) add(entry Entry, data []byte, start, end int, replacement string) {
	original := append([]byte(nil), data[start:end]...)
	repl := []byte(replacement)
	if len(repl) < len(original) {
		repl = append(repl, bytes.Repeat([]byte{' '}, len(original)-len(repl))...)
	}
	r.edits = append(r.edits, Candidate{Entry: entry, MatchStart: start, MatchEnd: end, Original: original, Replacement: repl})
}

// These markers are emitted by the packaged desktop functions. Restrict each
// rule to its logged operation; matching a generic authentication check is not
// sufficient evidence to modify it.
func functionRegion(data []byte, marker string) (int, int, error) {
	i := bytes.Index(data, []byte(marker))
	if i < 0 || bytes.Count(data, []byte(marker)) != 1 {
		return 0, 0, fmt.Errorf("non-unique function marker %q", marker)
	}
	start, end, ok := enclosingFunction(data, i, true)
	if !ok {
		return 0, 0, fmt.Errorf("unsupported function layout for %q", marker)
	}
	return start, end, nil
}

// Only inspect the bounded function containing an operation marker. This
// includes inline functions without making a whole constructor their scope.
func enclosingFunction(data []byte, position int, asyncOnly bool) (int, int, bool) {
	start := max(0, position-10000)
	headers := functionHeader.FindAllSubmatchIndex(data[start:position], -1)
	for i := len(headers) - 1; i >= 0; i-- {
		header := headers[i]
		functionStart := start + header[0]
		if asyncOnly && (!bytes.HasPrefix(data[functionStart:], []byte("async function")) ||
			bytes.Count(data[start+header[2]:start+header[3]], []byte(",")) == 0) {
			continue
		}
		end, ok := javascriptBlockEnd(data, start+header[1]-1, min(len(data), functionStart+10000), 0)
		if ok && end > position {
			return functionStart, end, true
		}
	}
	return 0, 0, false
}

func javascriptBlockEnd(data []byte, start, limit, nesting int) (int, bool) {
	if nesting > 64 || start >= limit || data[start] != '{' {
		return 0, false
	}
	depth, regexAllowed := 0, true
	for i := start; i < limit; {
		c := data[i]
		switch {
		case c == '"' || c == '\'' || c == '`':
			quote := c
			i++
			closed := false
			for i < limit {
				if data[i] == '\\' {
					i += 2
				} else if data[i] == quote {
					i++
					closed = true
					break
				} else if quote == '`' && data[i] == '$' && i+1 < limit && data[i+1] == '{' {
					end, ok := javascriptBlockEnd(data, i+1, limit, nesting+1)
					if !ok {
						return 0, false
					}
					i = end
				} else {
					i++
				}
			}
			if !closed {
				return 0, false
			}
			regexAllowed = false
		case c == '/' && i+1 < limit && data[i+1] == '/':
			i += 2
			for i < limit && data[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < limit && data[i+1] == '*':
			end := bytes.Index(data[i+2:limit], []byte("*/"))
			if end < 0 {
				return 0, false
			}
			i += end + 4
		case c == '/' && regexAllowed:
			i++
			inClass, closed := false, false
			for i < limit {
				if data[i] == '\\' {
					i += 2
					continue
				}
				if data[i] == '[' {
					inClass = true
				} else if data[i] == ']' {
					inClass = false
				} else if data[i] == '/' && !inClass {
					i++
					closed = true
					break
				} else if data[i] == '\n' {
					return 0, false
				}
				i++
			}
			if !closed {
				return 0, false
			}
			regexAllowed = false
		case c == '{':
			depth++
			regexAllowed = true
			i++
		case c == '}':
			depth--
			i++
			if depth == 0 {
				return i, true
			}
			regexAllowed = false
		case c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c == '_' || c == '$':
			j := i + 1
			for j < limit && (data[j] >= 'A' && data[j] <= 'Z' || data[j] >= 'a' && data[j] <= 'z' ||
				data[j] >= '0' && data[j] <= '9' || data[j] == '_' || data[j] == '$') {
				j++
			}
			switch string(data[i:j]) {
			case "return", "throw", "case", "delete", "void", "typeof", "new", "in", "instanceof", "yield", "await", "else", "do":
				regexAllowed = true
			default:
				regexAllowed = false
			}
			i = j
		default:
			if c != ' ' && c != '\n' && c != '\r' && c != '\t' {
				regexAllowed = strings.ContainsRune("([=,:;!?&|+-*%^~<>/", rune(c))
			}
			i++
		}
	}
	return 0, false
}

func nextDeclaration(data []byte, start int) int {
	end := len(data)
	for _, delimiter := range []string{"}async function ", "}function ", "}var "} {
		if i := bytes.Index(data[start:], []byte(delimiter)); i >= 0 && start+i+1 < end {
			end = start + i + 1
		}
	}
	return end
}

// Rspack places many CommonJS modules in one JavaScript asset. Function names
// are only unique inside a module, so uniqueness checks must use that module's
// span instead of the whole asset. Legacy assets without module wrappers keep
// the complete asset as their scope.
func rspackModuleSpan(data []byte, position int) (int, int) {
	for _, module := range rspackScopes(data) {
		if module.start <= position && position < module.end {
			return module.start, module.end
		}
	}
	return 0, len(data)
}

func rspackScopes(data []byte) []rspackScope {
	if !bytes.Contains(data, []byte("__webpack_modules__=")) {
		return nil
	}
	matches := rspackModule.FindAllSubmatchIndex(data, -1)
	modules := make([]rspackScope, 0, len(matches))
	for i, match := range matches {
		end := len(data)
		if i+1 < len(matches) {
			end = matches[i+1][0] + 1
		}
		modules = append(modules, rspackScope{
			id: string(data[match[2]:match[3]]), exports: string(data[match[6]:match[7]]),
			require: string(data[match[8]:match[9]]), start: match[2], end: end,
		})
	}
	return modules
}

func rspackScopeAt(data []byte, position int) rspackScope {
	for _, module := range rspackScopes(data) {
		if module.start <= position && position < module.end {
			return module
		}
	}
	return rspackScope{start: 0, end: len(data)}
}

func scanRules(entry Entry, data []byte) (ruleReport, error) {
	var r ruleReport
	for _, m := range gatePattern.FindAllSubmatchIndex(data, -1) {
		if !bytes.Equal(data[m[6]:m[7]], data[m[8]:m[9]]) {
			continue
		}
		auth, loading, value := string(data[m[2]:m[3]]), string(data[m[4]:m[5]]), string(data[m[6]:m[7]])
		r.ui++
		r.add(entry, data, m[0], m[1], fmt.Sprintf("!%s&&(!%s||!!%s&&%s?.requirements?.featureRequirements?.fast_mode!==!1)", loading, auth, value, value))
	}
	count := len(patchedGatePattern.FindAllIndex(data, -1)) + len(previousPatchedGatePattern.FindAllIndex(data, -1))
	r.ui += count
	r.patched += count

	access, err := scanSpeedAccess(entry, data)
	if err != nil {
		return r, err
	}
	r.ui += access.ui
	r.patched += access.patched
	r.edits = append(r.edits, access.edits...)

	if bytes.Contains(data, []byte("Failed to read saved service tier for request")) {
		start, end, err := functionRegion(data, "Failed to read saved service tier for request")
		if err != nil {
			return r, err
		}
		region := data[start:end]
		if !bytes.Contains(region, []byte("config/read")) || !bytes.Contains(region, []byte("configRequirements/read")) || !bytes.Contains(region, []byte("service_tier")) {
			return r, fmt.Errorf("unsupported saved-tier reader in %s", entry.Path)
		}
		m := savedGuard.FindSubmatchIndex(region)
		if m == nil {
			return r, fmt.Errorf("saved-tier guard missing in %s", entry.Path)
		}
		definition := savedDefinition.FindSubmatch(region)
		if definition == nil || m[4] >= 0 && !bytes.Equal(definition[1], region[m[4]:m[5]]) {
			return r, fmt.Errorf("saved-tier authentication callback differs in %s", entry.Path)
		}
		r.readers++
		if string(region[m[2]:m[3]]) == "!1" {
			r.patched++
		} else {
			r.add(entry, data, start+m[2], start+m[3], "!1")
		}
	}

	if bytes.Contains(data, []byte("Failed to read service tier for request")) {
		marker := bytes.Index(data, []byte("Failed to read service tier for request"))
		module := rspackScopeAt(data, marker)
		scopeStart, scopeEnd := module.start, module.end
		start, end, err := functionRegion(data, "Failed to read service tier for request")
		if err != nil {
			return r, err
		}
		region := data[start:end]
		call := awaitCall.FindSubmatch(region)
		if !bytes.Contains(region, []byte("service_tier")) {
			return r, fmt.Errorf("request-tier policy call missing in %s", entry.Path)
		}
		helperStart, helperEnd := start, end
		if call != nil {
			declaration := []byte("async function " + string(call[1]) + "(")
			scope := data[scopeStart:scopeEnd]
			if bytes.Count(scope, declaration) != 1 {
				return r, fmt.Errorf("request-tier policy helper is not unique in %s", entry.Path)
			}
			helperStart = scopeStart + bytes.Index(scope, declaration)
			open := bytes.IndexByte(data[helperStart:scopeEnd], '{')
			if open < 0 {
				return r, fmt.Errorf("unsupported request-tier policy body in %s", entry.Path)
			}
			var ok bool
			helperEnd, ok = javascriptBlockEnd(data, helperStart+open, min(scopeEnd, helperStart+10000), 0)
			if !ok {
				return r, fmt.Errorf("unsupported request-tier policy body in %s", entry.Path)
			}
		} else if module.id == "" || !bytes.HasPrefix(region, []byte("async function(")) ||
			!bytes.Contains(region, []byte("await async function(")) {
			return r, fmt.Errorf("unsupported inline request-tier policy in %s", entry.Path)
		}
		helper := data[helperStart:helperEnd]
		ms := authDenied.FindAllSubmatchIndex(helper, -1)
		msJSON := authDeniedJSON.FindAllSubmatchIndex(helper, -1)
		ps := authDeniedPatched.FindAllIndex(helper, -1)
		psJSON := authDeniedJSONPatched.FindAllIndex(helper, -1)
		if len(ms)+len(msJSON)+len(ps)+len(psJSON) != 1 {
			return r, fmt.Errorf("unsupported request-tier auth guard in %s", entry.Path)
		}
		var authVariable []byte
		switch {
		case len(ms) == 1:
			authVariable = helper[ms[0][2]:ms[0][3]]
		case len(msJSON) == 1:
			authVariable = helper[msJSON[0][2]:msJSON[0][3]]
		case len(ps) == 1:
			authVariable = authDeniedPatched.FindSubmatch(helper)[1]
		default:
			authVariable = authDeniedJSONPatched.FindSubmatch(helper)[1]
		}
		r.policies++
		r.policyModule = module.id
		if len(ps)+len(psJSON) == 1 {
			r.patched++
		} else {
			jsonGuard := len(msJSON) == 1
			var m []int
			if jsonGuard {
				m = msJSON[0]
			} else if len(ms) == 1 {
				m = ms[0]
			} else {
				return r, fmt.Errorf("request-tier auth guard binding is missing in %s", entry.Path)
			}
			if !bytes.Equal(helper[m[2]:m[3]], helper[m[4]:m[5]]) ||
				jsonGuard && !bytes.Equal(helper[m[2]:m[3]], helper[m[6]:m[7]]) {
				return r, fmt.Errorf("request-tier auth variables differ")
			}
			value := string(helper[m[2]:m[3]])
			replacement := "!/^(chatgpt|personalAccessToken|apikey)$/.test(" + value + ")"
			if jsonGuard {
				replacement = "!/^(chatgpt|personalAccessToken|apikey|amazonBedrock)$/.test(" + value + ")"
			}
			r.add(entry, data, helperStart+m[0], helperStart+m[1], replacement)
		}
		allImports := importedAuthCall.FindAllSubmatchIndex(helper, -1)
		var imports [][]int
		for _, imported := range allImports {
			if bytes.Equal(helper[imported[2]:imported[3]], authVariable) {
				imports = append(imports, imported)
			}
		}
		if len(allImports) > 0 && call == nil {
			if module.id == "" || len(imports) != 1 {
				return r, fmt.Errorf("ambiguous imported Speed identity reader in %s (module=%s, calls=%d)", entry.Path, module.id, len(imports))
			}
			imported := imports[0]
			auth := helper[imported[2]:imported[3]]
			guards := authDeniedJSON.FindAllSubmatch(helper, -1)
			if len(guards) == 1 && (!bytes.Equal(auth, guards[0][1]) || !bytes.Equal(auth, guards[0][2]) || !bytes.Equal(auth, guards[0][3])) {
				return r, fmt.Errorf("Speed identity result differs from its policy in %s", entry.Path)
			}
			alias := string(helper[imported[4]:imported[5]])
			export := string(helper[imported[6]:imported[7]])
			binding := regexp.MustCompile(`(?:^|[,;]|var )` + regexp.QuoteMeta(alias) + `=` +
				regexp.QuoteMeta(module.require) + `\("([^"]+)"\)`)
			matches := binding.FindAllSubmatch(data[module.start:module.end], -1)
			if len(matches) != 1 {
				return r, fmt.Errorf("Speed identity module binding is not unique in %s", entry.Path)
			}
			r.accountReferences = append(r.accountReferences, rspackReference{string(matches[0][1]), export})
		}
		accountReader, err := scanSpeedAccountReader(entry, data, helper, scopeStart, scopeEnd)
		if err != nil {
			return r, err
		}
		r.patched += accountReader.patched
		r.edits = append(r.edits, accountReader.edits...)
	}
	return r, nil
}

func isJavaScript(entry Entry) bool {
	return !entry.Node.Unpacked && entry.Node.Size > 0 && strings.HasSuffix(strings.ToLower(entry.Path), ".js")
}
