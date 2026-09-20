// Package check is the check domain's root: the vocabulary the rule
// files, the lint file and the findings share (check-rules.md) — a
// rule's kind, its target, its severity — and the CEL environment
// versions the engine provides. The values are wire facts: they are
// the spellings a rule file carries, so every consumer names them from
// here and no two packages can drift on one. The domain's mechanisms —
// the rule files, the environment, lint and breaking evaluation, the
// lint file — are its subpackages.
package check

// Kind is a rule's kind (REQ-rules-file-schema): a lint rule judges one
// schema, a breaking rule an aligned pair.
type Kind string

// The kinds, as a rule file spells them.
const (
	KindLint     Kind = "lint"
	KindBreaking Kind = "breaking"
)

// Severity is a rule's severity (REQ-rules-file-schema).
type Severity string

// The severities, as a rule file spells them.
const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
)

// Target is the entity kind a rule binds (check-rules.md, the target
// term).
type Target string

// The targets, as a rule file spells them.
const (
	TargetFile      Target = "file"
	TargetPackage   Target = "package"
	TargetMessage   Target = "message"
	TargetField     Target = "field"
	TargetOneof     Target = "oneof"
	TargetEnum      Target = "enum"
	TargetEnumValue Target = "enum-value"
	TargetService   Target = "service"
	TargetMethod    Target = "method"
	TargetExtension Target = "extension"
	TargetSet       Target = "set"
)

var targets = [...]Target{TargetFile, TargetPackage, TargetMessage, TargetField, TargetOneof, TargetEnum, TargetEnumValue, TargetService, TargetMethod, TargetExtension, TargetSet}

// Targets lists every target in the order the term states them.
func Targets() []Target { return append([]Target(nil), targets[:]...) }

var (
	kinds      = [...]Kind{KindLint, KindBreaking}
	severities = [...]Severity{SeverityError, SeverityWarning}
)

// ParseKind is the kind s spells, or false.
func ParseKind(s string) (Kind, bool) { return parse(s, kinds[:]) }

// ParseSeverity is the severity s spells, or false.
func ParseSeverity(s string) (Severity, bool) { return parse(s, severities[:]) }

// ParseTarget is the target s spells, or false.
func ParseTarget(s string) (Target, bool) { return parse(s, targets[:]) }

// parse is the one closed-vocabulary reader: s is a member of all
// exactly as spelled, or nothing.
func parse[T ~string](s string, all []T) (T, bool) {
	for _, v := range all {
		if T(s) == v {
			return v, true
		}
	}
	return "", false
}

var environments = [...]int{1}

// Environments lists the CEL environment versions the engine provides,
// in order; a rule file targeting any other is refused
// (REQ-rules-env-versioned). Additions to an environment bump the
// version: the contract a rule targets never changes under its number.
func Environments() []int { return append([]int(nil), environments[:]...) }

// ProvidesEnvironment reports whether the engine provides environment
// version v.
func ProvidesEnvironment(v int) bool {
	for _, e := range environments {
		if v == e {
			return true
		}
	}
	return false
}

// Finding is one rule's false verdict (REQ-rules-verdict), located per
// REQ-rules-finding-location: Line and Column 1-based, the column in
// code points; Line zero for a finding without a position (a package
// rule's, at the package's first checked file); Path empty for a
// finding without a location (a set rule's); Base for a finding in
// the comparison base, a pair whose new side is absent.
type Finding struct {
	RuleID   string
	Severity Severity
	Message  string
	Path     string
	Line     int
	Column   int
	Base     bool
}

// Report is a check run's outcome: the findings in evaluation order —
// rules in the order given, entities in population order — and the
// number of rules enabled, the ones given (REQ-rules-no-defaults:
// zero rules, zero findings).
type Report struct {
	Findings []Finding
	Rules    int
}
