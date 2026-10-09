package verifier

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// NormalizedCodeResult contains the outputs of code normalization and tokenization.
type NormalizedCodeResult struct {
	NormalizedCode    string   // Code with comments removed and whitespaces normalized
	NormalizedSHA256  string   // SHA-256 of NormalizedCode
	StructuralCode    string   // Code with identifiers canonicalized (e.g. V0, V1...)
	StructuralSHA256  string   // SHA-256 of StructuralCode
	Tokens            []string // Canonical token sequence
	MinCoreThreshold  bool     // Whether code has enough content to be checked
}

var cKeywords = map[string]bool{
	"auto": true, "break": true, "case": true, "char": true, "const": true,
	"continue": true, "default": true, "do": true, "double": true, "else": true,
	"enum": true, "extern": true, "float": true, "for": true, "goto": true,
	"if": true, "inline": true, "int": true, "long": true, "register": true,
	"restrict": true, "return": true, "short": true, "signed": true, "sizeof": true,
	"static": true, "struct": true, "switch": true, "typedef": true, "union": true,
	"unsigned": true, "void": true, "volatile": true, "while": true, "bool": true,
	"true": true, "false": true, "nullptr": true, "NULL": true,
	// C++
	"class": true, "public": true, "private": true, "protected": true,
	"virtual": true, "friend": true, "operator": true, "new": true, "delete": true,
	"this": true, "namespace": true, "using": true, "template": true, "typename": true,
	"try": true, "catch": true, "throw": true, "const_cast": true, "static_cast": true,
	"dynamic_cast": true, "reinterpret_cast": true, "std": true,
	// CUDA & GPU Intrinsics
	"__global__": true, "__device__": true, "__host__": true, "__shared__": true,
	"__constant__": true, "__syncthreads": true, "__syncwarp": true,
	"blockIdx": true, "threadIdx": true, "blockDim": true, "gridDim": true, "warpSize": true,
	"cudaMalloc": true, "cudaFree": true, "cudaMemcpy": true, "cudaMemcpyHostToDevice": true,
	"cudaMemcpyDeviceToHost": true, "cudaMemcpyDeviceToDevice": true,
	"cudaDeviceSynchronize": true, "cudaGetLastError": true, "cudaSuccess": true,
	"cudaError_t": true, "dim3": true,
	// Parallel Computing (OpenMP, MPI, Pthreads)
	"pragma": true, "omp": true, "parallel": true, "critical": true, "barrier": true,
	"reduction": true, "atomic": true, "master": true, "single": true, "section": true,
	"sections": true, "task": true, "MPI_Init": true, "MPI_Finalize": true, "MPI_Comm_rank": true,
	"MPI_Comm_size": true, "MPI_Send": true, "MPI_Recv": true, "MPI_Bcast": true,
	"pthread_t": true, "pthread_create": true, "pthread_join": true, "pthread_mutex_t": true,
	// Standard Library Functions & Math
	"printf": true, "scanf": true, "malloc": true, "free": true, "memcpy": true,
	"memset": true, "sqrt": true, "pow": true, "exp": true, "sin": true, "cos": true,
	"main": true,
}

var pyKeywords = map[string]bool{
	"and": true, "as": true, "assert": true, "break": true, "class": true, "continue": true,
	"def": true, "del": true, "elif": true, "else": true, "except": true, "False": true,
	"finally": true, "for": true, "from": true, "global": true, "if": true, "import": true,
	"in": true, "is": true, "lambda": true, "None": true, "nonlocal": true, "not": true,
	"or": true, "pass": true, "raise": true, "return": true, "True": true, "try": true,
	"while": true, "with": true, "yield": true, "print": true, "range": true, "len": true,
}

