package migrate

import (
	"sort"
	"strings"

	"github.com/greatliontech/pb/internal/check"
)

// Ruleset is the module path of the ruleset the migrated lint file
// imports (REQ-migrate-rules): buf's rules, each under buf's id, each
// tagged with buf's categories.
const Ruleset = "github.com/greatliontech/buf-rules"

// RulesetAlias is the alias the lint file imports the ruleset under
// (REQ-migrate-rules).
const RulesetAlias = "buf"

// rulesetFact is the source of the report's fact for the ruleset the
// lint file imports: mapped at the version discovered or given,
// unmapped where none was.
const rulesetFact = "the lint file's rulesets " + Ruleset

// rulesetRule is what the migration knows of one rule of the ruleset:
// its kind, its target and the categories it carries as tags.
type rulesetRule struct {
	kind   check.Kind
	target check.Target
	tags   string // space-separated
}

// positionless reports whether a rule of the ruleset, by its bare id,
// yields findings without a position — a package or set rule
// (check-rules.md REQ-rules-finding-location) — which no comment
// suppresses and no path glob but `**` reaches.
// fileRule reports whether a rule of the ruleset, by its bare id,
// targets the file: its finding sits at the file's first lexical
// element, whatever statement buf annotated.
func fileRule(id string) bool {
	r, ok := rulesetRules[id]
	return ok && r.target == check.TargetFile
}

func positionless(id string) bool {
	r, ok := rulesetRules[id]
	return ok && (r.target == check.TargetPackage || r.target == check.TargetSet)
}