// StripComments removes single-line and multi-line comments from source code.
func StripComments(content []byte, ext string) string {
	ext = strings.ToLower(ext)
	isPython := ext == ".py"

	var sb strings.Builder
	runes := []rune(string(content))
	n := len(runes)
	i := 0

	for i < n {
		r := runes[i]

		// String literal handling (skip comment detection inside strings)
		if r == '"' || r == '\'' {
			quote := r
			// Check Python triple quotes
			if isPython && i+2 < n && runes[i+1] == quote && runes[i+2] == quote {
				i += 3
				for i < n {
					if runes[i] == quote && i+2 < n && runes[i+1] == quote && runes[i+2] == quote {
						i += 3
						break
					}
					i++
				}
				sb.WriteRune(' ')
				continue
			}

			sb.WriteRune(quote)
			i++
			for i < n {
				cur := runes[i]
				sb.WriteRune(cur)
				if cur == '\\' && i+1 < n {
					i++
					sb.WriteRune(runes[i])
				} else if cur == quote {
					i++
					break
				}
				i++
			}
			continue
		}

		// Python / Shell comments: # ...
		if isPython && r == '#' {
			for i < n && runes[i] != '\n' {
				i++
			}
			if i < n && runes[i] == '\n' {
				sb.WriteRune('\n')
				i++
			}
			continue
		}

		// C-style comments
		if !isPython && r == '/' && i+1 < n {
			next := runes[i+1]
			if next == '/' {
				// Single-line comment: // ...
				i += 2
				for i < n && runes[i] != '\n' {
					i++
				}
				if i < n && runes[i] == '\n' {
					sb.WriteRune('\n')
					i++
				}
				continue
			} else if next == '*' {
				// Multi-line comment: /* ... */
				i += 2
				for i+1 < n && !(runes[i] == '*' && runes[i+1] == '/') {
					i++
				}
				if i+1 < n {
					i += 2
				} else {
					i = n
				}
				sb.WriteRune(' ')
				continue
			}
		}

		sb.WriteRune(r)
		i++
	}

	return sb.String()
}

// NormalizeWhitespace normalizes spacing and line breaks.
func NormalizeWhitespace(text string) string {
	// Replace all whitespace sequences with a single space
	reSpaces := regexp.MustCompile(`\s+`)
	clean := reSpaces.ReplaceAllString(text, " ")

	// Remove space around common operators and punctuation to eliminate style differences
	rePunct := regexp.MustCompile(`\s*([\{\}\(\)\[\];,\:\+\-\*\/\%\=\<\>\!\&\|\^\~\?])\s*`)
	clean = rePunct.ReplaceAllString(clean, "$1")

	return strings.TrimSpace(clean)
}