// rulesetRules is the ruleset's rules by id, the ruleset at the
// version the dependency table pins, held to the ruleset's own files
// by TestRulesetTable. A buf id or category absent here is one the
// ruleset does not declare — PROTOVALIDATE, FILE_SAME_PHP_GENERIC_SERVICES,
// a deprecated v1 category — and migrates as an unmapped fact.
var rulesetRules = map[string]rulesetRule{
	"COMMENT_ENUM":                                                    {check.KindLint, "enum", "COMMENTS"},
	"COMMENT_ENUM_VALUE":                                              {check.KindLint, "enum-value", "COMMENTS"},
	"COMMENT_FIELD":                                                   {check.KindLint, "field", "COMMENTS"},
	"COMMENT_MESSAGE":                                                 {check.KindLint, "message", "COMMENTS"},
	"COMMENT_ONEOF":                                                   {check.KindLint, "oneof", "COMMENTS"},
	"COMMENT_RPC":                                                     {check.KindLint, "method", "COMMENTS"},
	"COMMENT_SERVICE":                                                 {check.KindLint, "service", "COMMENTS"},
	"DIRECTORY_SAME_PACKAGE":                                          {check.KindLint, "set", "MINIMAL BASIC STANDARD"},
	"ENUM_FIRST_VALUE_ZERO":                                           {check.KindLint, "enum", "BASIC STANDARD"},
	"ENUM_NO_ALLOW_ALIAS":                                             {check.KindLint, "enum", "BASIC STANDARD"},
	"ENUM_NO_DELETE":                                                  {check.KindBreaking, "enum", "FILE"},
	"ENUM_NO_DELETE_STABLE":                                           {check.KindBreaking, "enum", "FILE_STABLE"},
	"ENUM_PASCAL_CASE":                                                {check.KindLint, "enum", "BASIC STANDARD"},
	"ENUM_SAME_JSON_FORMAT":                                           {check.KindBreaking, "enum", "FILE PACKAGE WIRE_JSON"},
	"ENUM_SAME_JSON_FORMAT_STABLE":                                    {check.KindBreaking, "enum", "FILE_STABLE PACKAGE_STABLE WIRE_JSON_STABLE"},
	"ENUM_SAME_TYPE":                                                  {check.KindBreaking, "enum", "FILE PACKAGE"},
	"ENUM_SAME_TYPE_STABLE":                                           {check.KindBreaking, "enum", "FILE_STABLE PACKAGE_STABLE"},
	"ENUM_VALUE_NO_DELETE":                                            {check.KindBreaking, "enum-value", "FILE PACKAGE"},
	"ENUM_VALUE_NO_DELETE_STABLE":                                     {check.KindBreaking, "enum-value", "FILE_STABLE PACKAGE_STABLE"},
	"ENUM_VALUE_NO_DELETE_UNLESS_NAME_RESERVED":                       {check.KindBreaking, "enum-value", "WIRE_JSON"},
	"ENUM_VALUE_NO_DELETE_UNLESS_NAME_RESERVED_STABLE":                {check.KindBreaking, "enum-value", "WIRE_JSON_STABLE"},
	"ENUM_VALUE_NO_DELETE_UNLESS_NUMBER_RESERVED":                     {check.KindBreaking, "enum-value", "WIRE WIRE_JSON"},
	"ENUM_VALUE_NO_DELETE_UNLESS_NUMBER_RESERVED_STABLE":              {check.KindBreaking, "enum-value", "WIRE_STABLE WIRE_JSON_STABLE"},
	"ENUM_VALUE_PREFIX":                                               {check.KindLint, "enum-value", "STANDARD"},
	"ENUM_VALUE_SAME_NAME":                                            {check.KindBreaking, "enum-value", "FILE PACKAGE WIRE_JSON"},
	"ENUM_VALUE_SAME_NAME_STABLE":                                     {check.KindBreaking, "enum-value", "FILE_STABLE PACKAGE_STABLE WIRE_JSON_STABLE"},
	"ENUM_VALUE_UPPER_SNAKE_CASE":                                     {check.KindLint, "enum-value", "BASIC STANDARD"},
	"ENUM_ZERO_VALUE_SUFFIX":                                          {check.KindLint, "enum-value", "STANDARD"},
	"EXTENSION_MESSAGE_NO_DELETE":                                     {check.KindBreaking, "message", "FILE PACKAGE"},
	"EXTENSION_MESSAGE_NO_DELETE_STABLE":                              {check.KindBreaking, "message", "FILE_STABLE PACKAGE_STABLE"},
	"EXTENSION_NO_DELETE":                                             {check.KindBreaking, "extension", "FILE"},
	"EXTENSION_NO_DELETE_STABLE":                                      {check.KindBreaking, "extension", "FILE_STABLE"},
	"FIELD_LOWER_SNAKE_CASE":                                          {check.KindLint, "field", "BASIC STANDARD"},
	"FIELD_NOT_REQUIRED":                                              {check.KindLint, "field", "BASIC STANDARD"},
	"FIELD_NO_DELETE":                                                 {check.KindBreaking, "field", "FILE PACKAGE"},
	"FIELD_NO_DELETE_STABLE":                                          {check.KindBreaking, "field", "FILE_STABLE PACKAGE_STABLE"},
	"FIELD_NO_DELETE_UNLESS_NAME_RESERVED":                            {check.KindBreaking, "field", "WIRE_JSON"},
	"FIELD_NO_DELETE_UNLESS_NAME_RESERVED_STABLE":                     {check.KindBreaking, "field", "WIRE_JSON_STABLE"},
	"FIELD_NO_DELETE_UNLESS_NUMBER_RESERVED":                          {check.KindBreaking, "field", "WIRE WIRE_JSON"},
	"FIELD_NO_DELETE_UNLESS_NUMBER_RESERVED_STABLE":                   {check.KindBreaking, "field", "WIRE_STABLE WIRE_JSON_STABLE"},
	"FIELD_SAME_CARDINALITY":                                          {check.KindBreaking, "field", "FILE PACKAGE"},
	"FIELD_SAME_CARDINALITY_STABLE":                                   {check.KindBreaking, "field", "FILE_STABLE PACKAGE_STABLE"},
	"FIELD_SAME_CPP_STRING_TYPE":                                      {check.KindBreaking, "field", "FILE PACKAGE"},
	"FIELD_SAME_CPP_STRING_TYPE_STABLE":                               {check.KindBreaking, "field", "FILE_STABLE PACKAGE_STABLE"},
	"FIELD_SAME_CTYPE":                                                {check.KindBreaking, "field", ""},
	"FIELD_SAME_CTYPE_STABLE":                                         {check.KindBreaking, "field", ""},
	"FIELD_SAME_DEFAULT":                                              {check.KindBreaking, "field", "FILE PACKAGE WIRE_JSON WIRE"},
	"FIELD_SAME_DEFAULT_STABLE":                                       {check.KindBreaking, "field", "FILE_STABLE PACKAGE_STABLE WIRE_JSON_STABLE WIRE_STABLE"},
	"FIELD_SAME_JAVA_UTF8_VALIDATION":                                 {check.KindBreaking, "field", "FILE PACKAGE"},
	"FIELD_SAME_JAVA_UTF8_VALIDATION_STABLE":                          {check.KindBreaking, "field", "FILE_STABLE PACKAGE_STABLE"},
	"FIELD_SAME_JSON_NAME":                                            {check.KindBreaking, "field", "FILE PACKAGE WIRE_JSON"},
	"FIELD_SAME_JSON_NAME_STABLE":                                     {check.KindBreaking, "field", "FILE_STABLE PACKAGE_STABLE WIRE_JSON_STABLE"},
	"FIELD_SAME_JSTYPE":                                               {check.KindBreaking, "field", "FILE PACKAGE"},
	"FIELD_SAME_JSTYPE_STABLE":                                        {check.KindBreaking, "field", "FILE_STABLE PACKAGE_STABLE"},
	"FIELD_SAME_LABEL":                                                {check.KindBreaking, "field", ""},
	"FIELD_SAME_LABEL_STABLE":                                         {check.KindBreaking, "field", ""},
	"FIELD_SAME_NAME":                                                 {check.KindBreaking, "field", "FILE PACKAGE WIRE_JSON"},
	"FIELD_SAME_NAME_STABLE":                                          {check.KindBreaking, "field", "FILE_STABLE PACKAGE_STABLE WIRE_JSON_STABLE"},
	"FIELD_SAME_ONEOF":                                                {check.KindBreaking, "field", "FILE PACKAGE WIRE_JSON WIRE"},
	"FIELD_SAME_ONEOF_STABLE":                                         {check.KindBreaking, "field", "FILE_STABLE PACKAGE_STABLE WIRE_JSON_STABLE WIRE_STABLE"},
	"FIELD_SAME_TYPE":                                                 {check.KindBreaking, "field", "FILE PACKAGE"},
	"FIELD_SAME_TYPE_STABLE":                                          {check.KindBreaking, "field", "FILE_STABLE PACKAGE_STABLE"},
	"FIELD_SAME_UTF8_VALIDATION":                                      {check.KindBreaking, "field", "FILE PACKAGE"},
	"FIELD_SAME_UTF8_VALIDATION_STABLE":                               {check.KindBreaking, "field", "FILE_STABLE PACKAGE_STABLE"},
	"FIELD_WIRE_COMPATIBLE_CARDINALITY":                               {check.KindBreaking, "field", "WIRE"},
	"FIELD_WIRE_COMPATIBLE_CARDINALITY_STABLE":                        {check.KindBreaking, "field", "WIRE_STABLE"},
	"FIELD_WIRE_COMPATIBLE_TYPE":                                      {check.KindBreaking, "field", "WIRE"},
	"FIELD_WIRE_COMPATIBLE_TYPE_STABLE":                               {check.KindBreaking, "field", "WIRE_STABLE"},
	"FIELD_WIRE_JSON_COMPATIBLE_CARDINALITY":                          {check.KindBreaking, "field", "WIRE_JSON"},
	"FIELD_WIRE_JSON_COMPATIBLE_CARDINALITY_STABLE":                   {check.KindBreaking, "field", "WIRE_JSON_STABLE"},
	"FIELD_WIRE_JSON_COMPATIBLE_TYPE":                                 {check.KindBreaking, "field", "WIRE_JSON"},
	"FIELD_WIRE_JSON_COMPATIBLE_TYPE_STABLE":                          {check.KindBreaking, "field", "WIRE_JSON_STABLE"},
	"FILE_LOWER_SNAKE_CASE":                                           {check.KindLint, "file", "STANDARD"},
	"FILE_NO_DELETE":                                                  {check.KindBreaking, "file", "FILE"},
	"FILE_NO_DELETE_STABLE":                                           {check.KindBreaking, "file", "FILE_STABLE"},
	"FILE_SAME_CC_ENABLE_ARENAS":                                      {check.KindBreaking, "file", "FILE PACKAGE"},
	"FILE_SAME_CC_ENABLE_ARENAS_STABLE":                               {check.KindBreaking, "file", "FILE_STABLE PACKAGE_STABLE"},
	"FILE_SAME_CC_GENERIC_SERVICES":                                   {check.KindBreaking, "file", "FILE PACKAGE"},
	"FILE_SAME_CC_GENERIC_SERVICES_STABLE":                            {check.KindBreaking, "file", "FILE_STABLE PACKAGE_STABLE"},
	"FILE_SAME_CSHARP_NAMESPACE":                                      {check.KindBreaking, "file", "FILE PACKAGE"},
	"FILE_SAME_CSHARP_NAMESPACE_STABLE":                               {check.KindBreaking, "file", "FILE_STABLE PACKAGE_STABLE"},
	"FILE_SAME_GO_PACKAGE":                                            {check.KindBreaking, "file", "FILE PACKAGE"},
	"FILE_SAME_GO_PACKAGE_STABLE":                                     {check.KindBreaking, "file", "FILE_STABLE PACKAGE_STABLE"},
	"FILE_SAME_JAVA_GENERIC_SERVICES":                                 {check.KindBreaking, "file", "FILE PACKAGE"},
	"FILE_SAME_JAVA_GENERIC_SERVICES_STABLE":                          {check.KindBreaking, "file", "FILE_STABLE PACKAGE_STABLE"},
	"FILE_SAME_JAVA_MULTIPLE_FILES":                                   {check.KindBreaking, "file", "FILE PACKAGE"},
	"FILE_SAME_JAVA_MULTIPLE_FILES_STABLE":                            {check.KindBreaking, "file", "FILE_STABLE PACKAGE_STABLE"},
	"FILE_SAME_JAVA_OUTER_CLASSNAME":                                  {check.KindBreaking, "file", "FILE PACKAGE"},
	"FILE_SAME_JAVA_OUTER_CLASSNAME_STABLE":                           {check.KindBreaking, "file", "FILE_STABLE PACKAGE_STABLE"},
	"FILE_SAME_JAVA_PACKAGE":                                          {check.KindBreaking, "file", "FILE PACKAGE"},
	"FILE_SAME_JAVA_PACKAGE_STABLE":                                   {check.KindBreaking, "file", "FILE_STABLE PACKAGE_STABLE"},
	"FILE_SAME_JAVA_STRING_CHECK_UTF8":                                {check.KindBreaking, "file", ""},
	"FILE_SAME_JAVA_STRING_CHECK_UTF8_STABLE":                         {check.KindBreaking, "file", ""},
	"FILE_SAME_OBJC_CLASS_PREFIX":                                     {check.KindBreaking, "file", "FILE PACKAGE"},
	"FILE_SAME_OBJC_CLASS_PREFIX_STABLE":                              {check.KindBreaking, "file", "FILE_STABLE PACKAGE_STABLE"},
	"FILE_SAME_OPTIMIZE_FOR":                                          {check.KindBreaking, "file", "FILE PACKAGE"},
	"FILE_SAME_OPTIMIZE_FOR_STABLE":                                   {check.KindBreaking, "file", "FILE_STABLE PACKAGE_STABLE"},
	"FILE_SAME_PACKAGE":                                               {check.KindBreaking, "file", "FILE PACKAGE WIRE_JSON WIRE"},
	"FILE_SAME_PACKAGE_STABLE":                                        {check.KindBreaking, "file", "FILE_STABLE PACKAGE_STABLE WIRE_JSON_STABLE WIRE_STABLE"},
	"FILE_SAME_PHP_CLASS_PREFIX":                                      {check.KindBreaking, "file", "FILE PACKAGE"},
	"FILE_SAME_PHP_CLASS_PREFIX_STABLE":                               {check.KindBreaking, "file", "FILE_STABLE PACKAGE_STABLE"},
	"FILE_SAME_PHP_METADATA_NAMESPACE":                                {check.KindBreaking, "file", "FILE PACKAGE"},
	"FILE_SAME_PHP_METADATA_NAMESPACE_STABLE":                         {check.KindBreaking, "file", "FILE_STABLE PACKAGE_STABLE"},
	"FILE_SAME_PHP_NAMESPACE":                                         {check.KindBreaking, "file", "FILE PACKAGE"},
	"FILE_SAME_PHP_NAMESPACE_STABLE":                                  {check.KindBreaking, "file", "FILE_STABLE PACKAGE_STABLE"},
	"FILE_SAME_PY_GENERIC_SERVICES":                                   {check.KindBreaking, "file", "FILE PACKAGE"},
	"FILE_SAME_PY_GENERIC_SERVICES_STABLE":                            {check.KindBreaking, "file", "FILE_STABLE PACKAGE_STABLE"},
	"FILE_SAME_RUBY_PACKAGE":                                          {check.KindBreaking, "file", "FILE PACKAGE"},
	"FILE_SAME_RUBY_PACKAGE_STABLE":                                   {check.KindBreaking, "file", "FILE_STABLE PACKAGE_STABLE"},
	"FILE_SAME_SWIFT_PREFIX":                                          {check.KindBreaking, "file", "FILE PACKAGE"},
	"FILE_SAME_SWIFT_PREFIX_STABLE":                                   {check.KindBreaking, "file", "FILE_STABLE PACKAGE_STABLE"},
	"FILE_SAME_SYNTAX":                                                {check.KindBreaking, "file", "FILE PACKAGE"},
	"FILE_SAME_SYNTAX_STABLE":                                         {check.KindBreaking, "file", "FILE_STABLE PACKAGE_STABLE"},
	"IMPORT_NO_PUBLIC":                                                {check.KindLint, "file", "BASIC STANDARD"},
	"IMPORT_NO_WEAK":                                                  {check.KindLint, "file", ""},
	"IMPORT_USED":                                                     {check.KindLint, "file", "BASIC STANDARD"},
	"MESSAGE_NO_DELETE":                                               {check.KindBreaking, "message", "FILE"},
	"MESSAGE_NO_DELETE_STABLE":                                        {check.KindBreaking, "message", "FILE_STABLE"},
	"MESSAGE_NO_REMOVE_STANDARD_DESCRIPTOR_ACCESSOR":                  {check.KindBreaking, "message", "FILE PACKAGE"},
	"MESSAGE_NO_REMOVE_STANDARD_DESCRIPTOR_ACCESSOR_STABLE":           {check.KindBreaking, "message", "FILE_STABLE PACKAGE_STABLE"},
	"MESSAGE_PASCAL_CASE":                                             {check.KindLint, "message", "BASIC STANDARD"},
	"MESSAGE_SAME_JSON_FORMAT":                                        {check.KindBreaking, "message", "FILE PACKAGE WIRE_JSON"},
	"MESSAGE_SAME_JSON_FORMAT_STABLE":                                 {check.KindBreaking, "message", "FILE_STABLE PACKAGE_STABLE WIRE_JSON_STABLE"},
	"MESSAGE_SAME_MESSAGE_SET_WIRE_FORMAT":                            {check.KindBreaking, "message", ""},
	"MESSAGE_SAME_MESSAGE_SET_WIRE_FORMAT_STABLE":                     {check.KindBreaking, "message", ""},
	"MESSAGE_SAME_REQUIRED_FIELDS":                                    {check.KindBreaking, "message", "FILE PACKAGE WIRE_JSON WIRE"},
	"MESSAGE_SAME_REQUIRED_FIELDS_STABLE":                             {check.KindBreaking, "message", "FILE_STABLE PACKAGE_STABLE WIRE_JSON_STABLE WIRE_STABLE"},
	"ONEOF_LOWER_SNAKE_CASE":                                          {check.KindLint, "oneof", "BASIC STANDARD"},
	"ONEOF_NO_DELETE":                                                 {check.KindBreaking, "oneof", "FILE PACKAGE"},
	"ONEOF_NO_DELETE_STABLE":                                          {check.KindBreaking, "oneof", "FILE_STABLE PACKAGE_STABLE"},
	"PACKAGE_DEFINED":                                                 {check.KindLint, "file", "MINIMAL BASIC STANDARD"},
	"PACKAGE_DIRECTORY_MATCH":                                         {check.KindLint, "file", "MINIMAL BASIC STANDARD"},
	"PACKAGE_ENUM_NO_DELETE":                                          {check.KindBreaking, "enum", "PACKAGE"},
	"PACKAGE_ENUM_NO_DELETE_STABLE":                                   {check.KindBreaking, "enum", "PACKAGE_STABLE"},
	"PACKAGE_EXTENSION_NO_DELETE":                                     {check.KindBreaking, "extension", "PACKAGE"},
	"PACKAGE_EXTENSION_NO_DELETE_STABLE":                              {check.KindBreaking, "extension", "PACKAGE_STABLE"},
	"PACKAGE_LOWER_SNAKE_CASE":                                        {check.KindLint, "file", "BASIC STANDARD"},
	"PACKAGE_MESSAGE_NO_DELETE":                                       {check.KindBreaking, "message", "PACKAGE"},
	"PACKAGE_MESSAGE_NO_DELETE_STABLE":                                {check.KindBreaking, "message", "PACKAGE_STABLE"},
	"PACKAGE_NO_DELETE":                                               {check.KindBreaking, "package", "PACKAGE"},
	"PACKAGE_NO_DELETE_STABLE":                                        {check.KindBreaking, "package", "PACKAGE_STABLE"},
	"PACKAGE_NO_IMPORT_CYCLE":                                         {check.KindLint, "set", "MINIMAL BASIC STANDARD"},
	"PACKAGE_SAME_CSHARP_NAMESPACE":                                   {check.KindLint, "package", "BASIC STANDARD"},
	"PACKAGE_SAME_DIRECTORY":                                          {check.KindLint, "package", "MINIMAL BASIC STANDARD"},
	"PACKAGE_SAME_GO_PACKAGE":                                         {check.KindLint, "package", "BASIC STANDARD"},
	"PACKAGE_SAME_JAVA_MULTIPLE_FILES":                                {check.KindLint, "package", "BASIC STANDARD"},
	"PACKAGE_SAME_JAVA_PACKAGE":                                       {check.KindLint, "package", "BASIC STANDARD"},
	"PACKAGE_SAME_PHP_NAMESPACE":                                      {check.KindLint, "package", "BASIC STANDARD"},
	"PACKAGE_SAME_RUBY_PACKAGE":                                       {check.KindLint, "package", "BASIC STANDARD"},
	"PACKAGE_SAME_SWIFT_PREFIX":                                       {check.KindLint, "package", "BASIC STANDARD"},
	"PACKAGE_SERVICE_NO_DELETE":                                       {check.KindBreaking, "service", "PACKAGE"},
	"PACKAGE_SERVICE_NO_DELETE_STABLE":                                {check.KindBreaking, "service", "PACKAGE_STABLE"},
	"PACKAGE_VERSION_SUFFIX":                                          {check.KindLint, "file", "STANDARD"},
	"RESERVED_ENUM_NO_DELETE":                                         {check.KindBreaking, "enum", "FILE PACKAGE WIRE_JSON WIRE"},
	"RESERVED_ENUM_NO_DELETE_STABLE":                                  {check.KindBreaking, "enum", "FILE_STABLE PACKAGE_STABLE WIRE_JSON_STABLE WIRE_STABLE"},
	"RESERVED_MESSAGE_NO_DELETE":                                      {check.KindBreaking, "message", "FILE PACKAGE WIRE_JSON WIRE"},
	"RESERVED_MESSAGE_NO_DELETE_STABLE":                               {check.KindBreaking, "message", "FILE_STABLE PACKAGE_STABLE WIRE_JSON_STABLE WIRE_STABLE"},
	"RPC_NO_CLIENT_STREAMING":                                         {check.KindLint, "method", "UNARY_RPC"},
	"RPC_NO_DELETE":                                                   {check.KindBreaking, "method", "FILE PACKAGE"},
	"RPC_NO_DELETE_STABLE":                                            {check.KindBreaking, "method", "FILE_STABLE PACKAGE_STABLE"},
	"RPC_NO_SERVER_STREAMING":                                         {check.KindLint, "method", "UNARY_RPC"},
	"RPC_PASCAL_CASE":                                                 {check.KindLint, "method", "BASIC STANDARD"},
	"RPC_REQUEST_RESPONSE_UNIQUE":                                     {check.KindLint, "set", "STANDARD"},
	"RPC_REQUEST_RESPONSE_UNIQUE_ALLOW_EMPTY_REQUESTS":                {check.KindLint, "set", ""},
	"RPC_REQUEST_RESPONSE_UNIQUE_ALLOW_EMPTY_REQUESTS_RESPONSES":      {check.KindLint, "set", ""},
	"RPC_REQUEST_RESPONSE_UNIQUE_ALLOW_EMPTY_RESPONSES":               {check.KindLint, "set", ""},
	"RPC_REQUEST_RESPONSE_UNIQUE_ALLOW_SAME":                          {check.KindLint, "set", ""},
	"RPC_REQUEST_RESPONSE_UNIQUE_ALLOW_SAME_EMPTY_REQUESTS":           {check.KindLint, "set", ""},
	"RPC_REQUEST_RESPONSE_UNIQUE_ALLOW_SAME_EMPTY_REQUESTS_RESPONSES": {check.KindLint, "set", ""},
	"RPC_REQUEST_RESPONSE_UNIQUE_ALLOW_SAME_EMPTY_RESPONSES":          {check.KindLint, "set", ""},
	"RPC_REQUEST_STANDARD_NAME":                                       {check.KindLint, "method", "STANDARD"},
	"RPC_REQUEST_STANDARD_NAME_ALLOW_EMPTY":                           {check.KindLint, "method", ""},
	"RPC_RESPONSE_STANDARD_NAME":                                      {check.KindLint, "method", "STANDARD"},
	"RPC_RESPONSE_STANDARD_NAME_ALLOW_EMPTY":                          {check.KindLint, "method", ""},
	"RPC_SAME_CLIENT_STREAMING":                                       {check.KindBreaking, "method", "FILE PACKAGE WIRE_JSON WIRE"},
	"RPC_SAME_CLIENT_STREAMING_STABLE":                                {check.KindBreaking, "method", "FILE_STABLE PACKAGE_STABLE WIRE_JSON_STABLE WIRE_STABLE"},
	"RPC_SAME_IDEMPOTENCY_LEVEL":                                      {check.KindBreaking, "method", "FILE PACKAGE WIRE_JSON WIRE"},
	"RPC_SAME_IDEMPOTENCY_LEVEL_STABLE":                               {check.KindBreaking, "method", "FILE_STABLE PACKAGE_STABLE WIRE_JSON_STABLE WIRE_STABLE"},
	"RPC_SAME_REQUEST_TYPE":                                           {check.KindBreaking, "method", "FILE PACKAGE WIRE_JSON WIRE"},
	"RPC_SAME_REQUEST_TYPE_STABLE":                                    {check.KindBreaking, "method", "FILE_STABLE PACKAGE_STABLE WIRE_JSON_STABLE WIRE_STABLE"},
	"RPC_SAME_RESPONSE_TYPE":                                          {check.KindBreaking, "method", "FILE PACKAGE WIRE_JSON WIRE"},
	"RPC_SAME_RESPONSE_TYPE_STABLE":                                   {check.KindBreaking, "method", "FILE_STABLE PACKAGE_STABLE WIRE_JSON_STABLE WIRE_STABLE"},
	"RPC_SAME_SERVER_STREAMING":                                       {check.KindBreaking, "method", "FILE PACKAGE WIRE_JSON WIRE"},
	"RPC_SAME_SERVER_STREAMING_STABLE":                                {check.KindBreaking, "method", "FILE_STABLE PACKAGE_STABLE WIRE_JSON_STABLE WIRE_STABLE"},
	"SERVICE_NO_DELETE":                                               {check.KindBreaking, "service", "FILE"},
	"SERVICE_NO_DELETE_STABLE":                                        {check.KindBreaking, "service", "FILE_STABLE"},
	"SERVICE_PASCAL_CASE":                                             {check.KindLint, "service", "BASIC STANDARD"},
	"SERVICE_SUFFIX":                                                  {check.KindLint, "service", "STANDARD"},
	"STABLE_PACKAGE_NO_IMPORT_UNSTABLE":                               {check.KindLint, "file", ""},
	"SYNTAX_SPECIFIED":                                                {check.KindLint, "file", "BASIC STANDARD"},
}

// rulesetTags is, per kind, every tag a rule of the kind carries;
// rulesetTagged is, per kind and tag, the ids of the rules carrying
// it in raw-byte order. Both are indexes over rulesetRules, built
// once.
var rulesetTags, rulesetTagged = func() (map[check.Kind]map[string]bool, map[check.Kind]map[string][]string) {
	tags := map[check.Kind]map[string]bool{}
	tagged := map[check.Kind]map[string][]string{}
	for id, r := range rulesetRules {
		if tags[r.kind] == nil {
			tags[r.kind] = map[string]bool{}
			tagged[r.kind] = map[string][]string{}
		}
		for _, t := range strings.Fields(r.tags) {
			tags[r.kind][t] = true
			tagged[r.kind][t] = append(tagged[r.kind][t], id)
		}
	}
	for _, byTag := range tagged {
		for _, ids := range byTag {
			sort.Strings(ids)
		}
	}
	return tags, tagged
}()