// CanonicalizeTokens parses the code into structural tokens, mapping custom identifiers to V0, V1...
func CanonicalizeTokens(text string, ext string) (string, []string) {
	isPython := strings.ToLower(ext) == ".py"
	keywords := cKeywords
	if isPython {
		keywords = pyKeywords
	}

	var tokens []string
	var sb strings.Builder

	runes := []rune(text)
	n := len(runes)
	i := 0

	varMap := make(map[string]string)
	varCounter := 0

	for i < n {
		r := runes[i]

		// Skip whitespace
		if unicode.IsSpace(r) {
			i++
			continue
		}

		// String literals
		if r == '"' || r == '\'' {
			quote := r
			i++
			for i < n {
				cur := runes[i]
				if cur == '\\' && i+1 < n {
					i += 2
					continue
				}
				if cur == quote {
					i++
					break
				}
				i++
			}
			tokens = append(tokens, "STR")
			sb.WriteString("STR")
			continue
		}

		// Numbers
		if unicode.IsDigit(r) {
			numStart := i
			for i < n && (unicode.IsDigit(runes[i]) || runes[i] == '.' || runes[i] == 'x' || runes[i] == 'X' || (runes[i] >= 'a' && runes[i] <= 'f') || (runes[i] >= 'A' && runes[i] <= 'F') || runes[i] == 'u' || runes[i] == 'U' || runes[i] == 'l' || runes[i] == 'L' || runes[i] == 'f' || runes[i] == 'F') {
				i++
			}
			numStr := string(runes[numStart:i])
			if numStr == "0" || numStr == "1" {
				tokens = append(tokens, numStr)
				sb.WriteString(numStr)
			} else {
				tokens = append(tokens, "NUM")
				sb.WriteString("NUM")
			}
			continue
		}

		// Identifiers and Keywords
		if unicode.IsLetter(r) || r == '_' {
			start := i
			for i < n && (unicode.IsLetter(runes[i]) || unicode.IsDigit(runes[i]) || runes[i] == '_') {
				i++
			}
			ident := string(runes[start:i])

			if keywords[ident] {
				tokens = append(tokens, ident)
				sb.WriteString(ident)
			} else {
				canon, ok := varMap[ident]
				if !ok {
					canon = "V" + strconv.Itoa(varCounter)
					varCounter++
					varMap[ident] = canon
				}
				tokens = append(tokens, canon)
				sb.WriteString(canon)
			}
			continue
		}

		// Multi-character operators
		if i+1 < n {
			two := string(runes[i : i+2])
			if two == "==" || two == "!=" || two == "<=" || two == ">=" || two == "&&" || two == "||" || two == "++" || two == "--" || two == "+=" || two == "-=" || two == "*=" || two == "/=" || two == "->" || two == "::" || two == "<<" || two == ">>" {
				tokens = append(tokens, two)
				sb.WriteString(two)
				i += 2
				continue
			}
		}

		// Single character punctuation / operator
		ch := string(r)
		tokens = append(tokens, ch)
		sb.WriteString(ch)
		i++
	}

	return sb.String(), tokens
}

// ComputeNormalizedCode analyzes source code bytes and returns normalized and structural fingerprints.
func ComputeNormalizedCode(content []byte, filename string) *NormalizedCodeResult {
	ext := filepath.Ext(filename)

	// 1. Strip comments
	noComments := StripComments(content, ext)

	// 2. Whitespace normalization
	normCode := NormalizeWhitespace(noComments)
	normHasher := sha256.New()
	normHasher.Write([]byte(normCode))
	normSHA := hex.EncodeToString(normHasher.Sum(nil))

	// 3. Structural Tokenization
	structCode, tokens := CanonicalizeTokens(normCode, ext)
	structHasher := sha256.New()
	structHasher.Write([]byte(structCode))
	structSHA := hex.EncodeToString(structHasher.Sum(nil))

	// Code must have non-trivial content to qualify for structural collision
	minThreshold := len(tokens) >= 20 && len(normCode) >= 60

	return &NormalizedCodeResult{
		NormalizedCode:   normCode,
		NormalizedSHA256: normSHA,
		StructuralCode:   structCode,
		StructuralSHA256: structSHA,
		Tokens:           tokens,
		MinCoreThreshold: minThreshold,
	}
}

// ComputeTokenSimilarity calculates Jaccard similarity between two token sequences using 4-gram sliding windows.
func ComputeTokenSimilarity(tokensA, tokensB []string) float64 {
	const nGram = 4
	if len(tokensA) < nGram || len(tokensB) < nGram {
		return 0.0
	}

	makeGrams := func(tokens []string) map[string]struct{} {
		grams := make(map[string]struct{}, len(tokens)-nGram+1)
		for i := 0; i <= len(tokens)-nGram; i++ {
			gram := strings.Join(tokens[i:i+nGram], " ")
			grams[gram] = struct{}{}
		}
		return grams
	}

	gramsA := makeGrams(tokensA)
	gramsB := makeGrams(tokensB)

	intersection := 0
	for g := range gramsA {
		if _, ok := gramsB[g]; ok {
			intersection++
		}
	}

	union := len(gramsA) + len(gramsB) - intersection
	if union == 0 {
		return 0.0
	}

	return float64(intersection) / float64(union)
}
